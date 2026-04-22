package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/*.html static/*
var uiFS embed.FS

func runServe(ctx context.Context, dbPath string, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("http", ":8000", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, err := openDB(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	// Spin up the background poller + auto-agent worker. They tail the context
	// so they shut down cleanly on SIGTERM.
	startAutoLoops(ctx, db)

	tmpl, err := template.New("").Funcs(template.FuncMap{
		"relTime":  relTime,
		"fmtTime":  func(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02 15:04:05") },
		"truncate": func(s string, n int) string { return truncate(s, n) },
		"money":    func(v float64) string { return fmt.Sprintf("$%.4f", v) },
	}).ParseFS(uiFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}

	srv := &server{db: db, tmpl: tmpl}
	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/result/", srv.handleResult)
	mux.HandleFunc("/conversations", srv.handleConversations)
	mux.HandleFunc("/conversation/", srv.handleConversation)
	mux.HandleFunc("/api/run", srv.handleRunStream)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	static, _ := uiFS.ReadFile("static/app.js")
	css, _ := uiFS.ReadFile("static/app.css")
	mux.HandleFunc("/static/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(static)
	})
	mux.HandleFunc("/static/app.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Write(css)
	})

	hs := &http.Server{
		Addr:              *addr,
		Handler:           authMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(shCtx)
	}()
	slog.InfoContext(ctx, "listening", "addr", *addr)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

type server struct {
	db   *sql.DB
	tmpl *template.Template
}

type resultRow struct {
	ID             int64
	CreatedAt      int64
	ConversationID string
	Prompt         string
	Output         string
	InputTokens    int
	OutputTokens   int
	CostUSD        float64
	StepsJSON      string
}

type scrapeRow struct {
	ID           int64
	StartedAt    int64
	FinishedAt   sql.NullInt64
	ConvsSeen    int
	MsgsSeen     int
	CommentsSeen int
	Error        string
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	var results []resultRow
	rows, err := s.db.QueryContext(r.Context(), `SELECT id, created_at, COALESCE(conversation_id,''), COALESCE(prompt,''), output, input_tokens, output_tokens, cost_usd FROM results ORDER BY created_at DESC LIMIT 50`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var rr resultRow
		if err := rows.Scan(&rr.ID, &rr.CreatedAt, &rr.ConversationID, &rr.Prompt, &rr.Output, &rr.InputTokens, &rr.OutputTokens, &rr.CostUSD); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		results = append(results, rr)
	}

	var scrapes []scrapeRow
	rows2, err := s.db.QueryContext(r.Context(), `SELECT id, started_at, finished_at, convs_seen, msgs_seen, comments_seen, COALESCE(error,'') FROM scrape_runs ORDER BY id DESC LIMIT 10`)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var sr scrapeRow
			if err := rows2.Scan(&sr.ID, &sr.StartedAt, &sr.FinishedAt, &sr.ConvsSeen, &sr.MsgsSeen, &sr.CommentsSeen, &sr.Error); err != nil {
				continue
			}
			scrapes = append(scrapes, sr)
		}
	}

	var totalConvs, totalMsgs, totalComments int
	_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM conversations`).Scan(&totalConvs)
	_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM messages`).Scan(&totalMsgs)
	_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM comments`).Scan(&totalComments)

	data := map[string]any{
		"Results":       results,
		"Scrapes":       scrapes,
		"TotalConvs":    totalConvs,
		"TotalMsgs":     totalMsgs,
		"TotalComments": totalComments,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		slog.WarnContext(r.Context(), "render index", "error", err)
	}
}

func (s *server) handleResult(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/result/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var rr resultRow
	err = s.db.QueryRowContext(r.Context(), `SELECT id, created_at, COALESCE(conversation_id,''), COALESCE(prompt,''), output, input_tokens, output_tokens, cost_usd, COALESCE(steps_json,'[]') FROM results WHERE id=?`, id).
		Scan(&rr.ID, &rr.CreatedAt, &rr.ConversationID, &rr.Prompt, &rr.Output, &rr.InputTokens, &rr.OutputTokens, &rr.CostUSD, &rr.StepsJSON)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var steps []agentStep
	_ = json.Unmarshal([]byte(rr.StepsJSON), &steps)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tmpl.ExecuteTemplate(w, "result.html", map[string]any{"R": rr, "Steps": steps})
}

func (s *server) handleConversations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	var rows *sql.Rows
	var err error
	if q != "" {
		rows, err = s.db.QueryContext(r.Context(), `SELECT DISTINCT c.id, COALESCE(c.subject,''), COALESCE(c.team_name,''), c.last_activity_at
FROM conversations c
JOIN messages m ON m.conversation_id = c.id
WHERE m.rowid IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)
ORDER BY c.last_activity_at DESC LIMIT 100`, q)
	} else {
		rows, err = s.db.QueryContext(r.Context(), `SELECT id, COALESCE(subject,''), COALESCE(team_name,''), last_activity_at FROM conversations ORDER BY last_activity_at DESC LIMIT 100`)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	type C struct {
		ID, Subject, Team string
		LastActivity      int64
	}
	var convs []C
	for rows.Next() {
		var c C
		if err := rows.Scan(&c.ID, &c.Subject, &c.Team, &c.LastActivity); err == nil {
			convs = append(convs, c)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tmpl.ExecuteTemplate(w, "conversations.html", map[string]any{"Convs": convs, "Q": q})
}

func (s *server) handleConversation(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/conversation/")
	var subject, team, labels, assignees string
	var created, lastActivity int64
	err := s.db.QueryRowContext(r.Context(), `SELECT COALESCE(subject,''), COALESCE(team_name,''), COALESCE(assignees_json,'[]'), COALESCE(labels_json,'[]'), COALESCE(created_at,0), COALESCE(last_activity_at,0) FROM conversations WHERE id=?`, id).
		Scan(&subject, &team, &assignees, &labels, &created, &lastActivity)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	type M struct {
		Delivered                         int64
		FromAddr, FromName, Subject, Body string
	}
	var msgs []M
	mrows, _ := s.db.QueryContext(r.Context(), `SELECT delivered_at, from_address, from_name, subject, body_text FROM messages WHERE conversation_id=? ORDER BY delivered_at ASC`, id)
	if mrows != nil {
		defer mrows.Close()
		for mrows.Next() {
			var m M
			if err := mrows.Scan(&m.Delivered, &m.FromAddr, &m.FromName, &m.Subject, &m.Body); err == nil {
				msgs = append(msgs, m)
			}
		}
	}
	type Cmt struct {
		Created      int64
		Author, Body string
	}
	var cmts []Cmt
	crows, _ := s.db.QueryContext(r.Context(), `SELECT created_at, COALESCE(author_name,author_email), body FROM comments WHERE conversation_id=? ORDER BY created_at ASC`, id)
	if crows != nil {
		defer crows.Close()
		for crows.Next() {
			var c Cmt
			if err := crows.Scan(&c.Created, &c.Author, &c.Body); err == nil {
				cmts = append(cmts, c)
			}
		}
	}
	data := map[string]any{
		"ID": id, "Subject": subject, "Team": team,
		"Labels": labels, "Assignees": assignees,
		"Created": created, "LastActivity": lastActivity,
		"Messages": msgs, "Comments": cmts,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tmpl.ExecuteTemplate(w, "conversation.html", data)
}

// handleRunStream runs the agent and streams events to the browser over SSE.
func (s *server) handleRunStream(w http.ResponseWriter, r *http.Request) {
	convID := r.URL.Query().Get("conversation")
	prompt := r.URL.Query().Get("prompt")
	if strings.TrimSpace(prompt) == "" {
		prompt = "Triage this conversation and produce a short internal comment for the on-call engineer."
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	events := make(chan agentEvent, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := runAgent(ctx, s.db, convID, prompt, events)
		if err != nil {
			events <- agentEvent{Error: err.Error(), Done: res}
		}
		close(events)
	}()

	for ev := range events {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	<-done
	fmt.Fprint(w, "event: end\ndata: {}\n\n")
	flusher.Flush()
}

func relTime(ts int64) string {
	if ts <= 0 {
		return "never"
	}
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// --- Access control ---------------------------------------------------------
//
// Authentication is handled upstream by the exe.dev HTTPS proxy, which sets
// X-ExeDev-Email (see https://exe.dev/docs/login-with-exe.md). We just
// allow-list who can reach the UI / API.
//
// To grant someone access, add their exact email or a "*@domain" wildcard.

var allowedEmails = []string{
	"*@bold.dev",
	"philip.zeyliger@gmail.com",
	// Add more entries here.
}

func emailAllowed(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	for _, pat := range allowedEmails {
		pat = strings.ToLower(pat)
		if strings.HasPrefix(pat, "*@") {
			if strings.HasSuffix(email, pat[1:]) {
				return true
			}
		} else if email == pat {
			return true
		}
	}
	return false
}

func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		email := r.Header.Get("X-ExeDev-Email")
		if email == "" {
			// Not authenticated by the exe.dev proxy; send through login.
			http.Redirect(w, r, "/__exe.dev/login?redirect="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			return
		}
		if !emailAllowed(email) {
			http.Error(w, "Access denied for "+email+". Ask an admin to add you to the allow list.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
