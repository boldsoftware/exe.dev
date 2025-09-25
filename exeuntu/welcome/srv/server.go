package srv

import (
	"database/sql"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	welcomedbpkg "exe.dev/exeuntu/welcome/db"
	welcomedb "exe.dev/exeuntu/welcome/sqlc"
)

type Server struct {
	DB       *sql.DB
	Hostname string
}

func New(db *sql.DB, hostname string) *Server {
	return &Server{DB: db, Hostname: hostname}
}

func (s *Server) HandleRoot(w http.ResponseWriter, r *http.Request) {
	// Identity from proxy headers (if present)
	userID := strings.TrimSpace(r.Header.Get("X-Exedev-Userid"))
	if userID == "" {
		userID = strings.TrimSpace(r.Header.Get("X-Exedev-UserID"))
	}
	userEmail := strings.TrimSpace(r.Header.Get("X-Exedev-Email"))

	// Logged-in detection: either headers present or subdomain auth cookie exists
	loggedIn := userID != "" || userEmail != ""
	if !loggedIn {
		if c, err := r.Cookie("exe-proxy-auth"); err == nil && c.Value != "" {
			loggedIn = true
		}
	}

	// Key for counting
	counterKey := ""
	if userID != "" {
		counterKey = "user:" + userID
	} else {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if host == "" {
			host = r.RemoteAddr
		}
		counterKey = "anon:" + host
	}

	// Update db counter (sqlc)
	q := welcomedb.New(s.DB)
	now := time.Now()
	_ = q.UpsertVisitor(r.Context(), welcomedb.UpsertVisitorParams{
		ID:        counterKey,
		Email:     sqlNull(userEmail),
		CreatedAt: now,
		LastSeen:  now,
	})
	v, err := q.GetVisitor(r.Context(), counterKey)
	var count int64
	if err == nil {
		count = v.ViewCount
	}

	// Compute login/logout links
	loginURL := loginURLForRequest(r)
	logoutURL := "/__exe.dev/logout"

	// Render
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><html><head><meta charset=\"utf-8\"><title>exe.dev – Welcome</title>")
	fmt.Fprintf(w, "<style>body{font-family: system-ui,-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;max-width:740px;margin:40px auto;padding:0 16px;color:#222}.muted{color:#666}.btn{display:inline-block;padding:8px 12px;border:1px solid #ccc;border-radius:6px;text-decoration:none;color:#222}.btn:hover{background:#f6f6f6}code{background:#f3f3f3;padding:2px 4px;border-radius:4px}</style>")
	fmt.Fprintf(w, "</head><body>")
	fmt.Fprintf(w, "<h1>Welcome to exe.dev</h1>")
	fmt.Fprintf(w, "<p class=\"muted\">Hostname: %s &nbsp;•&nbsp; Time: %s &nbsp;•&nbsp; Path: %s</p>", html.EscapeString(s.Hostname), time.Now().Format(time.RFC3339), html.EscapeString(r.URL.Path))

	if loggedIn {
		who := ""
		if userEmail != "" && userID != "" {
			who = fmt.Sprintf("%s (%s)", html.EscapeString(userEmail), html.EscapeString(userID))
		} else if userEmail != "" {
			who = html.EscapeString(userEmail)
		} else if userID != "" {
			who = html.EscapeString(userID)
		} else {
			who = "(authenticated)"
		}
		fmt.Fprintf(w, "<p>You’re logged in as <strong>%s</strong>. <a class=\"btn\" href=\"%s\">Logout</a></p>", who, html.EscapeString(logoutURL))
	} else {
		fmt.Fprintf(w, "<p>You’re not logged in. <a class=\"btn\" href=\"%s\">Login</a></p>", html.EscapeString(loginURL))
	}

	if count > 0 {
		fmt.Fprintf(w, "<p>You’ve viewed this page <strong>%d</strong> times.</p>", count)
	}

	fmt.Fprintf(w, "<h3>Auth URLs</h3>")
	fmt.Fprintf(w, "<ul><li>Login: <code>%s</code></li><li>Logout: <code>%s</code></li></ul>", html.EscapeString(loginURL), html.EscapeString(logoutURL))
	fmt.Fprintf(w, "</body></html>")
}

func (s *Server) HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "OK\n")
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

func mainDomainFromHost(h string) string {
	if i := strings.LastIndex(h, ":"); i > 0 {
		basePort := h[i:]
		host := h[:i]
		if strings.HasSuffix(host, ".localhost") {
			return "localhost" + basePort
		}
		if strings.HasSuffix(host, ".exe.dev") {
			return "exe.dev"
		}
		return host
	}
	if strings.HasSuffix(h, ".localhost") {
		return "localhost"
	}
	if strings.HasSuffix(h, ".exe.dev") {
		return "exe.dev"
	}
	return h
}

// SetupDatabase initializes the database connection and runs migrations
func (s *Server) SetupDatabase(dbPath string) error {
	if dbPath == "" {
		dbPath = "./welcome.sqlite3"
	}

	db, err := welcomedbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open db: %w", err)
	}
	s.DB = db

	if err := welcomedbpkg.RunMigrations(db); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}

// Serve starts the HTTP server with the configured routes
func (s *Server) Serve(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.HandleRoot)
	mux.HandleFunc("/health", s.HandleHealth)

	log.Printf("Starting welcome server on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func sqlNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
