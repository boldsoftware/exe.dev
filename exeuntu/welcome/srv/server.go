package srv

import (
	"database/sql"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"welcome.exe.dev/db"
	"welcome.exe.dev/db/welcomedb"
)

type Server struct {
	DB           *sql.DB
	Hostname     string
	TemplatesDir string
}

type welcomePageData struct {
	Hostname   string
	Now        string
	UserEmail  string
	VisitCount int64
	AuthLinks  []authLink
}

type authLink struct {
	Label string
	URL   string
}

func New(dbPath, hostname string) (*Server, error) {
	_, thisFile, _, _ := runtime.Caller(0)
	templatesDir := filepath.Join(filepath.Dir(thisFile), "templates")
	srv := &Server{
		Hostname:     hostname,
		TemplatesDir: templatesDir,
	}
	if err := srv.setUpDatabase(dbPath); err != nil {
		return nil, err
	}
	return srv, nil
}

func (s *Server) HandleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/favicon.ico" {
		http.NotFound(w, r)
		return
	}

	// Identity from proxy headers (if present)
	// UserID is stable; email is useful.
	userID := strings.TrimSpace(r.Header.Get("X-ExeDev-UserID"))
	userEmail := strings.TrimSpace(r.Header.Get("X-ExeDev-Email"))
	now := time.Now()

	var count int64
	if userID != "" && s.DB != nil {
		q := welcomedb.New(s.DB)
		shouldRecordView := r.Method == http.MethodGet
		if shouldRecordView {
			// Best effort
			err := q.UpsertVisitor(r.Context(), welcomedb.UpsertVisitorParams{
				ID:        userID,
				CreatedAt: now,
				LastSeen:  now,
			})
			if err != nil {
				slog.Warn("upsert visitor", "error", err, "user_id", userID)
			}
		}
		if v, err := q.VisitorWithID(r.Context(), userID); err == nil {
			count = v.ViewCount
		}
	}

	data := welcomePageData{
		Hostname:   s.Hostname,
		Now:        now.Format(time.RFC3339),
		UserEmail:  userEmail,
		VisitCount: count,
		AuthLinks: []authLink{
			{Label: "Login", URL: loginURLForRequest(r)},
			{Label: "Logout", URL: "/__exe.dev/logout"},
		},
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.renderTemplate(w, "welcome.html", data); err != nil {
		slog.Warn("render template", "url", r.URL.Path, "error", err)
	}
}

func loginURLForRequest(r *http.Request) string {
	path := r.URL.RequestURI()
	main := mainDomainFromHost(r.Host)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	v := url.Values{}
	v.Set("redirect", path)
	v.Set("return_host", r.Host)
	return fmt.Sprintf("%s://%s/auth?%s", scheme, main, v.Encode())
}

func (s *Server) renderTemplate(w http.ResponseWriter, name string, data any) error {
	path := filepath.Join(s.TemplatesDir, name)
	tmpl, err := template.ParseFiles(path)
	if err != nil {
		return fmt.Errorf("parse template %q: %w", name, err)
	}
	if err := tmpl.Execute(w, data); err != nil {
		return fmt.Errorf("execute template %q: %w", name, err)
	}
	return nil
}

func mainDomainFromHost(h string) string {
	host, port, err := net.SplitHostPort(h)
	if err != nil {
		host = strings.TrimSpace(h)
	}
	if port != "" {
		port = ":" + port
	}
	if strings.HasSuffix(host, ".localhost") {
		return "localhost" + port
	}
	if strings.HasSuffix(host, ".exe.dev") {
		return "exe.dev"
	}
	return host
}

// SetupDatabase initializes the database connection and runs migrations
func (s *Server) setUpDatabase(dbPath string) error {
	wdb, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open db: %w", err)
	}
	s.DB = wdb
	if err := db.RunMigrations(wdb); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}
	return nil
}

// Serve starts the HTTP server with the configured routes
func (s *Server) Serve(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.HandleRoot)
	slog.Info("starting welcome server", "addr", addr)
	return http.ListenAndServe(addr, mux)
}
