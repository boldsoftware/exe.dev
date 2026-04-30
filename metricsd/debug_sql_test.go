package metricsd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"exe.dev/metricsd/types"
)

// TestPreflightQuery covers the cheap textual preflight: stripping
// comments, rejecting `;` outside strings, dollar-quoted strings, and
// EXPLAIN ANALYZE. It does *not* cover the read-only allowlist; that's
// enforced by DuckDB's parser via classifyStatement and is exercised by
// TestDebugSQLEndpoint below.
func TestPreflightQuery(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"select", "SELECT 1", false},
		{"select-trailing-semi", "SELECT 1;", false},
		{"with", "WITH t AS (SELECT 1) SELECT * FROM t", false},
		{"explain", "EXPLAIN SELECT 1", false},
		{"leading-comment", "-- a\nSELECT 1", false},
		{"block-comment", "/* a */ SELECT 1", false},
		// stripSQLComments must not glue tokens together.
		{"block-comment-no-space", "SELECT/*x*/1", false},
		{"empty", "   ", true},
		{"only-comment", "-- foo", true},
		{"two-stmts", "SELECT 1; SELECT 2", true},
		{"two-stmts-with-string", "SELECT ';'; SELECT 2", true},
		{"semi-in-string-ok", "SELECT ';'", false},
		{"semi-in-comment-ok", "SELECT 1 -- ;", false},
		{"explain-analyze", "EXPLAIN ANALYZE SELECT 1", true},
		{"explain-analyze-delete", "EXPLAIN ANALYZE DELETE FROM vm_metrics", true},
		{"explain-analyze-lower", "explain analyze select 1", true},
		{"dollar-quote", "SELECT $$ '$$", true},
		{"dollar-quote-tagged", "SELECT $tag$ ; $tag$", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := preflightQuery(tc.in)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %q", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.in, err)
			}
		})
	}
}

func TestDebugSQLEndpoint(t *testing.T) {
	ctx := context.Background()
	connector, db, _, err := OpenDB(ctx, "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()

	// Insert a couple rows so SELECT * has something to look at.
	now := time.Now().UTC().Truncate(time.Second)
	if err := srv.InsertMetrics(ctx, []types.Metric{
		{Timestamp: now, VMName: "test-a", Host: "h1"},
		{Timestamp: now.Add(time.Second), VMName: "test-b", Host: "h1"},
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post := func(query, params string) (*http.Response, []byte) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"q": query})
		url := ts.URL + "/debug/sql/run"
		if params != "" {
			url += "?" + params
		}
		resp, err := http.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		buf := &bytes.Buffer{}
		_, _ = buf.ReadFrom(resp.Body)
		resp.Body.Close()
		return resp, buf.Bytes()
	}

	t.Run("select", func(t *testing.T) {
		resp, body := post("SELECT vm_name FROM vm_metrics ORDER BY vm_name", "")
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, body)
		}
		var r debugSQLResponse
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatalf("decode: %v: %s", err, body)
		}
		if len(r.Columns) != 1 || r.Columns[0] != "vm_name" {
			t.Fatalf("columns: %v", r.Columns)
		}
		if len(r.Rows) != 2 {
			t.Fatalf("want 2 rows, got %d (%v)", len(r.Rows), r.Rows)
		}
	})

	// Each of these queries DuckDB classifies as SELECT. They exercise
	// that we don't need a hand-written keyword allowlist.
	for _, q := range []string{
		"DESCRIBE vm_metrics",
		"SHOW TABLES",
		"PRAGMA show_tables",
		"VALUES (1),(2)",
		"FROM vm_metrics LIMIT 1",
		"WITH t AS (SELECT 1) SELECT * FROM t",
		"WITH RECURSIVE t(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM t WHERE x<3) SELECT * FROM t",
		"EXPLAIN SELECT 1",
		"EXPLAIN DELETE FROM vm_metrics", // EXPLAIN doesn't execute
	} {
		t.Run("accepts/"+q, func(t *testing.T) {
			resp, body := post(q, "")
			if resp.StatusCode != 200 {
				t.Fatalf("status %d: %s", resp.StatusCode, body)
			}
		})
	}

	// And these queries DuckDB classifies as something other than
	// SELECT/EXPLAIN, so they must be rejected before execution.
	for _, q := range []string{
		"DELETE FROM vm_metrics",
		"INSERT INTO vm_metrics SELECT * FROM vm_metrics LIMIT 0",
		"UPDATE vm_metrics SET vm_name='x'",
		"DROP TABLE vm_metrics",
		"CREATE TABLE foo (x INT)",
		"ATTACH 'foo.db'",
		"COPY vm_metrics TO '/tmp/x.parquet'",
		"PRAGMA memory_limit='1B'",
		"PRAGMA enable_profiling='json'",
		"SET memory_limit='1B'",
		// CTE-wrapped writes parse as their inner statement's type.
		"WITH t AS (SELECT 1) DELETE FROM vm_metrics",
		"WITH t AS (SELECT 1) INSERT INTO vm_metrics SELECT * FROM vm_metrics LIMIT 0",
		"WITH t AS (SELECT 1) UPDATE vm_metrics SET vm_name='x'",
		"VACUUM",
	} {
		t.Run("rejects/"+q, func(t *testing.T) {
			resp, body := post(q, "")
			if resp.StatusCode != 400 {
				t.Fatalf("expected 400, got %d: %s", resp.StatusCode, body)
			}
		})
	}

	t.Run("rejects-multi-statement-without-executing", func(t *testing.T) {
		// duckdb-go's PrepareContext executes all but the last
		// statement when given multi-statement input. Our `;`
		// rejection happens *before* PrepareContext, so this must be
		// rejected before any execution. We verify that no rows were
		// deleted from vm_metrics.
		resp, body := post("DELETE FROM vm_metrics; SELECT 1", "")
		if resp.StatusCode != 400 {
			t.Fatalf("expected 400, got %d: %s", resp.StatusCode, body)
		}
		// Verify the table is intact.
		resp, body = post("SELECT count(*) FROM vm_metrics", "")
		if resp.StatusCode != 200 {
			t.Fatalf("count status %d: %s", resp.StatusCode, body)
		}
		// Streaming JSON puts a newline between row entries; just
		// look for the value.
		if !bytes.Contains(body, []byte(`[2]`)) {
			t.Fatalf("expected count=2, got %s", body)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		resp, body := post("SELECT * FROM range(10)", "limit=3")
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, body)
		}
		var r debugSQLResponse
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatalf("decode: %v: %s", err, body)
		}
		if !r.Truncated || len(r.Rows) != 3 {
			t.Fatalf("expected truncated=true rows=3, got %+v", r)
		}
	})

	t.Run("csv", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/debug/sql/run?format=csv&q=" +
			"SELECT%20vm_name%20FROM%20vm_metrics%20ORDER%20BY%20vm_name")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		buf := &bytes.Buffer{}
		_, _ = buf.ReadFrom(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, buf.String())
		}
		if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
			t.Fatalf("content-type %q", got)
		}
		want := "vm_name\ntest-a\ntest-b\n"
		if buf.String() != want {
			t.Fatalf("csv body: %q want %q", buf.String(), want)
		}
	})

	t.Run("connection-poisoning-discards-conn", func(t *testing.T) {
		// Create a temp table on one conn (DuckDB temp tables are
		// per-connection), poison+close it, then assert a fresh conn
		// from the pool can't see the table -- i.e. the original conn
		// was actually discarded rather than reused.
		c, err := srv.db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn: %v", err)
		}
		if _, err := c.ExecContext(ctx, "CREATE TEMP TABLE leak_canary (x INT)"); err != nil {
			t.Fatalf("CREATE TEMP: %v", err)
		}
		poisonAndClose(c)

		c2, err := srv.db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn(2): %v", err)
		}
		defer c2.Close()
		var n int
		err = c2.QueryRowContext(ctx, "SELECT count(*) FROM leak_canary").Scan(&n)
		if err == nil {
			t.Fatalf("temp table from poisoned conn was visible on a fresh conn (count=%d); pool reused the conn", n)
		}
	})

	t.Run("blob-rendered-as-hex", func(t *testing.T) {
		resp, body := post("SELECT 'AB'::BLOB AS b", "")
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, body)
		}
		var r debugSQLResponse
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatalf("decode: %v: %s", err, body)
		}
		if len(r.Rows) != 1 || len(r.Rows[0]) != 1 {
			t.Fatalf("unexpected rows: %+v", r.Rows)
		}
		cell, ok := r.Rows[0][0].(string)
		if !ok || cell != "0x4142" {
			t.Fatalf("expected hex 0x4142, got %T %v", r.Rows[0][0], r.Rows[0][0])
		}
	})

	t.Run("large-cell-truncated", func(t *testing.T) {
		// repeat() can produce a string larger than the per-cell
		// budget; we expect normalization to truncate it rather
		// than the daemon OOMing.
		resp, body := post("SELECT repeat('A', 200000) AS big", "")
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, body)
		}
		if !bytes.Contains(body, []byte("truncated")) {
			t.Fatalf("expected truncated marker, got %s", body)
		}
	})

	t.Run("plain-text-error-from-overload", func(t *testing.T) {
		// Method-not-allowed is a plain-text error path; verify
		// the JS-side handling will see a non-JSON content type.
		resp, err := http.Get(ts.URL + "/debug/sql/run")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		// GET is allowed but with no q is a 400 JSON; that's fine.
		// We just want to confirm a non-JSON path exists; PUT does it.
		req, _ := http.NewRequest("PUT", ts.URL+"/debug/sql/run", nil)
		resp2, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != 405 {
			t.Fatalf("expected 405, got %d", resp2.StatusCode)
		}
		if ct := resp2.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
			t.Fatalf("expected non-JSON content-type for plain-text error, got %q", ct)
		}
	})

	t.Run("page-renders", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/debug/sql")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status %d", resp.StatusCode)
		}
		buf := &bytes.Buffer{}
		_, _ = buf.ReadFrom(resp.Body)
		if !strings.Contains(buf.String(), "debug_sql.js") {
			t.Fatalf("page missing js: %s", buf.String())
		}
	})
}
