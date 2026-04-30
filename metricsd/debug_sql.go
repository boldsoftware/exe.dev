// Debug SQL console: read-only SQL queries against the metricsd DuckDB.
//
// We use DuckDB's own parser as the read-only oracle. The flow is:
//
//  1. Strip comments, reject unquoted `;` (duckdb-go's PrepareContext
//     actually *executes* all-but-the-last statement when handed a
//     multi-statement input -- see vendor connection.go's prepareStmts --
//     so we have to enforce single-statement before we ever call Prepare).
//     Reject dollar-quoted strings ($$..$$ / $tag$..$tag$) since our `;`
//     lexer doesn't understand them.
//  2. Reject `EXPLAIN ANALYZE` textually: DuckDB's StatementType reports
//     plain EXPLAIN and EXPLAIN ANALYZE identically, but EXPLAIN ANALYZE
//     executes the analyzed statement at execute time.
//  3. PrepareContext on a dedicated *sql.Conn, ask DuckDB for the
//     statement type via duckdb.Stmt.StatementType(), and allow only
//     SELECT (covers WITH/FROM/VALUES/SHOW/DESCRIBE/read-only PRAGMA --
//     they all parse as SELECT) and EXPLAIN. Anything else (INSERT,
//     UPDATE, DELETE, COPY, ATTACH, DETACH, SET, PRAGMA-that-writes,
//     CALL, VACUUM, CREATE, DROP, ...) is rejected, *including* CTE-
//     wrapped writes (`WITH c AS (...) DELETE FROM t` parses as DELETE).
//  4. Execute the query through the same conn, then poison the conn so
//     any session state cannot leak into the rest of the metricsd pool.
//
// Concurrency is capped (4 in-flight) and a query timeout (default 30s,
// max 2m) is enforced via context. Result rows are capped (default
// 1000, max 100000) and per-cell bytes truncated at 64 KiB to defend
// against pathological wide rows. BLOB cells are hex-encoded.
//
// Output: JSON `{columns: [...], rows: [[...], ...], elapsed_ms, truncated}`
// or `text/csv` if `?format=csv`.
//
// Query parameters (limit, timeout, format) are read from the URL even
// when the body is POSTed; only `q` is taken from the body.
package metricsd

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/duckdb/duckdb-go/v2"
)

const (
	debugSQLDefaultLimit  = 1000
	debugSQLMaxLimit      = 100000
	debugSQLDefaultTO     = 30 * time.Second
	debugSQLMaxTO         = 2 * time.Minute
	debugSQLMaxCellBytes  = 64 * 1024   // truncate any single cell beyond this
	debugSQLMaxRowBytes   = 1024 * 1024 // truncate any single row beyond ~1MiB
	debugSQLMaxConcurrent = 4           // in-flight cap for /debug/sql/run
)

var debugSQLSem = make(chan struct{}, debugSQLMaxConcurrent)

type debugSQLResponse struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	ElapsedMS int64    `json:"elapsed_ms"`
	Truncated bool     `json:"truncated"`
	Error     string   `json:"error,omitempty"`
}

// stripSQLComments replaces -- line comments and /* ... */ block
// comments with a single space (so `SELECT/*x*/1` becomes `SELECT 1`,
// not `SELECT1`), while preserving single-quoted string literals and
// double-quoted identifiers.
func stripSQLComments(q string) string {
	var b strings.Builder
	b.Grow(len(q))
	i, n := 0, len(q)
	for i < n {
		c := q[i]
		switch {
		case c == '\'' || c == '"':
			quote := c
			b.WriteByte(c)
			i++
			for i < n {
				ch := q[i]
				b.WriteByte(ch)
				i++
				if ch == quote {
					if i < n && q[i] == quote {
						b.WriteByte(q[i])
						i++
						continue
					}
					break
				}
			}
		case c == '-' && i+1 < n && q[i+1] == '-':
			for i < n && q[i] != '\n' {
				i++
			}
			// Leave the trailing newline (if any) so following tokens
			// stay on the next line; emit a space as a hard separator
			// in case there's no newline (end of input).
			b.WriteByte(' ')
		case c == '/' && i+1 < n && q[i+1] == '*':
			i += 2
			for i+1 < n && !(q[i] == '*' && q[i+1] == '/') {
				i++
			}
			if i+1 < n {
				i += 2
			} else {
				i = n
			}
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// containsUnquotedSemicolon reports whether s contains a `;` outside a
// single- or double-quoted string. A trailing semicolon (after stripping
// trailing whitespace) is allowed.
func containsUnquotedSemicolon(s string) bool {
	s = strings.TrimRight(s, " \t\r\n")
	s = strings.TrimSuffix(s, ";")
	i, n := 0, len(s)
	for i < n {
		c := s[i]
		if c == '\'' || c == '"' {
			quote := c
			i++
			for i < n {
				ch := s[i]
				i++
				if ch == quote {
					if i < n && s[i] == quote {
						i++
						continue
					}
					break
				}
			}
			continue
		}
		if c == ';' {
			return true
		}
		i++
	}
	return false
}

// containsDollarQuote conservatively rejects $$..$$ and $tag$..$tag$.
// We don't model these in our `;` lexer, and ignoring them would let an
// attacker hide a `;` inside a dollar-quoted body and slip a second
// statement past PrepareContext.
func containsDollarQuote(s string) bool {
	i, n := 0, len(s)
	for i < n {
		c := s[i]
		if c == '\'' || c == '"' {
			quote := c
			i++
			for i < n {
				ch := s[i]
				i++
				if ch == quote {
					if i < n && s[i] == quote {
						i++
						continue
					}
					break
				}
			}
			continue
		}
		if c == '$' && i+1 < n {
			next := s[i+1]
			if next == '$' || (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') || next == '_' {
				return true
			}
		}
		i++
	}
	return false
}

// startsWithExplainAnalyze reports whether the (comment-stripped, leading-
// whitespace-trimmed) query begins with the keywords EXPLAIN ANALYZE.
// We use this to distinguish EXPLAIN (safe, just produces a plan) from
// EXPLAIN ANALYZE (executes the inner statement) since DuckDB reports
// the same StatementType for both.
func startsWithExplainAnalyze(s string) bool {
	s = strings.TrimLeft(s, " \t\r\n")
	kw, rest := splitHeadKeyword(s)
	if kw != "EXPLAIN" {
		return false
	}
	next, _ := splitHeadKeyword(strings.TrimLeft(rest, " \t\r\n"))
	return next == "ANALYZE"
}

func splitHeadKeyword(s string) (kw, rest string) {
	end := 0
	for end < len(s) {
		c := s[end]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			end++
			continue
		}
		break
	}
	return strings.ToUpper(s[:end]), s[end:]
}

// preflightQuery does the structural checks that must happen before we
// hand the query to PrepareContext.
func preflightQuery(q string) (string, error) {
	clean := strings.TrimSpace(stripSQLComments(q))
	if clean == "" {
		return "", fmt.Errorf("empty query")
	}
	if containsUnquotedSemicolon(clean) {
		return "", fmt.Errorf("only one statement allowed (remove `;`)")
	}
	if containsDollarQuote(clean) {
		return "", fmt.Errorf("dollar-quoted strings are not supported on /debug/sql")
	}
	if startsWithExplainAnalyze(clean) {
		return "", fmt.Errorf("EXPLAIN ANALYZE is not allowed (it executes the inner statement)")
	}
	return clean, nil
}

// classifyStatement asks DuckDB's parser what kind of statement `query`
// is, *without executing it*, and returns nil if it is read-only.
//
// We allow only SELECT (covers WITH/FROM/VALUES/SHOW/DESCRIBE and read-
// only PRAGMAs which all parse as SELECT) and EXPLAIN (the EXPLAIN
// ANALYZE form is rejected by preflightQuery upstream). PRAGMAs that
// mutate state (memory_limit, profile_output, ...) parse as SET and are
// rejected; CTE-wrapped writes parse as INSERT/UPDATE/DELETE and are
// rejected.
func classifyStatement(ctx context.Context, c *sql.Conn, query string) error {
	return c.Raw(func(rc any) error {
		prep, ok := rc.(driver.ConnPrepareContext)
		if !ok {
			return fmt.Errorf("driver does not support PrepareContext")
		}
		stmt, err := prep.PrepareContext(ctx, query)
		if err != nil {
			return err
		}
		defer stmt.Close()
		ds, ok := stmt.(*duckdb.Stmt)
		if !ok {
			return fmt.Errorf("prepared statement is not a duckdb.Stmt (%T)", stmt)
		}
		st, err := ds.StatementType()
		if err != nil {
			return err
		}
		switch st {
		case duckdb.STATEMENT_TYPE_SELECT, duckdb.STATEMENT_TYPE_EXPLAIN:
			return nil
		}
		return fmt.Errorf("only read-only statements are allowed; duckdb classified this as %s", stmtTypeName(st))
	})
}

// stmtTypeName gives a short uppercase name for the DuckDB statement
// type, used in error messages.
func stmtTypeName(t duckdb.StmtType) string {
	switch t {
	case duckdb.STATEMENT_TYPE_SELECT:
		return "SELECT"
	case duckdb.STATEMENT_TYPE_INSERT:
		return "INSERT"
	case duckdb.STATEMENT_TYPE_UPDATE:
		return "UPDATE"
	case duckdb.STATEMENT_TYPE_DELETE:
		return "DELETE"
	case duckdb.STATEMENT_TYPE_EXPLAIN:
		return "EXPLAIN"
	case duckdb.STATEMENT_TYPE_CREATE:
		return "CREATE"
	case duckdb.STATEMENT_TYPE_DROP:
		return "DROP"
	case duckdb.STATEMENT_TYPE_ALTER:
		return "ALTER"
	case duckdb.STATEMENT_TYPE_COPY:
		return "COPY"
	case duckdb.STATEMENT_TYPE_ATTACH:
		return "ATTACH"
	case duckdb.STATEMENT_TYPE_DETACH:
		return "DETACH"
	case duckdb.STATEMENT_TYPE_SET, duckdb.STATEMENT_TYPE_VARIABLE_SET:
		return "SET"
	case duckdb.STATEMENT_TYPE_CALL:
		return "CALL"
	case duckdb.STATEMENT_TYPE_TRANSACTION:
		return "TRANSACTION"
	case duckdb.STATEMENT_TYPE_VACUUM:
		return "VACUUM"
	case duckdb.STATEMENT_TYPE_ANALYZE:
		return "ANALYZE"
	case duckdb.STATEMENT_TYPE_PRAGMA:
		return "PRAGMA"
	case duckdb.STATEMENT_TYPE_PREPARE:
		return "PREPARE"
	case duckdb.STATEMENT_TYPE_EXECUTE:
		return "EXECUTE"
	case duckdb.STATEMENT_TYPE_LOAD:
		return "LOAD"
	case duckdb.STATEMENT_TYPE_EXPORT:
		return "EXPORT"
	case duckdb.STATEMENT_TYPE_EXTENSION:
		return "EXTENSION"
	case duckdb.STATEMENT_TYPE_CREATE_FUNC:
		return "CREATE_FUNC"
	}
	return fmt.Sprintf("unknown(%d)", int(t))
}

// poisonAndClose marks c as bad so database/sql discards it from the
// pool, then closes the *sql.Conn. We do this after every debug query
// so that any temporary state on the connection cannot leak into other
// endpoints sharing the pool.
func poisonAndClose(c *sql.Conn) {
	if c == nil {
		return
	}
	_ = c.Raw(func(any) error { return driver.ErrBadConn })
	_ = c.Close()
}

// streamDebugSQL runs a preflighted query on a dedicated connection
// (after asking DuckDB to confirm it's read-only) and invokes onCols
// once with the column list, then onRow per row up to rowLimit. Rows
// are not buffered, so memory stays bounded regardless of result size.
// Returns truncated=true if the row limit was hit. Per-cell and
// per-row byte caps apply (see normalizeCell, debugSQLMaxRowBytes).
func (s *Server) streamDebugSQL(
	ctx context.Context,
	query string,
	rowLimit int,
	onCols func([]string) error,
	onRow func([]any) error,
) (truncated bool, err error) {
	c, err := s.db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("acquire conn: %w", err)
	}
	defer poisonAndClose(c)

	if err := classifyStatement(ctx, c, query); err != nil {
		return false, err
	}

	r, err := c.QueryContext(ctx, query)
	if err != nil {
		return false, err
	}
	defer r.Close()

	cols, err := r.Columns()
	if err != nil {
		return false, err
	}
	if err := onCols(cols); err != nil {
		return false, err
	}

	holders := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range holders {
		ptrs[i] = &holders[i]
	}
	rowCount := 0
	for r.Next() {
		if rowCount >= rowLimit {
			truncated = true
			break
		}
		if err := r.Scan(ptrs...); err != nil {
			return false, err
		}
		row := make([]any, len(cols))
		rowBytes := 0
		for i, v := range holders {
			nv := normalizeCell(v)
			if rowBytes >= debugSQLMaxRowBytes {
				nv = "<row byte budget exceeded>"
			} else {
				rowBytes += approxCellBytes(nv)
			}
			row[i] = nv
		}
		if err := onRow(row); err != nil {
			return false, err
		}
		rowCount++
	}
	if err := r.Err(); err != nil {
		return truncated, err
	}
	return truncated, nil
}

// approxCellBytes is a cheap upper-bound size estimate for the row
// byte budget. It only needs to be in the right ballpark.
func approxCellBytes(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case string:
		return len(t)
	case []byte:
		return len(t)
	default:
		return 24 // small numbers, bools, time.Time
	}
}

// normalizeCell makes a scanned value safe for JSON/CSV output: BLOBs
// become hex strings, oversized strings/blobs are truncated.
func normalizeCell(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		if len(t) > debugSQLMaxCellBytes {
			return fmt.Sprintf("0x%s... <truncated %d bytes>", hex.EncodeToString(t[:debugSQLMaxCellBytes/2]), len(t))
		}
		return "0x" + hex.EncodeToString(t)
	case string:
		if len(t) > debugSQLMaxCellBytes {
			return t[:debugSQLMaxCellBytes] + fmt.Sprintf("... <truncated %d bytes>", len(t))
		}
		return t
	default:
		return v
	}
}

func (s *Server) handleDebugSQL(w http.ResponseWriter, r *http.Request) {
	select {
	case debugSQLSem <- struct{}{}:
		defer func() { <-debugSQLSem }()
	default:
		http.Error(w, "too many concurrent debug queries; try again shortly", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()

	// Pull `q` from POST form/body or GET query string.
	var query string
	switch r.Method {
	case http.MethodGet:
		query = r.URL.Query().Get("q")
	case http.MethodPost:
		ct := r.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "application/json") {
			var body struct {
				Q string `json:"q"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			query = body.Q
		} else {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
				return
			}
			query = r.PostForm.Get("q")
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clean, err := preflightQuery(query)
	if err != nil {
		writeDebugSQLError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	rowLimit := debugSQLDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil && n > 0 {
			rowLimit = n
		}
	}
	if rowLimit > debugSQLMaxLimit {
		rowLimit = debugSQLMaxLimit
	}

	timeout := debugSQLDefaultTO
	if v := r.URL.Query().Get("timeout"); v != "" {
		if d, perr := time.ParseDuration(v); perr == nil && d > 0 {
			timeout = d
		}
	}
	if timeout > debugSQLMaxTO {
		timeout = debugSQLMaxTO
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	format := r.URL.Query().Get("format")
	if format == "csv" {
		s.streamDebugSQLCSV(ctx, w, clean, rowLimit)
		return
	}
	s.streamDebugSQLJSON(ctx, w, clean, rowLimit)
}

// streamDebugSQLJSON runs the query and streams the JSON response
// without buffering all rows. Errors that occur before any output is
// written produce a 400 with a structured JSON body; errors that occur
// mid-stream are surfaced via the trailing `error` field.
func (s *Server) streamDebugSQLJSON(ctx context.Context, w http.ResponseWriter, query string, rowLimit int) {
	start := time.Now()
	headerWritten := false
	rowsStarted := false
	firstRow := true
	encoder := json.NewEncoder(w)

	writeHeader := func(cols []string) error {
		w.Header().Set("Content-Type", "application/json")
		headerWritten = true
		colsJSON, err := json.Marshal(cols)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, `{"columns":%s,"rows":[`, colsJSON); err != nil {
			return err
		}
		rowsStarted = true
		return nil
	}

	writeRow := func(row []any) error {
		if !firstRow {
			if _, err := w.Write([]byte(",")); err != nil {
				return err
			}
		}
		firstRow = false
		return encoder.Encode(row) // Encode appends a newline; that's fine inside the array
	}

	truncated, qerr := s.streamDebugSQL(ctx, query, rowLimit, writeHeader, writeRow)
	elapsed := time.Since(start)

	if !headerWritten {
		// Failed before we wrote anything: respond with a normal
		// 400 + JSON error body.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(debugSQLResponse{Error: errString(qerr), Rows: [][]any{}})
		return
	}
	if rowsStarted {
		if _, err := w.Write([]byte("]")); err != nil {
			return
		}
	}
	tail := fmt.Sprintf(`,"elapsed_ms":%d,"truncated":%t`, elapsed.Milliseconds(), truncated)
	if qerr != nil {
		eb, _ := json.Marshal(qerr.Error())
		tail += `,"error":` + string(eb)
	}
	tail += "}\n"
	_, _ = w.Write([]byte(tail))
}

func (s *Server) streamDebugSQLCSV(ctx context.Context, w http.ResponseWriter, query string, rowLimit int) {
	headerWritten := false
	var cw *csv.Writer

	writeHeader := func(cols []string) error {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="metricsd-query.csv"`)
		headerWritten = true
		cw = csv.NewWriter(w)
		return cw.Write(cols)
	}

	strRow := []string{}
	writeRow := func(row []any) error {
		if cap(strRow) < len(row) {
			strRow = make([]string, len(row))
		} else {
			strRow = strRow[:len(row)]
		}
		for i, v := range row {
			strRow[i] = formatCellForCSV(v)
		}
		return cw.Write(strRow)
	}

	_, qerr := s.streamDebugSQL(ctx, query, rowLimit, writeHeader, writeRow)
	if cw != nil {
		cw.Flush()
	}
	if !headerWritten && qerr != nil {
		http.Error(w, qerr.Error(), http.StatusBadRequest)
	}
	// If we already wrote the CSV header, mid-stream errors are
	// silently truncated; CSV has no error field to fill in.
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeDebugSQLError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	if r.URL.Query().Get("format") == "csv" {
		http.Error(w, msg, code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(debugSQLResponse{Error: msg, Rows: [][]any{}})
}

func formatCellForCSV(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'g', -1, 32)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func (s *Server) handleDebugSQLPage(w http.ResponseWriter, r *http.Request) {
	page, err := staticFiles.ReadFile("static/debug_sql.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page)
}
