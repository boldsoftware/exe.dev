package exe

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"exe.dev/billing"
	"exe.dev/exedb"
	"exe.dev/sqlite"
)

// handleMobile handles the mobile UI flow at /m using a mux for cleaner routing
func (s *Server) handleMobile(w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleMobileHome)
	mux.HandleFunc("/new", s.handleMobileNew)
	mux.HandleFunc("/check-hostname", s.handleMobileHostnameCheck)
	mux.HandleFunc("/create-vm", s.handleMobileCreateVM)
	mux.HandleFunc("/email-auth", s.handleMobileEmailAuth)
	mux.HandleFunc("POST /verify-token", s.handleMobileVerifyTokenManualEntry)
	mux.HandleFunc("GET /verify-token", s.handleMobileVerifyTokenEmailLink)
	mux.HandleFunc("/home", s.handleMobileVMList)
	mux.HandleFunc("/creating", s.handleMobileCreatingPage)
	mux.HandleFunc("/creating/stream", s.handleMobileCreatingStream)
	mux.HandleFunc("/box/", s.handleMobileBoxPage)

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
	// Check if user is already authenticated
	if cookie, err := r.Cookie("exe-auth"); err == nil && cookie.Value != "" {
		if _, err := s.validateAuthCookie(r.Context(), cookie.Value, r.Host); err == nil {
			// User is authenticated, show their VM list at /m
			s.handleMobileVMList(w, r)
			return
		}
	}

	// Generate a random hostname suggestion
	hostnameSuggestion := generateRandomBoxName()

	data := struct {
		HostnameSuggestion string
	}{
		HostnameSuggestion: hostnameSuggestion,
	}

	s.renderTemplate(w, "mobile-home.html", data)
}

// handleMobileNew renders the VM creation page explicitly
func (s *Server) handleMobileNew(w http.ResponseWriter, r *http.Request) {
	// Always show the create page (even if logged in)
	hostnameSuggestion := generateRandomBoxName()
	data := struct {
		HostnameSuggestion string
	}{
		HostnameSuggestion: hostnameSuggestion,
	}
	s.renderTemplate(w, "mobile-home.html", data)
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

	hostname := strings.TrimSpace(request.Hostname)
	if hostname == "" {
		http.Error(w, "Hostname is required", http.StatusBadRequest)
		return
	}

	// Check if hostname is valid and available
	isValid := isValidBoxName(hostname)
	isAvailable := true

	if isValid {
		isAvailable = s.isBoxNameAvailable(r.Context(), hostname)
	}

	response := struct {
		Valid     bool   `json:"valid"`
		Available bool   `json:"available"`
		Message   string `json:"message"`
	}{
		Valid:     isValid,
		Available: isAvailable,
	}

	if !isValid {
		response.Message = "Invalid hostname format. Must be 5-64 characters, letters, numbers, and hyphens only."
	} else if !isAvailable {
		response.Message = "This hostname is already taken. Please choose another."
	} else {
		response.Message = "This hostname is available!"
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

	hostname := strings.TrimSpace(r.FormValue("hostname"))
	description := strings.TrimSpace(r.FormValue("description"))

	slog.Info("Mobile VM creation request", "hostname", hostname, "description", description)

	// If user is logged in, go directly to creating page with SSE
	if cookie, err := r.Cookie("exe-auth"); err == nil && cookie.Value != "" {
		if _, err := s.validateAuthCookie(r.Context(), cookie.Value, r.Host); err == nil {
			http.Redirect(w, r, "/m/creating?hostname="+urlQueryEscape(hostname)+"&description="+urlQueryEscape(description), http.StatusTemporaryRedirect)
			return
		}
	}

	// Otherwise, proceed to email auth, carrying the VM details as hidden fields
	data := struct {
		Hostname    string
		Description string
	}{
		Hostname:    hostname,
		Description: description,
	}

	s.renderTemplate(w, "mobile-email-auth.html", data)
}

// handleMobileEmailAuth handles email authentication
func (s *Server) handleMobileEmailAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	email := strings.TrimSpace(r.FormValue("email"))
	hostname := strings.TrimSpace(r.FormValue("hostname"))
	description := strings.TrimSpace(r.FormValue("description"))
	if email == "" {
		http.Error(w, "Email is required", http.StatusBadRequest)
		return
	}

	// Basic email validation
	if !s.isValidEmail(email) {
		http.Error(w, "Invalid email format", http.StatusBadRequest)
		return
	}

	// Generate verification token
	token := s.generateRegistrationToken()

	// Store email verification token in database
	err := s.storeEmailVerification(r.Context(), email, token)
	if err != nil {
		slog.Error("Failed to store email verification", "error", err)
		http.Error(w, "Failed to process request", http.StatusInternalServerError)
		return
	}

	// Record pending VM creation details for this user+token so we can create after verification
	// Lookup user_id by email (storeEmailVerification ensures user+alloc exists)
	userID, err := withRxRes(s, r.Context(), func(ctx context.Context, q *exedb.Queries) (string, error) {
		return q.GetUserIDByEmail(ctx, email)
	})
	if err != nil {
		slog.Error("Failed to lookup user after email auth", "email", email, "error", err)
		http.Error(w, "Failed to process request", http.StatusInternalServerError)
		return
	}
	if hostname != "" {
		if err := s.db.Tx(r.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Conn().ExecContext(ctx, `INSERT OR REPLACE INTO mobile_pending_vm (token, user_id, hostname, description) VALUES (?, ?, ?, ?)`, token, userID, hostname, description)
			return err
		}); err != nil {
			slog.Error("Failed to store pending mobile VM", "error", err)
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
		slog.Error("Failed to send verification email", "error", err)
		http.Error(w, "Failed to send email", http.StatusInternalServerError)
		return
	}

	data := struct {
		Email  string
		DevURL string
		Code   string
	}{
		Email: email,
		Code:  token[:8],
	}

	if s.devMode != "" {
		data.DevURL = verifyURL
	}

	s.renderTemplate(w, "mobile-email-sent.html", data)
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
		slog.Error("Failed to create auth cookie", "error", err)
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

	// If we have a pending VM tied to this token, redirect to creating page
	var hostname string
	err = s.db.Rx(r.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		row := rx.Conn().QueryRowContext(ctx, `SELECT hostname FROM mobile_pending_vm WHERE token = ?`, token)
		return row.Scan(&hostname)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Redirect(w, r, "/m", http.StatusTemporaryRedirect)
			return
		}
		slog.Error("Failed to query pending mobile VM by token", "error", err)
		http.Error(w, "Failed to process request", http.StatusInternalServerError)
		return
	}
	if hostname != "" {
		http.Redirect(w, r, "/m/creating?hostname="+urlQueryEscape(hostname), http.StatusTemporaryRedirect)
		return
	}
	http.Redirect(w, r, "/m", http.StatusTemporaryRedirect)
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
		slog.Error("Failed to create auth cookie", "error", err)
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
	var hostname string
	err = s.db.Rx(r.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		row := rx.Conn().QueryRowContext(ctx, `SELECT hostname FROM mobile_pending_vm WHERE user_id = ? ORDER BY created_at DESC LIMIT 1`, userID)
		return row.Scan(&hostname)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Redirect(w, r, "/m", http.StatusTemporaryRedirect)
			return
		}
		slog.Error("Failed to query pending mobile VM by user_id", "error", err)
		http.Error(w, "Failed to process request", http.StatusInternalServerError)
		return
	}
	if hostname != "" {
		http.Redirect(w, r, "/m/creating?hostname="+urlQueryEscape(hostname), http.StatusTemporaryRedirect)
		return
	}
	http.Redirect(w, r, "/m", http.StatusTemporaryRedirect)
}

// handleMobileVMList shows the user's VM list
func (s *Server) handleMobileVMList(w http.ResponseWriter, r *http.Request) {
	// Check authentication
	cookie, err := r.Cookie("exe-auth")
	if err != nil || cookie.Value == "" {
		http.Redirect(w, r, "/m", http.StatusTemporaryRedirect)
		return
	}

	userID, err := s.validateAuthCookie(r.Context(), cookie.Value, r.Host)
	if err != nil {
		http.Redirect(w, r, "/m", http.StatusTemporaryRedirect)
		return
	}

	// Get user's allocation
	alloc, err := s.getUserAlloc(r.Context(), userID)
	if err != nil {
		slog.Error("Failed to get user allocation", "error", err, "user_id", userID)
		http.Error(w, "Failed to load user data", http.StatusInternalServerError)
		return
	}

	// Get user's boxes
	var boxes []exedb.Box
	if alloc != nil {
		boxes, err = s.getBoxesForAlloc(r.Context(), alloc.AllocID)
		if err != nil {
			slog.Error("Failed to get boxes", "error", err, "alloc_id", alloc.AllocID)
			http.Error(w, "Failed to load boxes", http.StatusInternalServerError)
			return
		}
	}

	// Convert []exedb.Box to []*exedb.Box
	boxPtrs := make([]*exedb.Box, len(boxes))
	for i := range boxes {
		boxPtrs[i] = &boxes[i]
	}

	data := struct {
		Boxes []*exedb.Box
	}{
		Boxes: boxPtrs,
	}

	s.renderTemplate(w, "mobile-vm-list.html", data)
}

// Helper functions for mobile

// handleMobileCreatingPage shows a creating screen that connects to SSE for progress
func (s *Server) handleMobileCreatingPage(w http.ResponseWriter, r *http.Request) {
	// Require auth
	cookie, err := r.Cookie("exe-auth")
	if err != nil || cookie.Value == "" {
		http.Redirect(w, r, "/m", http.StatusTemporaryRedirect)
		return
	}
	if _, err := s.validateAuthCookie(r.Context(), cookie.Value, r.Host); err != nil {
		http.Redirect(w, r, "/m", http.StatusTemporaryRedirect)
		return
	}
	hostname := strings.TrimSpace(r.URL.Query().Get("hostname"))
	data := struct{ Hostname string }{Hostname: hostname}
	s.renderTemplate(w, "mobile-creating.html", data)
}

// sseWrite is a helper to write SSE data events
func sseWrite(w http.ResponseWriter, data string) {
	fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// sseEvent writes a named SSE event with data
func sseEvent(w http.ResponseWriter, event, data string) {
	fmt.Fprintf(w, "event: %s\n", event)
	fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// sseLineWriter implements io.Writer and converts writes to SSE data lines
type sseLineWriter struct {
	w   http.ResponseWriter
	buf strings.Builder
}

func (sw *sseLineWriter) Write(p []byte) (int, error) {
	// Process incoming bytes; flush on newline as a normal message.
	// If we see carriage-return updates without newline (spinner frames),
	// emit them immediately as a dedicated SSE event `spin` so the client
	// can render in-place progress updates.
	for _, b := range p {
		if b == '\n' {
			// Completed line: send as a standard message
			line := sw.buf.String()
			sseWrite(sw.w, line)
			sw.buf.Reset()
			continue
		}
		sw.buf.WriteByte(b)
	}
	// If buffer contains a carriage return but no newline, it's likely an in-place spinner update.
	// Flush it immediately as a spin event to keep the UI responsive.
	if sw.buf.Len() > 0 {
		s := sw.buf.String()
		if strings.Contains(s, "\r") && !strings.Contains(s, "\n") {
			sseEvent(sw.w, "spin", s)
			sw.buf.Reset()
		}
	}
	return len(p), nil
}

// terminalAddress returns the terminal URL for a box
func (s *Server) terminalAddress(boxName string) string {
	if s.devMode != "" {
		return fmt.Sprintf("http://%s.xterm.localhost:%d/", boxName, s.httpLn.tcp.Port)
	}
	return fmt.Sprintf("https://%s.xterm.exe.dev/", boxName)
}

// handleMobileCreatingStream streams progress via SSE and creates the VM
func (s *Server) handleMobileCreatingStream(w http.ResponseWriter, r *http.Request) {
	// Require POST for creation (write action)
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Require auth and resolve user and alloc
	cookie, err := r.Cookie("exe-auth")
	if err != nil || cookie.Value == "" {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}
	userID, err := s.validateAuthCookie(r.Context(), cookie.Value, r.Host)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	// Read hostname from POST body
	hostname := strings.TrimSpace(r.PostFormValue("hostname"))
	if hostname == "" || !isValidBoxName(hostname) {
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}
	if !s.isBoxNameAvailable(r.Context(), hostname) {
		http.Error(w, "Hostname not available", http.StatusConflict)
		return
	}

	alloc, err := s.getUserAlloc(r.Context(), userID)
	if err != nil || alloc == nil {
		http.Error(w, "No allocation", http.StatusInternalServerError)
		return
	}

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Initial message
	sseWrite(w, fmt.Sprintf("Creating %s...", hostname))

	// Reuse the SSH command handler for creation, wiring Output to SSE
	ss := NewSSHServer(s, billing.New(s.db))

	fs := newCommandFlags()
	_ = fs.Set("name", hostname)
	cc := &CommandContext{
		User:    &User{UserID: userID},
		Alloc:   alloc,
		FlagSet: fs,
		Output:  &sseLineWriter{w: w},
		// Force spinner/progress output for HTTP/SSE flows
		ForceSpinner: true,
	}

	if err := ss.handleNewCommand(r.Context(), cc); err != nil {
		slog.Error("mobile create failed", "user_id", userID, "hostname", hostname, "error", err)
		sseEvent(w, "fail", err.Error())
		return
	}

	if err := s.db.Tx(r.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `DELETE FROM mobile_pending_vm WHERE user_id = ? AND hostname = ?`, userID, hostname)
		return err
	}); err != nil {
		slog.Error("Failed to delete pending mobile VM", "error", err, "user_id", userID, "hostname", hostname)
	}

	httpsURL := s.httpsProxyAddress(hostname)
	termURL := s.terminalAddress(hostname)
	sseEvent(w, "done", fmt.Sprintf("%s|%s", httpsURL, termURL))
}

// getBoxForUserByUserID fetches a box for a user by userID and name
func (s *Server) getBoxForUserByUserID(ctx context.Context, userID, boxName string) (*exedb.Box, error) {
	b, err := withRxRes(s, ctx, func(ctx context.Context, q *exedb.Queries) (exedb.Box, error) {
		return q.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{Name: boxName, UserID: userID})
	})
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// handleMobileBoxPage shows details for a box
func (s *Server) handleMobileBoxPage(w http.ResponseWriter, r *http.Request) {
	// Require auth
	cookie, err := r.Cookie("exe-auth")
	if err != nil || cookie.Value == "" {
		http.Redirect(w, r, "/m", http.StatusTemporaryRedirect)
		return
	}
	userID, err := s.validateAuthCookie(r.Context(), cookie.Value, r.Host)
	if err != nil {
		http.Redirect(w, r, "/m", http.StatusTemporaryRedirect)
		return
	}

	// Extract name after /box/
	name := strings.TrimPrefix(r.URL.Path, "/box/")
	name = strings.Trim(name, "/")
	if name == "" {
		http.Error(w, "Missing box name", http.StatusBadRequest)
		return
	}
	box, err := s.getBoxForUserByUserID(r.Context(), userID, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := struct {
		Box       *exedb.Box
		HTTPURL   string
		TermURL   string
		HostLabel string
	}{
		Box:       box,
		HTTPURL:   s.httpsProxyAddress(box.Name),
		TermURL:   s.terminalAddress(box.Name),
		HostLabel: fmt.Sprintf("%s.exe.dev", box.Name),
	}
	s.renderTemplate(w, "mobile-vm.html", data)
}

// urlQueryEscape escapes a string for URL query
func urlQueryEscape(sv string) string {
	return strings.ReplaceAll(strings.ReplaceAll(sv, " ", "+"), "\n", " ")
}
