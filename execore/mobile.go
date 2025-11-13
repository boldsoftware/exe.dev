package execore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"exe.dev/boxname"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/sqlite"
)

const (

	// creationStreamIdleTimeout is how long to keep a creation stream after last access
	creationStreamIdleTimeout = 10 * time.Minute
)

// creationStreamKey uniquely identifies a creation stream
type creationStreamKey struct {
	userID   string
	hostname string
}

// CreationStream holds the output stream for a box being created
type CreationStream struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	logBuf     bytes.Buffer // Complete log for database storage
	done       bool
	err        error
	waiters    []chan struct{}
	hostname   string
	lastAccess time.Time
	key        creationStreamKey
	server     *Server
}

// Write implements io.Writer
func (cs *CreationStream) Write(p []byte) (n int, err error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.lastAccess = time.Now()
	n, err = cs.buf.Write(p)
	// Also write to log buffer for database storage
	cs.logBuf.Write(p)
	// Notify all waiters that new data is available
	for _, ch := range cs.waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return n, err
}

// MarkDone marks the stream as complete
func (cs *CreationStream) MarkDone(err error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.done = true
	cs.err = err
	cs.lastAccess = time.Now()
	// Notify all waiters
	for _, ch := range cs.waiters {
		close(ch)
	}
	cs.waiters = nil
}

// startCleanupTimer starts a goroutine that removes this stream after idle timeout
func (cs *CreationStream) startCleanupTimer() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			cs.mu.Lock()
			idle := time.Since(cs.lastAccess)
			done := cs.done
			cs.mu.Unlock()

			// Clean up if done and idle for long enough
			if done && idle > creationStreamIdleTimeout {
				cs.server.removeCreationStream(cs.key.userID, cs.key.hostname)
				return
			}
		}
	}()
}

// Read reads available data and waits for more if not done
func (cs *CreationStream) Read(p []byte) (n int, err error) {
	cs.mu.Lock()
	cs.lastAccess = time.Now()
	n, err = cs.buf.Read(p)
	if n > 0 || (err != nil && err != io.EOF) {
		cs.mu.Unlock()
		return n, err
	}
	if cs.done {
		err := cs.err
		cs.mu.Unlock()
		if err != nil {
			return 0, err
		}
		return 0, io.EOF
	}
	// No data available and not done - wait
	waitCh := make(chan struct{}, 1)
	cs.waiters = append(cs.waiters, waitCh)
	cs.mu.Unlock()

	<-waitCh
	return 0, nil // Signal to retry
}

// getOrCreateCreationStream gets or creates a creation stream for a user's hostname
func (s *Server) getOrCreateCreationStream(userID, hostname string) *CreationStream {
	s.creationStreamsMu.Lock()
	defer s.creationStreamsMu.Unlock()
	key := creationStreamKey{userID: userID, hostname: hostname}
	if cs, ok := s.creationStreams[key]; ok {
		return cs
	}
	cs := &CreationStream{
		hostname:   hostname,
		key:        key,
		server:     s,
		lastAccess: time.Now(),
	}
	s.creationStreams[key] = cs
	cs.startCleanupTimer()
	return cs
}

// getCreationStream gets an existing creation stream
func (s *Server) getCreationStream(userID, hostname string) *CreationStream {
	s.creationStreamsMu.Lock()
	defer s.creationStreamsMu.Unlock()
	return s.creationStreams[creationStreamKey{userID: userID, hostname: hostname}]
}

// removeCreationStream removes a creation stream after it's done
func (s *Server) removeCreationStream(userID, hostname string) {
	s.creationStreamsMu.Lock()
	defer s.creationStreamsMu.Unlock()
	delete(s.creationStreams, creationStreamKey{userID: userID, hostname: hostname})
}

// startBoxCreation starts creating a box in the background
func (s *Server) startBoxCreation(ctx context.Context, hostname, prompt, userID string) {
	// Check if already creating
	if cs := s.getCreationStream(userID, hostname); cs != nil {
		s.slog().Info("Box creation already in progress", "hostname", hostname, "user_id", userID)
		return
	}

	// Create the stream first so errors can be written to it
	cs := s.getOrCreateCreationStream(userID, hostname)

	// Check if hostname is available
	if !s.isBoxNameAvailable(ctx, hostname) {
		s.slog().Error("Box name not available", "hostname", hostname)
		cs.MarkDone(fmt.Errorf("box name %q is not available", hostname))
		return
	}

	// Start creation in background
	go func() {
		// Create a context for the creation (separate from request context)
		createCtx, cancel := context.WithTimeout(context.Background(), longOperationTimeout)
		defer cancel()

		// Set up the command context
		ss := NewSSHServer(s)
		fs := newCommandFlags()
		_ = fs.Set("name", hostname)
		if prompt != "" {
			_ = fs.Set("prompt", prompt)
		}

		cc := &exemenu.CommandContext{
			User:         &exemenu.UserInfo{ID: userID},
			FlagSet:      fs,
			Output:       cs,
			Logger:       s.slog(),
			ForceSpinner: true,
		}

		// Run the creation
		err := ss.handleNewCommand(createCtx, cc)

		// Save creation log to database
		cs.mu.Lock()
		creationLog := cs.logBuf.String()
		cs.mu.Unlock()

		if saveErr := s.db.Tx(createCtx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, updateErr := tx.Conn().ExecContext(ctx, `UPDATE boxes SET creation_log = ? WHERE name = ?`, creationLog, hostname)
			return updateErr
		}); saveErr != nil {
			s.slog().Error("Failed to save creation log", "error", saveErr, "hostname", hostname)
		}

		if err != nil {
			s.slog().Error("Box creation failed", "hostname", hostname, "error", err)
			cs.MarkDone(err)
			return
		}

		// Clean up pending VM entry if it exists
		if err := s.db.Tx(createCtx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Conn().ExecContext(ctx, `DELETE FROM mobile_pending_vm WHERE user_id = ? AND hostname = ?`, userID, hostname)
			return err
		}); err != nil {
			s.slog().Error("Failed to delete pending mobile VM", "error", err, "user_id", userID, "hostname", hostname)
		}

		cs.MarkDone(nil)
		s.slog().Info("Box creation completed", "hostname", hostname)
	}()
}

// handleMobile handles the mobile UI flow at /m using a mux for cleaner routing
func (s *Server) handleMobile(w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleMobileHome)
	mux.HandleFunc("/check-hostname", s.handleMobileHostnameCheck)
	mux.HandleFunc("/create-vm", s.handleMobileCreateVM)
	mux.HandleFunc("/email-auth", s.handleMobileEmailAuth)
	mux.HandleFunc("POST /verify-token", s.handleMobileVerifyTokenManualEntry)
	mux.HandleFunc("GET /verify-token", s.handleMobileVerifyTokenEmailLink)
	mux.HandleFunc("/home", s.handleMobileVMList)
	mux.HandleFunc("/creating/stream", s.handleMobileCreatingStream)
	mux.HandleFunc("/box/creation-log", s.handleBoxCreationLog)

	// Strip /m prefix before passing to mux
	originalURL := r.URL.Path
	r.URL.Path = strings.TrimPrefix(originalURL, "/m")
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}

	mux.ServeHTTP(w, r)

	// Restore original URL path
	r.URL.Path = originalURL
}

// handleMobileHome renders the initial mobile page
func (s *Server) handleMobileHome(w http.ResponseWriter, r *http.Request) {
	// Only handle exact "/" path (which is /m after prefix stripping)
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Check if user is already authenticated
	if _, err := s.validateAuthCookie(r); err == nil {
		// User is authenticated, redirect to unified dashboard
		http.Redirect(w, r, "/~", http.StatusSeeOther)
		return
	}

	// Generate a random hostname suggestion
	hostnameSuggestion := boxname.Random()

	data := struct {
		HostnameSuggestion string
		IsLoggedIn         bool
	}{
		HostnameSuggestion: hostnameSuggestion,
		IsLoggedIn:         false, // Already checked auth above, if we're here user is not logged in
	}

	s.renderTemplate(w, "new.html", data)
}

// handleMobileNew renders the new box form
func (s *Server) handleMobileNew(w http.ResponseWriter, r *http.Request) {
	// Always show the new box form
	hostnameSuggestion := boxname.Random()

	// Check if user is logged in
	_, err := s.validateAuthCookie(r)
	isLoggedIn := err == nil

	data := struct {
		HostnameSuggestion string
		IsLoggedIn         bool
		ActivePage         string
	}{
		HostnameSuggestion: hostnameSuggestion,
		IsLoggedIn:         isLoggedIn,
		ActivePage:         "",
	}
	s.renderTemplate(w, "new.html", data)
}

// handleMobileHostnameCheck checks if a hostname is available
func (s *Server) handleMobileHostnameCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request struct {
		Hostname string `json:"hostname"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	name := strings.ToLower(strings.TrimSpace(request.Hostname))
	if name == "" {
		http.Error(w, "Box name is required", http.StatusBadRequest)
		return
	}

	// Check if hostname is valid and available
	isValid := boxname.Valid(name)
	isAvailable := true

	if isValid {
		isAvailable = s.isBoxNameAvailable(r.Context(), name)
	}

	response := struct {
		Valid     bool   `json:"valid"`
		Available bool   `json:"available"`
		Message   string `json:"message"`
	}{
		Valid:     isValid,
		Available: isAvailable,
	}

	switch {
	case !isValid:
		response.Message = boxname.InvalidBoxNameMessage
	case !isAvailable:
		response.Message = "That box name is not available."
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// handleMobileCreateVM handles VM creation request
func (s *Server) handleMobileCreateVM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hostname := strings.ToLower(strings.TrimSpace(r.FormValue("hostname")))
	prompt := strings.TrimSpace(r.FormValue("prompt"))

	s.slog().Info("Mobile VM creation request", "hostname", hostname, "prompt", prompt)

	// If user is logged in, start creation immediately and redirect to dashboard
	if userID, err := s.validateAuthCookie(r); err == nil {
		// Start box creation in background
		s.startBoxCreation(r.Context(), hostname, prompt, userID)
		http.Redirect(w, r, "/~?filter="+urlQueryEscape(hostname), http.StatusSeeOther)
		return
	}

	// Otherwise, proceed to email auth, carrying the VM details as hidden fields
	data := map[string]interface{}{
		"Hostname": hostname,
		"Prompt":   prompt,
	}

	s.renderTemplate(w, "auth-form.html", data)
}

// handleMobileEmailAuth handles email authentication
func (s *Server) handleMobileEmailAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	email := strings.TrimSpace(r.FormValue("email"))
	hostname := strings.ToLower(strings.TrimSpace(r.FormValue("hostname")))
	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if email == "" {
		http.Error(w, "Email is required", http.StatusBadRequest)
		return
	}

	// Basic email validation
	if !isValidEmail(email) {
		http.Error(w, "Invalid email format", http.StatusBadRequest)
		return
	}

	// Generate verification token
	token := generateRegistrationToken()

	// Store email verification token in database
	err := s.storeEmailVerification(r.Context(), email, token)
	if err != nil {
		s.slog().Error("Failed to store email verification", "error", err)
		http.Error(w, "Failed to process request", http.StatusInternalServerError)
		return
	}

	// Record pending VM creation details for this user+token so we can create after verification
	// Lookup user_id by email (storeEmailVerification ensures user+alloc exists)
	userID, err := withRxRes(s, r.Context(), func(ctx context.Context, q *exedb.Queries) (string, error) {
		return q.GetUserIDByEmail(ctx, email)
	})
	if err != nil {
		s.slog().Error("Failed to lookup user after email auth", "email", email, "error", err)
		http.Error(w, "Failed to process request", http.StatusInternalServerError)
		return
	}
	if hostname != "" {
		if err := s.db.Tx(r.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Conn().ExecContext(ctx, `INSERT OR REPLACE INTO mobile_pending_vm (token, user_id, hostname, prompt) VALUES (?, ?, ?, ?)`, token, userID, hostname, prompt)
			return err
		}); err != nil {
			s.slog().Error("Failed to store pending mobile VM", "error", err)
			http.Error(w, "Failed to process request", http.StatusInternalServerError)
			return
		}
	}

	// Send verification email
	verifyURL := fmt.Sprintf("%s/m/verify-token?token=%s", s.getBaseURL(), token)
	subject := "Verify your email - exe.dev"
	body := fmt.Sprintf(`Hello,

Please click the link below to verify your email and complete your setup:

%s

Or enter this token:

%s

This link will expire in 24 hours.

Best regards,
The exe.dev team`, verifyURL, token)

	err = s.sendEmail(email, subject, body)
	if err != nil {
		s.slog().Error("Failed to send verification email", "error", err)
		http.Error(w, "Failed to send email", http.StatusInternalServerError)
		return
	}

	data := struct {
		Email       string
		QueryString string
		DevURL      string
	}{
		Email:       email,
		QueryString: r.URL.RawQuery,
	}

	if s.devMode != "" {
		data.DevURL = verifyURL
	}

	s.renderTemplate(w, "email-sent.html", data)
}

// handleMobileVerifyTokenEmailLink handles token verification via clicked email link
func (s *Server) handleMobileVerifyTokenEmailLink(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "Missing token", http.StatusBadRequest)
		return
	}

	userID, err := s.validateEmailVerificationToken(r.Context(), token)
	if err != nil {
		http.Error(w, "Invalid or expired token", http.StatusBadRequest)
		return
	}

	// Create auth cookie
	cookieValue, err := s.createAuthCookie(r.Context(), userID, r.Host)
	if err != nil {
		s.slog().Error("Failed to create auth cookie", "error", err)
		http.Error(w, "Authentication failed", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "exe-auth",
		Value:    cookieValue,
		Path:     "/",
		Expires:  time.Now().Add(30 * 24 * time.Hour),
		Secure:   s.devMode == "", // Only secure in production
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	// If we have a pending VM tied to this token, start creation and redirect to dashboard
	var hostname, prompt string
	err = s.db.Rx(r.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		row := rx.Conn().QueryRowContext(ctx, `SELECT hostname, prompt FROM mobile_pending_vm WHERE token = ?`, token)
		return row.Scan(&hostname, &prompt)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Redirect(w, r, "/~", http.StatusSeeOther)
			return
		}
		s.slog().Error("Failed to query pending mobile VM by token", "error", err)
		http.Error(w, "Failed to process request", http.StatusInternalServerError)
		return
	}
	if hostname != "" {
		// Start box creation in background
		s.startBoxCreation(r.Context(), hostname, prompt, userID)
		http.Redirect(w, r, "/~?filter="+urlQueryEscape(hostname), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/~", http.StatusSeeOther)
}

// handleMobileVerifyTokenManualEntry handles token verification via manual entry form
func (s *Server) handleMobileVerifyTokenManualEntry(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.FormValue("token"))
	if token == "" {
		http.Error(w, "Token is required", http.StatusBadRequest)
		return
	}

	// Find full token by code prefix
	userID, err := s.validateEmailVerificationByToken(r.Context(), token)
	if err != nil {
		http.Error(w, "Invalid or expired token", http.StatusBadRequest)
		return
	}

	// Create auth cookie
	cookieValue, err := s.createAuthCookie(r.Context(), userID, r.Host)
	if err != nil {
		s.slog().Error("Failed to create auth cookie", "error", err)
		http.Error(w, "Authentication failed", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "exe-auth",
		Value:    cookieValue,
		Path:     "/",
		Expires:  time.Now().Add(30 * 24 * time.Hour),
		Secure:   s.devMode == "", // Only secure in production
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	// Look up the most recent pending VM for this user
	var hostname, prompt string
	err = s.db.Rx(r.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		row := rx.Conn().QueryRowContext(ctx, `SELECT hostname, prompt FROM mobile_pending_vm WHERE user_id = ? ORDER BY created_at DESC LIMIT 1`, userID)
		return row.Scan(&hostname, &prompt)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Redirect(w, r, "/~", http.StatusSeeOther)
			return
		}
		s.slog().Error("Failed to query pending mobile VM by user_id", "error", err)
		http.Error(w, "Failed to process request", http.StatusInternalServerError)
		return
	}
	if hostname != "" {
		// Start box creation in background
		s.startBoxCreation(r.Context(), hostname, prompt, userID)
		http.Redirect(w, r, "/~?filter="+urlQueryEscape(hostname), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/~", http.StatusSeeOther)
}

// handleMobileVMList redirects to the unified dashboard
func (s *Server) handleMobileVMList(w http.ResponseWriter, r *http.Request) {
	// Check authentication
	_, err := s.validateAuthCookie(r)
	if err != nil {
		http.Redirect(w, r, "/m", http.StatusSeeOther)
		return
	}

	// Redirect to unified dashboard
	http.Redirect(w, r, "/~", http.StatusSeeOther)
}

// Helper functions for mobile

// sseEvent writes a named SSE event with data
func sseEvent(w http.ResponseWriter, event, data string) {
	fmt.Fprintf(w, "event: %s\n", event)
	fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// terminalAddress returns the terminal URL for a box
func (s *Server) terminalAddress(boxName string) string {
	if s.devMode != "" {
		return fmt.Sprintf("http://%s.xterm.localhost:%d/", boxName, s.httpLn.tcp.Port)
	}
	return fmt.Sprintf("https://%s.xterm.exe.dev/", boxName)
}

// handleMobileCreatingStream streams progress from an in-memory creation stream
func (s *Server) handleMobileCreatingStream(w http.ResponseWriter, r *http.Request) {
	// Require auth
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	// Read hostname from query parameter
	hostname := strings.TrimSpace(r.URL.Query().Get("hostname"))
	if hostname == "" || !boxname.Valid(hostname) {
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}

	// Look up the creation stream
	cs := s.getCreationStream(userID, hostname)
	if cs == nil {
		// No stream, just return empty - it's fine
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		return
	}

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Read and stream the creation output as raw bytes
	buf := make([]byte, 4096)
	for {
		n, err := cs.Read(buf)
		if n > 0 {
			// Send base64-encoded terminal output as SSE data event
			encoded := base64.StdEncoding.EncodeToString(buf[:n])
			fmt.Fprintf(w, "data: %s\n\n", encoded)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err == io.EOF {
			// Creation completed successfully
			httpsURL := s.httpsProxyAddress(hostname)
			termURL := s.terminalAddress(hostname)
			sseEvent(w, "done", fmt.Sprintf("%s|%s", httpsURL, termURL))
			return
		}
		if err != nil {
			// Creation failed
			sseEvent(w, "fail", err.Error())
			return
		}
		// No data yet, read will have waited and returned 0, nil - try again
	}
}

// handleBoxCreationLog returns the stored creation log for a box
func (s *Server) handleBoxCreationLog(w http.ResponseWriter, r *http.Request) {
	// Require auth
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	// Read hostname from query parameter
	hostname := strings.TrimSpace(r.URL.Query().Get("hostname"))
	if hostname == "" || !boxname.Valid(hostname) {
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}

	// Get the box
	box, err := s.getBoxForUserByUserID(r.Context(), userID, hostname)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Return the creation log (raw terminal output)
	w.Header().Set("Content-Type", "application/octet-stream")
	if box.CreationLog != nil && *box.CreationLog != "" {
		w.Write([]byte(*box.CreationLog))
	} else {
		w.Write([]byte(""))
	}
}

// getBoxForUserByUserID fetches a box for a user by userID and name
func (s *Server) getBoxForUserByUserID(ctx context.Context, userID, boxName string) (*exedb.Box, error) {
	b, err := withRxRes(s, ctx, func(ctx context.Context, q *exedb.Queries) (exedb.Box, error) {
		return q.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{Name: boxName, CreatedByUserID: userID})
	})
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// urlQueryEscape escapes a string for URL query
func urlQueryEscape(sv string) string {
	return strings.ReplaceAll(strings.ReplaceAll(sv, " ", "+"), "\n", " ")
}
