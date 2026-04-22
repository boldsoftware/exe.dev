package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// maxToolOutputBytes / maxToolOutputLines clip every tool response.
const (
	maxToolOutputBytes = 50 * 1024
	maxToolOutputLines = 1000
)

// clipOutput always returns a non-empty string, even on panic or error.
func clipOutput(s string) string {
	if s == "" {
		return "(empty result)"
	}
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > maxToolOutputLines {
		lines = lines[:maxToolOutputLines]
		lines = append(lines, fmt.Sprintf("\n… truncated to %d lines\n", maxToolOutputLines))
	}
	out := strings.Join(lines, "")
	if len(out) > maxToolOutputBytes {
		out = out[:maxToolOutputBytes] + fmt.Sprintf("\n… truncated to %d bytes\n", maxToolOutputBytes)
	}
	return out
}

// safeTool wraps a tool fn so panics and errors still produce a useful
// string (instead of blocking the agent loop).
func safeTool(name string, fn func() (string, error)) string {
	defer func() { _ = recover() }()
	out, err := func() (out string, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("tool %s panicked: %v", name, r)
			}
		}()
		return fn()
	}()
	if err != nil {
		return clipOutput(fmt.Sprintf("ERROR (%s): %s", name, err.Error()))
	}
	return clipOutput(out)
}

// ---------------------------------------------------------------------------
// sqlite_query — read-only SELECT against the Missive DB.
// ---------------------------------------------------------------------------

var (
	readOnlyStart = regexp.MustCompile(`(?i)^\s*(SELECT|WITH)\b`)
	forbiddenRE   = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|REPLACE|DROP|CREATE|ALTER|ATTACH|DETACH|VACUUM|PRAGMA|REINDEX)\b`)
)

func toolSQLiteQuery(ctx context.Context, db *sql.DB, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("empty query")
	}
	if !readOnlyStart.MatchString(query) {
		return "", errors.New("only SELECT / WITH queries are allowed")
	}
	if forbiddenRE.MatchString(query) {
		return "", errors.New("forbidden keyword: read-only tool")
	}
	// enforce a hard LIMIT: wrap in subquery with a big cap.
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Belt & braces: also enforce read-only at connection level.
	conn, err := db.Conn(queryCtx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(queryCtx, `PRAGMA query_only = ON`); err != nil {
		return "", err
	}
	defer func() {
		// Restore so the pooled connection is safe for subsequent writes.
		_, _ = conn.ExecContext(context.Background(), `PRAGMA query_only = OFF`)
	}()
	rows, err := conn.QueryContext(queryCtx, query)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write(cols)
	rowCount := 0
	for rows.Next() {
		if rowCount >= 500 {
			_ = w.Write([]string{"… truncated at 500 rows"})
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", err
		}
		strs := make([]string, len(cols))
		for i, v := range vals {
			strs[i] = csvValue(v)
		}
		_ = w.Write(strs)
		rowCount++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	w.Flush()
	result := fmt.Sprintf("%d rows\n%s", rowCount, buf.String())
	return result, nil
}

func csvValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	case time.Time:
		return x.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// ---------------------------------------------------------------------------
// clickhouse_query — via integration proxy.
// ---------------------------------------------------------------------------

// defaultClickhouseURL is the usual exe.dev integration hostname.
// Overridable via EXE_CLICKHOUSE_URL for VMs where the integration is
// attached under a different name.
const defaultClickhouseURL = "https://clickhouse.int.exe.xyz/"

func clickhouseURL() string {
	if v := strings.TrimSpace(os.Getenv("EXE_CLICKHOUSE_URL")); v != "" {
		return v
	}
	return defaultClickhouseURL
}

var chClient = &http.Client{Timeout: 30 * time.Second}

func toolClickHouseQuery(ctx context.Context, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("empty query")
	}
	if forbiddenRE.MatchString(query) {
		return "", errors.New("forbidden keyword: read-only tool")
	}
	// Ask for TabSeparatedWithNames if the agent didn't specify FORMAT.
	hasFormat := regexp.MustCompile(`(?i)\bFORMAT\s+\w+\s*;?\s*$`).MatchString(query)
	body := query
	if !hasFormat {
		body = strings.TrimRight(query, "; \n") + "\nFORMAT TabSeparatedWithNames"
	}
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, clickhouseURL(), strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp, err := chClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxToolOutputBytes*2)))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("clickhouse HTTP %d: %s", resp.StatusCode, truncate(string(b), 500))
	}
	return string(b), nil
}

// ---------------------------------------------------------------------------
// exe_docs — fetch https://exe.dev/docs.md or /docs/*.md, cached on disk.
// ---------------------------------------------------------------------------

var docsClient = &http.Client{Timeout: 20 * time.Second}

func toolExeDocs(ctx context.Context, db *sql.DB, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/docs.md"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if strings.Contains(path, "..") || !strings.HasSuffix(path, ".md") {
		return "", fmt.Errorf("invalid docs path %q (must end in .md, no ..)", path)
	}

	const cacheTTL = 24 * time.Hour
	var body string
	var fetched int64
	if err := db.QueryRowContext(ctx, `SELECT body, fetched_at FROM docs_cache WHERE path=?`, path).Scan(&body, &fetched); err == nil {
		if time.Since(time.Unix(fetched, 0)) < cacheTTL {
			return fmt.Sprintf("(cached %s)\n%s", time.Unix(fetched, 0).Format(time.RFC3339), body), nil
		}
	}
	url := "https://exe.dev" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := docsClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	body = string(b)
	_, _ = db.ExecContext(ctx, `INSERT INTO docs_cache(path, fetched_at, body) VALUES(?,?,?)
ON CONFLICT(path) DO UPDATE SET fetched_at=excluded.fetched_at, body=excluded.body`, path, time.Now().Unix(), body)
	return body, nil
}

// ---------------------------------------------------------------------------
// publish_result — stores the final output in the DB.
// ---------------------------------------------------------------------------

func publishResult(ctx context.Context, db *sql.DB, conversationID, prompt, output string, steps []agentStep, inTok, outTok int, cost float64) (int64, error) {
	stepsJSON, _ := json.Marshal(steps)
	res, err := db.ExecContext(ctx, `INSERT INTO results
(created_at, conversation_id, prompt, output, input_tokens, output_tokens, cost_usd, steps_json)
VALUES (?,?,?,?,?,?,?,?)`,
		time.Now().Unix(), conversationID, prompt, output, inTok, outTok, cost, string(stepsJSON))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// Best-effort: post the result as an internal comment on the Missive
	// conversation with a link back to the full result page. Failures are
	// logged but don't break publishing (the UI is still the source of truth).
	if conversationID != "" {
		go postResultComment(context.WithoutCancel(ctx), id, conversationID, output)
	}
	return id, nil
}

// publicBaseURL is the externally-reachable URL for this bot's web UI. Used
// to build links in Missive comments. Override with EXE_SUPPORT_BOT_BASE_URL.
func publicBaseURL() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("EXE_SUPPORT_BOT_BASE_URL")), "/"); v != "" {
		return v
	}
	return "https://exe-support-bot.exe.xyz"
}

func postResultComment(ctx context.Context, resultID int64, conversationID, output string) {
	cfg, ok := resolveMissive()
	if !ok {
		slog.WarnContext(ctx, "publish_result: no missive config; skipping comment", "result", resultID)
		return
	}
	link := fmt.Sprintf("%s/result/%d", publicBaseURL(), resultID)
	md := strings.TrimSpace(output) + "\n\n---\n[Full agent run →](" + link + ")"
	// Notification body is plain text; keep it short.
	notifBody := truncate(strings.TrimSpace(output), 140)
	if notifBody == "" {
		notifBody = link
	}
	client := newMissiveClient(cfg)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	postID, err := client.postComment(ctx, conversationID, md, "exe-support-bot", notifBody)
	if err != nil {
		slog.WarnContext(ctx, "publish_result: missive comment failed", "result", resultID, "conv", conversationID, "err", err)
		return
	}
	slog.InfoContext(ctx, "publish_result: posted missive comment", "result", resultID, "conv", conversationID, "post", postID)
}
