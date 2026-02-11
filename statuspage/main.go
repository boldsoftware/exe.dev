package main

import (
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/xml"
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/style.css
var cssBytes []byte

var (
	db        *sql.DB
	cssString string
	templates *template.Template
)

func main() {
	httpAddr := flag.String("http", ":8080", "HTTP listen address")
	dbPath := flag.String("db", "/data/status.db", "SQLite database path")
	flag.Parse()

	cssString = string(cssBytes)
	templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

	var err error
	db, err = sql.Open("sqlite", *dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		panic(fmt.Sprintf("open database: %v", err))
	}
	defer db.Close()

	if err := migrate(); err != nil {
		panic(fmt.Sprintf("migrate database: %v", err))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/feed.xml", handleFeed)
	mux.HandleFunc("/private", handlePrivate)
	mux.HandleFunc("/private/login", handleLogin)
	mux.HandleFunc("/private/logout", handleLogout)
	mux.HandleFunc("/private/incident/create", requireAuth(handleCreateIncident))
	mux.HandleFunc("/private/incident/resolve", requireAuth(handleResolveIncident))
	mux.HandleFunc("/private/incident/edit", requireAuth(handleEditIncident))
	mux.HandleFunc("/private/incident/delete", requireAuth(handleDeleteIncident))
	mux.HandleFunc("/private/user/create", requireAuth(handleCreateUser))
	mux.HandleFunc("/private/user/delete", requireAuth(handleDeleteUser))
	mux.HandleFunc("/private/password", requireAuth(handleChangePassword))
	mux.HandleFunc("/private/invite/create", requireAuth(handleCreateInvite))
	mux.HandleFunc("/private/invite/revoke", requireAuth(handleRevokeInvite))
	mux.HandleFunc("/invite/", handleInvite)
	mux.HandleFunc("/health", handleHealth)

	slog.Info("starting statuspage", "addr", *httpAddr)
	srv := &http.Server{
		Addr:              *httpAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		panic(fmt.Sprintf("server: %v", err))
	}
}

func migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS incidents (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL DEFAULT (datetime('now')),
			resolved_at TEXT,
			created_by TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL REFERENCES users(id),
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS invites (
			token TEXT PRIMARY KEY,
			created_by TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			used_by TEXT
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("exec %q: %w", s, err)
		}
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if count == 0 {
		hash, err := bcrypt.GenerateFromPassword([]byte("87a2xsf3"), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		if _, err := db.Exec("INSERT INTO users (username, password_hash) VALUES (?, ?)", "philip", string(hash)); err != nil {
			return fmt.Errorf("insert seed user: %w", err)
		}
		slog.Info("seeded initial user", "username", "philip")
	}
	return nil
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok"}`+"\n")
}

// -- Data types --

type incident struct {
	ID          int
	Title       string
	Description string
	StartedAt   string
	ResolvedAt  string
	CreatedBy   string
}

type dayStatus struct {
	Date     string
	HasEvent bool
}

// -- Public page --

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	active, err := queryIncidents("SELECT id, title, description, started_at, '' as resolved_at, created_by FROM incidents WHERE resolved_at IS NULL ORDER BY started_at DESC")
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}

	past, err := queryIncidents("SELECT id, title, description, started_at, COALESCE(resolved_at, '') as resolved_at, created_by FROM incidents WHERE resolved_at IS NOT NULL ORDER BY started_at DESC LIMIT 30")
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}

	days, err := buildDayBars()
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}

	_, _, loggedIn := getSessionUser(r)

	data := struct {
		CSS      template.CSS
		Active   []incident
		Past     []incident
		Days     []dayStatus
		AllClear bool
		LoggedIn bool
	}{
		CSS:      template.CSS(cssString),
		Active:   active,
		Past:     past,
		Days:     days,
		AllClear: len(active) == 0,
		LoggedIn: loggedIn,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, "index.html", data); err != nil {
		slog.Error("render index", "error", err)
	}
}

// -- RSS Feed --

type rssChannel struct {
	XMLName     xml.Name  `xml:"channel"`
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Items       []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	GUID        string `xml:"guid"`
}

func handleFeed(w http.ResponseWriter, r *http.Request) {
	incidents, err := queryIncidents(`
		SELECT id, title, description, started_at, COALESCE(resolved_at, '') as resolved_at, created_by
		FROM incidents ORDER BY started_at DESC LIMIT 50
	`)
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}

	var items []rssItem
	for _, inc := range incidents {
		desc := inc.Description
		if inc.ResolvedAt != "" {
			if desc != "" {
				desc += "\n\n"
			}
			desc += "Resolved: " + inc.ResolvedAt + " UTC"
		} else {
			if desc != "" {
				desc += "\n\n"
			}
			desc += "Status: ongoing"
		}
		pubDate := parseTime(inc.StartedAt).Format(time.RFC1123Z)
		items = append(items, rssItem{
			Title:       inc.Title,
			Description: desc,
			PubDate:     pubDate,
			GUID:        fmt.Sprintf("incident-%d", inc.ID),
		})
	}

	channel := rssChannel{
		Title:       "exe.dev status",
		Link:        "https://status.exe.dev/",
		Description: "exe.dev service status and incidents",
		Items:       items,
	}

	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	fmt.Fprint(w, xml.Header)
	fmt.Fprint(w, `<rss version="2.0">`+"\n")
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(channel)
	fmt.Fprint(w, "\n</rss>\n")
}

// -- Auth helpers --

func createSession(userID int) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	expires := time.Now().UTC().Add(7 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	_, err := db.Exec("INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)", token, userID, expires)
	return token, err
}

func getSessionUser(r *http.Request) (int, string, bool) {
	c, err := r.Cookie("session")
	if err != nil {
		return 0, "", false
	}
	var userID int
	var username string
	err = db.QueryRow(`
		SELECT u.id, u.username FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token = ? AND s.expires_at > datetime('now')
	`, c.Value).Scan(&userID, &username)
	if err != nil {
		return 0, "", false
	}
	return userID, username, true
}

func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := getSessionUser(r); !ok {
			http.Redirect(w, r, "/private", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// -- Private pages --

func handlePrivate(w http.ResponseWriter, r *http.Request) {
	userID, username, loggedIn := getSessionUser(r)
	if !loggedIn {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		templates.ExecuteTemplate(w, "login.html", struct {
			CSS   template.CSS
			Error string
		}{CSS: template.CSS(cssString)})
		return
	}

	active, err := queryIncidents("SELECT id, title, description, started_at, '' as resolved_at, created_by FROM incidents WHERE resolved_at IS NULL ORDER BY started_at DESC")
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}

	past, err := queryIncidents("SELECT id, title, description, started_at, COALESCE(resolved_at, '') as resolved_at, created_by FROM incidents WHERE resolved_at IS NOT NULL ORDER BY started_at DESC LIMIT 50")
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}

	type user struct {
		ID       int
		Username string
	}
	urows, err := db.Query("SELECT id, username FROM users ORDER BY username")
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer urows.Close()
	var users []user
	for urows.Next() {
		var u user
		if err := urows.Scan(&u.ID, &u.Username); err != nil {
			http.Error(w, "database error", 500)
			return
		}
		users = append(users, u)
	}

	type invite struct {
		Token     string
		CreatedBy string
		CreatedAt string
		UsedBy    string
	}
	irows, err := db.Query("SELECT token, created_by, created_at, COALESCE(used_by, '') FROM invites ORDER BY created_at DESC")
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer irows.Close()
	var invites []invite
	for irows.Next() {
		var inv invite
		if err := irows.Scan(&inv.Token, &inv.CreatedBy, &inv.CreatedAt, &inv.UsedBy); err != nil {
			http.Error(w, "database error", 500)
			return
		}
		invites = append(invites, inv)
	}

	data := struct {
		CSS      template.CSS
		UserID   int
		Username string
		Active   []incident
		Past     []incident
		Users    []user
		Invites  []invite
		Msg      string
	}{
		CSS:      template.CSS(cssString),
		UserID:   userID,
		Username: username,
		Active:   active,
		Past:     past,
		Users:    users,
		Invites:  invites,
		Msg:      r.URL.Query().Get("msg"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, "private.html", data); err != nil {
		slog.Error("render private", "error", err)
	}
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/private", http.StatusSeeOther)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	var userID int
	var hash string
	err := db.QueryRow("SELECT id, password_hash FROM users WHERE username = ?", username).Scan(&userID, &hash)
	if err != nil {
		renderLogin(w, "Invalid username or password.")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		renderLogin(w, "Invalid username or password.")
		return
	}

	token, err := createSession(userID)
	if err != nil {
		http.Error(w, "session error", 500)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 60 * 60,
	})
	http.Redirect(w, r, "/private", http.StatusSeeOther)
}

func renderLogin(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	templates.ExecuteTemplate(w, "login.html", struct {
		CSS   template.CSS
		Error string
	}{CSS: template.CSS(cssString), Error: errMsg})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("session"); err == nil {
		db.Exec("DELETE FROM sessions WHERE token = ?", c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:   "session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/private", http.StatusSeeOther)
}

func handleCreateIncident(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/private", http.StatusSeeOther)
		return
	}
	_, username, _ := getSessionUser(r)
	title := strings.TrimSpace(r.FormValue("title"))
	description := strings.TrimSpace(r.FormValue("description"))
	if title == "" {
		http.Redirect(w, r, "/private?msg=Title+is+required", http.StatusSeeOther)
		return
	}
	_, err := db.Exec("INSERT INTO incidents (title, description, created_by) VALUES (?, ?, ?)", title, description, username)
	if err != nil {
		http.Redirect(w, r, "/private?msg=Error+creating+incident", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/private?msg=Incident+declared", http.StatusSeeOther)
}

func handleResolveIncident(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/private", http.StatusSeeOther)
		return
	}
	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Redirect(w, r, "/private?msg=Invalid+incident+ID", http.StatusSeeOther)
		return
	}
	_, err = db.Exec("UPDATE incidents SET resolved_at = datetime('now') WHERE id = ? AND resolved_at IS NULL", id)
	if err != nil {
		http.Redirect(w, r, "/private?msg=Error+resolving+incident", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/private?msg=Incident+resolved", http.StatusSeeOther)
}

func handleEditIncident(w http.ResponseWriter, r *http.Request) {
	_, username, _ := getSessionUser(r)
	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Redirect(w, r, "/private?msg=Invalid+incident+ID", http.StatusSeeOther)
		return
	}

	if r.Method == http.MethodPost {
		title := strings.TrimSpace(r.FormValue("title"))
		description := strings.TrimSpace(r.FormValue("description"))
		startedAt := strings.TrimSpace(r.FormValue("started_at"))
		resolvedAt := strings.TrimSpace(r.FormValue("resolved_at"))
		if title == "" {
			http.Redirect(w, r, fmt.Sprintf("/private/incident/edit?id=%d&msg=Title+is+required", id), http.StatusSeeOther)
			return
		}
		var resolvedVal any
		if resolvedAt == "" {
			resolvedVal = nil
		} else {
			resolvedVal = resolvedAt
		}
		_, err = db.Exec("UPDATE incidents SET title = ?, description = ?, started_at = ?, resolved_at = ? WHERE id = ?",
			title, description, startedAt, resolvedVal, id)
		if err != nil {
			http.Redirect(w, r, fmt.Sprintf("/private/incident/edit?id=%d&msg=Error+saving", id), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/private?msg=Incident+updated", http.StatusSeeOther)
		return
	}

	var inc incident
	var resolvedAt sql.NullString
	err = db.QueryRow("SELECT id, title, description, started_at, resolved_at, created_by FROM incidents WHERE id = ?", id).
		Scan(&inc.ID, &inc.Title, &inc.Description, &inc.StartedAt, &resolvedAt, &inc.CreatedBy)
	if err != nil {
		http.Redirect(w, r, "/private?msg=Incident+not+found", http.StatusSeeOther)
		return
	}
	if resolvedAt.Valid {
		inc.ResolvedAt = resolvedAt.String
	}

	data := struct {
		CSS      template.CSS
		Username string
		Incident incident
		Msg      string
	}{
		CSS:      template.CSS(cssString),
		Username: username,
		Incident: inc,
		Msg:      r.URL.Query().Get("msg"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, "edit-incident.html", data); err != nil {
		slog.Error("render edit-incident", "error", err)
	}
}

func handleDeleteIncident(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/private", http.StatusSeeOther)
		return
	}
	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Redirect(w, r, "/private?msg=Invalid+incident+ID", http.StatusSeeOther)
		return
	}
	_, err = db.Exec("DELETE FROM incidents WHERE id = ?", id)
	if err != nil {
		http.Redirect(w, r, "/private?msg=Error+deleting+incident", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/private?msg=Incident+deleted", http.StatusSeeOther)
}

func handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/private", http.StatusSeeOther)
		return
	}
	username := strings.TrimSpace(r.FormValue("new_username"))
	password := r.FormValue("new_password")
	if username == "" || password == "" {
		http.Redirect(w, r, "/private?msg=Username+and+password+required", http.StatusSeeOther)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Redirect(w, r, "/private?msg=Error+hashing+password", http.StatusSeeOther)
		return
	}
	_, err = db.Exec("INSERT INTO users (username, password_hash) VALUES (?, ?)", username, string(hash))
	if err != nil {
		http.Redirect(w, r, "/private?msg=Username+already+exists", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/private?msg=User+created", http.StatusSeeOther)
}

func handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/private", http.StatusSeeOther)
		return
	}
	userID, _, _ := getSessionUser(r)
	deleteID, err := strconv.Atoi(r.FormValue("user_id"))
	if err != nil {
		http.Redirect(w, r, "/private?msg=Invalid+user+ID", http.StatusSeeOther)
		return
	}
	if deleteID == userID {
		http.Redirect(w, r, "/private?msg=Cannot+delete+yourself", http.StatusSeeOther)
		return
	}
	db.Exec("DELETE FROM sessions WHERE user_id = ?", deleteID)
	db.Exec("DELETE FROM users WHERE id = ?", deleteID)
	http.Redirect(w, r, "/private?msg=User+deleted", http.StatusSeeOther)
}

func handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/private", http.StatusSeeOther)
		return
	}
	userID, _, _ := getSessionUser(r)
	oldPassword := r.FormValue("old_password")
	newPassword := r.FormValue("new_password_self")
	if oldPassword == "" || newPassword == "" {
		http.Redirect(w, r, "/private?msg=Both+passwords+required", http.StatusSeeOther)
		return
	}

	var currentHash string
	if err := db.QueryRow("SELECT password_hash FROM users WHERE id = ?", userID).Scan(&currentHash); err != nil {
		http.Redirect(w, r, "/private?msg=Error+reading+user", http.StatusSeeOther)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(oldPassword)); err != nil {
		http.Redirect(w, r, "/private?msg=Current+password+is+incorrect", http.StatusSeeOther)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Redirect(w, r, "/private?msg=Error+hashing+password", http.StatusSeeOther)
		return
	}
	db.Exec("UPDATE users SET password_hash = ? WHERE id = ?", string(hash), userID)
	if c, cErr := r.Cookie("session"); cErr == nil {
		db.Exec("DELETE FROM sessions WHERE user_id = ? AND token != ?", userID, c.Value)
	}
	http.Redirect(w, r, "/private?msg=Password+changed", http.StatusSeeOther)
}

// -- Invites --

func handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/private", http.StatusSeeOther)
		return
	}
	_, username, _ := getSessionUser(r)
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		http.Redirect(w, r, "/private?msg=Error+generating+invite", http.StatusSeeOther)
		return
	}
	token := hex.EncodeToString(b)
	_, err := db.Exec("INSERT INTO invites (token, created_by) VALUES (?, ?)", token, username)
	if err != nil {
		http.Redirect(w, r, "/private?msg=Error+creating+invite", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/private?msg=Invite+created", http.StatusSeeOther)
}

func handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/private", http.StatusSeeOther)
		return
	}
	token := r.FormValue("token")
	db.Exec("DELETE FROM invites WHERE token = ? AND used_by IS NULL", token)
	http.Redirect(w, r, "/private?msg=Invite+revoked", http.StatusSeeOther)
}

func handleInvite(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/invite/")
	if token == "" {
		http.NotFound(w, r)
		return
	}

	// Check invite is valid (exists and unused).
	var createdBy string
	err := db.QueryRow("SELECT created_by FROM invites WHERE token = ? AND used_by IS NULL", token).Scan(&createdBy)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		templates.ExecuteTemplate(w, "invite.html", struct {
			CSS   template.CSS
			Error string
			Token string
		}{CSS: template.CSS(cssString), Error: "This invite link is invalid or has already been used.", Token: ""})
		return
	}

	if r.Method == http.MethodPost {
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		if username == "" || password == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			templates.ExecuteTemplate(w, "invite.html", struct {
				CSS   template.CSS
				Error string
				Token string
			}{CSS: template.CSS(cssString), Error: "Username and password are required.", Token: token})
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "error", 500)
			return
		}
		_, err = db.Exec("INSERT INTO users (username, password_hash) VALUES (?, ?)", username, string(hash))
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			templates.ExecuteTemplate(w, "invite.html", struct {
				CSS   template.CSS
				Error string
				Token string
			}{CSS: template.CSS(cssString), Error: "That username is already taken.", Token: token})
			return
		}
		db.Exec("UPDATE invites SET used_by = ? WHERE token = ?", username, token)

		// Log them in.
		var userID int
		db.QueryRow("SELECT id FROM users WHERE username = ?", username).Scan(&userID)
		sessionToken, err := createSession(userID)
		if err != nil {
			http.Redirect(w, r, "/private", http.StatusSeeOther)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    sessionToken,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   7 * 24 * 60 * 60,
		})
		http.Redirect(w, r, "/private", http.StatusSeeOther)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	templates.ExecuteTemplate(w, "invite.html", struct {
		CSS   template.CSS
		Error string
		Token string
	}{CSS: template.CSS(cssString), Token: token})
}

// -- Helpers --

func queryIncidents(query string, args ...any) ([]incident, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []incident
	for rows.Next() {
		var inc incident
		if err := rows.Scan(&inc.ID, &inc.Title, &inc.Description, &inc.StartedAt, &inc.ResolvedAt, &inc.CreatedBy); err != nil {
			return nil, err
		}
		results = append(results, inc)
	}
	return results, rows.Err()
}

func buildDayBars() ([]dayStatus, error) {
	now := time.Now().UTC()
	days := make([]dayStatus, 90)
	for i := range days {
		d := now.AddDate(0, 0, -(89 - i))
		days[i].Date = d.Format("2006-01-02")
	}

	startDate := now.AddDate(0, 0, -89).Format("2006-01-02")

	barRows, err := db.Query(`
		SELECT date(started_at) as d FROM incidents WHERE date(started_at) >= ?
		UNION
		SELECT date(resolved_at) as d FROM incidents WHERE resolved_at IS NOT NULL AND date(resolved_at) >= ?
	`, startDate, startDate)
	if err != nil {
		return nil, err
	}
	defer barRows.Close()

	incidentDays := map[string]bool{}
	for barRows.Next() {
		var d string
		if err := barRows.Scan(&d); err != nil {
			return nil, err
		}
		incidentDays[d] = true
	}

	spanRows, err := db.Query(`SELECT started_at, resolved_at FROM incidents WHERE date(started_at) <= ? AND (resolved_at IS NULL OR date(resolved_at) >= ?)`,
		now.Format("2006-01-02"), startDate)
	if err != nil {
		return nil, err
	}
	defer spanRows.Close()
	for spanRows.Next() {
		var startStr string
		var resolvedStr sql.NullString
		if err := spanRows.Scan(&startStr, &resolvedStr); err != nil {
			return nil, err
		}
		incStart := parseTime(startStr)
		incEnd := now
		if resolvedStr.Valid {
			incEnd = parseTime(resolvedStr.String)
		}
		for d := incStart; !d.After(incEnd); d = d.AddDate(0, 0, 1) {
			incidentDays[d.Format("2006-01-02")] = true
		}
	}

	for i := range days {
		days[i].HasEvent = incidentDays[days[i].Date]
	}
	return days, nil
}

func parseTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
