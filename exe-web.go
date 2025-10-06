// Package exe implements the bulk of the exed server. This file
// has any web-related stuff in it.
package exe

import (
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"exe.dev/exedb"
	"exe.dev/llmgateway"
	"exe.dev/porkbun"
	"exe.dev/sqlite"
	templatespkg "exe.dev/templates"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/crypto/acme/autocert"
	_ "modernc.org/sqlite"
	"tailscale.com/client/tailscale"
	"tailscale.com/net/tsaddr"
)

func (s *Server) prepareHandler() http.Handler {
	lg := s.prepareLlmGateway()
	servMux := http.NewServeMux()
	servMux.Handle("/_/gateway/", lg)
	servMux.Handle("/", s)

	// Use standard promhttp instrumentation
	instrumentedHandler := promhttp.InstrumentMetricHandler(
		s.metricsRegistry,
		servMux)

	h := LoggerMiddleware(slog.Default())(instrumentedHandler)
	return h
}

// setupHTTPServer configures the HTTP server
func (s *Server) setupHTTPServer() {
	h := s.prepareHandler()

	s.httpServer = &http.Server{
		Addr:    s.httpLn.addr,
		Handler: h,
	}
}

func (s *Server) prepareLlmGateway() http.Handler {
	anthropicAPIKey := os.Getenv("ANTHROPIC_API_KEY")
	fireworksAPIKey := os.Getenv("FIREWORKS_API_KEY")
	openaiAPIKey := os.Getenv("OPENAI_API_KEY")

	lg := llmgateway.NewGateway(s.accountant, s.db, s, llmgateway.APIKeys{
		Anthropic: anthropicAPIKey,
		Fireworks: fireworksAPIKey,
		OpenAI:    openaiAPIKey,
	}, s.devMode != "")
	return lg
}

// setupHTTPSServer configures the HTTPS server with Let's Encrypt if enabled
func (s *Server) setupHTTPSServer() {
	if s.httpsLn.ln == nil {
		return
	}

	// Check if Porkbun API credentials are available for wildcard cert
	porkbunAPIKey := os.Getenv("PORKBUN_API_KEY")
	porkbunSecretKey := os.Getenv("PORKBUN_SECRET_API_KEY")

	if porkbunAPIKey != "" && porkbunSecretKey != "" {
		// Use Porkbun for wildcard certificates with DNS challenge
		slog.Info("Using Porkbun DNS provider for wildcard TLS certificates")
		s.wildcardCertManager = porkbun.NewWildcardCertManager(
			"exe.dev",
			"support@exe.dev",
			porkbunAPIKey,
			porkbunSecretKey,
			autocert.DirCache("certs"),
		)
	} else {
		// Fall back to regular autocert for non-wildcard certificates
		slog.Info("Using standard autocert (no wildcard support)", "note", "Set PORKBUN_API_KEY and PORKBUN_SECRET_API_KEY for wildcard certificates")
		s.certManager = &autocert.Manager{
			Cache:      autocert.DirCache("certs"),
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist("exe.dev"),
		}
	}

	// Single TLS dispatcher for all domains (exe.dev and Tailscale)
	s.httpsServer = &http.Server{
		Addr:    s.httpsLn.addr,
		Handler: s.prepareHandler(),
		TLSConfig: &tls.Config{
			GetCertificate: s.getCertificate,
		},
	}

	// Discover Tailscale DNS name early; certificate retrieval can happen lazily in getCertificate
	// If certs don't work, you might need to run the following in prod:
	//  sudo tailscale set --operator=$USER
	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tailscale.I_Acknowledge_This_API_Is_Unstable = true
		lc := &tailscale.LocalClient{}
		st, err := lc.Status(ctx)
		if err != nil || st == nil || st.Self == nil || st.Self.DNSName == "" {
			if err != nil {
				slog.Debug("tailscale status unavailable", "error", err)
			} else {
				slog.Debug("tailscale DNS name not found")
			}
			return
		}
		s.tsDomain = strings.TrimSuffix(st.Self.DNSName, ".")

		// Try to eagerly fetch and cache cert, but it's optional
		certPEM, keyPEM, err := lc.CertPair(ctx, s.tsDomain)
		if err != nil {
			slog.Debug("tailscale cert pair not preloaded", "error", err)
			return
		}
		c, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			slog.Debug("tailscale x509 keypair parse error", "error", err)
			return
		}
		if len(c.Certificate) > 0 {
			if leaf, err := x509.ParseCertificate(c.Certificate[0]); err == nil {
				c.Leaf = leaf
			}
		}
		s.tsCert = &c
		slog.Info("tailscale cert loaded", "domain", s.tsDomain)
	}()
}

// getCertificate is the single TLS certificate dispatcher for HTTPS.
// It serves:
// - Tailscale node certificate for the machine's Tailscale DNS name
// - Wildcard exe.dev certificates (via Porkbun DNS-01) when configured
// - Standard autocert for exe.dev when wildcard manager is not configured
func (s *Server) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello == nil || hello.ServerName == "" {
		return nil, fmt.Errorf("no server name provided")
	}

	serverName := strings.TrimSuffix(strings.ToLower(hello.ServerName), ".")

	// 1) Serve Tailscale certificate for exact Tailscale DNS name
	if s.tsDomain != "" && serverName == strings.ToLower(s.tsDomain) {
		if s.tsCert != nil {
			return s.tsCert, nil
		}
		return nil, fmt.Errorf("tailscale certificate not available for %s", s.tsDomain)
	}

	// 2) exe.dev handling
	if serverName == "exe.dev" || serverName == "www.exe.dev" || strings.HasSuffix(serverName, ".exe.dev") {
		if s.wildcardCertManager != nil {
			return s.wildcardCertManager.GetCertificate(hello)
		}
		if s.certManager != nil {
			return s.certManager.GetCertificate(hello)
		}
		return nil, fmt.Errorf("no certificate manager configured for exe.dev")
	}

	return nil, fmt.Errorf("unsupported domain %s", hello.ServerName)
}

// setupProxyServers configures additional listeners for proxy ports
func (s *Server) setupProxyServers() {
	proxyPorts := s.getProxyPorts()
	s.proxyLns = make([]*listener, 0, len(proxyPorts))

	// Create listeners for each proxy port
	for _, port := range proxyPorts {
		addr := fmt.Sprintf(":%d", port)
		ln, err := startListener(fmt.Sprintf("proxy-%d", port), addr)
		if err != nil {
			slog.Warn("Failed to listen on proxy port, skipping", "port", port, "error", err)
			continue
		}

		s.proxyLns = append(s.proxyLns, ln)

		slog.Debug("proxy listener configured", "addr", ln.tcp.String(), "port", ln.tcp.Port)

	}
}

// renderTemplate is a helper method that handles template parsing and execution
func (s *Server) renderTemplate(w http.ResponseWriter, templateName string, data interface{}) error {
	funcMap := template.FuncMap{
		"contains": strings.Contains,
	}
	tmpl, err := template.New(templateName).Funcs(funcMap).ParseFS(templatespkg.Files, "topbar.html", templateName)
	if err != nil {
		slog.Error("Failed to parse template", "error", err, "template", templateName)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return err
	}

	w.Header().Set("Content-Type", "text/html")
	if err := tmpl.ExecuteTemplate(w, templateName, data); err != nil {
		slog.Error("Failed to execute template", "error", err, "template", templateName)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return err
	}

	return nil
}

// ServeHTTP implements http.Handler for the HTTP server
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	stopping := s.stopping
	s.mu.RUnlock()

	if stopping {
		http.Error(w, "Server is shutting down", http.StatusServiceUnavailable)
		return
	}

	// Request logging occurs in LoggerMiddleware; avoid duplicate per-request logs here.

	// Check if this should be handled by the proxy handler
	isProxy := s.isProxyRequest(r.Host)
	isTerminal := s.isTerminalRequest(r.Host)
	if info := GetRequestLogInfo(r.Context()); info != nil {
		info.IsProxy = isProxy
		info.IsTerminal = isTerminal
	}
	if isTerminal {
		s.handleTerminalRequest(w, r)
		return
	}
	if isProxy {
		s.handleProxyRequest(w, r)
		return
	}

	// Handle root path and user dashboard
	path := r.URL.Path
	// Debug endpoints (pprof, expvar), gated by localhost or Tailscale access
	if strings.HasPrefix(path, "/debug") {
		requireLocalAccess(s.handleDebug)(w, r)
		return
	} else if strings.HasPrefix(path, "/docs") {
		if s.docs != nil && s.docs.Handle(w, r) {
			return
		}
	}
	switch path {
	case "/":
		// If authenticated, show user dashboard; otherwise redirect to /soon
		if userID, err := s.validateAuthCookie(r); err == nil {
			s.handleUserDashboard(w, r, userID)
			return
		}
		http.Redirect(w, r, "/soon", http.StatusTemporaryRedirect)
		return
	case "/soon":
		s.serveStaticFile(w, r, "soon.html")
		return
	case "/blog":
		// Temporary redirect for blog to the coming soon page
		http.Redirect(w, r, "/soon", http.StatusTemporaryRedirect)
		return
	case "/welcome":
		if err := s.renderTemplate(w, "welcome.html", nil); err != nil {
			return
		}
		return
	case "/~", "/~/":
		// User dashboard - require authentication
		userID, err := s.validateAuthCookie(r)
		if err != nil {
			// Not authenticated, redirect to auth
			authURL := fmt.Sprintf("/auth?redirect=%s", url.QueryEscape(r.URL.String()))
			http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
			return
		}
		s.handleUserDashboard(w, r, userID)
		return
	case "/user":
		// User profile page - require authentication
		userID, err := s.validateAuthCookie(r)
		if err != nil {
			// Not authenticated, redirect to auth
			authURL := fmt.Sprintf("/auth?redirect=%s", url.QueryEscape(r.URL.String()))
			http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
			return
		}
		s.handleUserProfile(w, r, userID)
		return
	case "/health":
		s.handleHealth(w, r)
	case "/metrics":
		requireLocalAccess(s.handleMetrics)(w, r)
	case "/about":
		s.serveStaticFile(w, r, "about.html")
	case "/jobs":
		s.serveStaticFile(w, r, "jobs.html")
	case "/waitlist":
		s.handleWaitlist(w, r)
	case "/verify-email":
		s.handleEmailVerificationHTTP(w, r)
	case "/verify-device":
		s.handleDeviceVerificationHTTP(w, r)
	case "/auth":
		s.handleAuth(w, r)
	case "/auth/confirm":
		s.handleAuthConfirm(w, r)
	case "/logout":
		s.handleLogout(w, r)
	case "/logged-out":
		s.handleLoggedOut(w, r)
	case "/shell":
		s.handleWebShell(w, r)
	case "/shell/ws":
		s.handleWebShellWS(w, r)
	default:
		// Handle mobile UI routes
		if strings.HasPrefix(path, "/m") {
			s.handleMobile(w, r)
			return
		}

		if strings.HasPrefix(path, "/auth/") {
			s.handleAuthCallback(w, r)
			return
		}

		// Serve embedded static assets under /static/
		if strings.HasPrefix(path, "/static/") {
			filename := strings.TrimPrefix(path, "/static/")
			// simple security check; our embed only exposes files inside static/
			if filename != "" && !strings.Contains(filename, "..") {
				s.serveStaticFile(w, r, filename)
				return
			}
		}

		// Try to serve static file if GET request
		if r.Method == "GET" && len(path) > 1 {
			filename := path[1:] // Remove leading slash
			// Security check: ensure filename doesn't contain path traversal
			if !strings.Contains(filename, "..") && !strings.Contains(filename, "/") {
				s.serveStaticFile(w, r, filename)
				return
			}
		}
		http.NotFound(w, r)
	}
}

// handleRoot handles requests to the root path
// serveStaticFile serves a file from the embedded static directory using http.FileServer
func (s *Server) serveStaticFile(w http.ResponseWriter, r *http.Request, filename string) {
	// Create a sub-filesystem from the static directory
	staticSubFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Check if file exists
	if _, err := staticSubFS.Open(filename); err != nil {
		http.NotFound(w, r)
		return
	}

	// Create a temporary request with the filename as path
	tempReq := r.Clone(r.Context())
	tempReq.URL.Path = "/" + filename

	// Use http.FileServer to serve the file
	http.FileServer(http.FS(staticSubFS)).ServeHTTP(w, tempReq)
}

// handleHealth handles health check requests
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","timestamp":"%s"}`, time.Now().Format(time.RFC3339))
}

// requireLocalAccess wraps an HTTP handler to only allow access from localhost or Tailscale IPs
func requireLocalAccess(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		remoteIP, err := netip.ParseAddr(host)
		if err != nil {
			http.Error(w, "remoteaddr check: "+r.RemoteAddr+": "+err.Error(), http.StatusInternalServerError)
			return
		}
		if !remoteIP.IsLoopback() && !tsaddr.IsTailscaleIP(remoteIP) {
			http.Error(w, "Access denied", http.StatusUnauthorized)
			return
		}
		handler(w, r)
	}
}

// handleMetrics serves Prometheus metrics, gated by localhost or Tailscale access
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	handler := promhttp.HandlerFor(s.metricsRegistry, promhttp.HandlerOpts{})
	handler.ServeHTTP(w, r)
}

// handleWaitlist handles POSTs from the coming soon waitlist form
func (s *Server) handleWaitlist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"ok":false,"error":"invalid form"}`)
		} else {
			http.Error(w, "invalid form", http.StatusBadRequest)
		}
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" {
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"ok":false,"error":"missing email"}`)
		} else {
			http.Error(w, "missing email", http.StatusBadRequest)
		}
		return
	}
	// Determine remote IP
	remoteIP := r.Header.Get("X-Forwarded-For")
	if remoteIP == "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err == nil {
			remoteIP = host
		} else {
			remoteIP = r.RemoteAddr
		}
	}

	// Collect selected meanings and encode as JSON object
	meanings := r.Form["vm_meaning"]
	var jsonPayload *string
	if len(meanings) > 0 {
		payload := map[string]any{
			"meaning": meanings,
		}
		if b, err := json.Marshal(payload); err == nil {
			s := string(b)
			jsonPayload = &s
		}
	}

	// Store in database and check if this is the first time this email appears
	wasNew := false
	err := s.db.Tx(r.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		var dummy int
		err := tx.QueryRow("SELECT 1 FROM waitlist WHERE email = ? LIMIT 1", email).Scan(&dummy)
		wasNew = errors.Is(err, sql.ErrNoRows)

		if jsonPayload != nil {
			_, err := tx.Exec("INSERT INTO waitlist (email, remote_ip, json) VALUES (?, ?, ?)", email, remoteIP, *jsonPayload)
			return err
		}
		_, err = tx.Exec("INSERT INTO waitlist (email, remote_ip) VALUES (?, ?)", email, remoteIP)
		return err
	})
	if err != nil {
		slog.Error("failed to insert waitlist entry", "err", err)
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"ok":false,"error":"internal error"}`)
		} else {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	// Send confirmation email only on first add
	if err == nil && wasNew {
		subject := "You're on the exe.dev waitlist"
		body := "Hello,\n\nThanks for your interest in exe.dev. You're on the waitlist. We'll reach out as soon as we have space for you.\n\nIn the meantime, we're heads down building a great SSH-first experience.\n\n— exe.dev"
		if sendErr := s.sendEmail(email, subject, body); sendErr != nil {
			slog.Warn("failed to send waitlist email", "email", email, "err", sendErr)
		}
	}

	// Return JSON for JS clients, otherwise redirect back to home
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// showDeviceVerificationForm shows a confirmation form for device verification
func (s *Server) showDeviceVerificationForm(w http.ResponseWriter, r *http.Request, token string) {
	pendingKey, verification, err := s.lookUpDeviceVerification(r.Context(), token)
	switch {
	case errors.Is(err, errExpiredToken), errors.Is(err, sql.ErrNoRows):
		http.Error(w, "invalid or expired verification token", http.StatusNotFound)
		return
	case errors.Is(err, errVerificationNotFound):
		http.Error(w, "verification session not found; please try logging in via SSH again", http.StatusBadRequest)
		return
	case err != nil:
		slog.Error("unexpected error during device verification check", "error", err, "token", token)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := struct {
		Email       string
		PublicKey   string
		Token       string
		PairingCode string
	}{
		Email:       pendingKey.UserEmail,
		PublicKey:   truncatePublicKey(pendingKey.PublicKey),
		Token:       token,
		PairingCode: verification.PairingCode,
	}

	s.renderTemplate(w, "device-verification.html", data)
}

// handleDeviceVerificationHTTP handles web-based device verification
func (s *Server) handleDeviceVerificationHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "failed to parse form", http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	if token == "" {
		http.Error(w, "missing token in form data", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.showDeviceVerificationForm(w, r, token)
		return
	case http.MethodPost:
		// continued below
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pendingKey, verification, err := s.lookUpDeviceVerification(r.Context(), token)
	switch {
	case errors.Is(err, errExpiredToken), errors.Is(err, sql.ErrNoRows):
		http.Error(w, "invalid or expired verification token", http.StatusNotFound)
		return
	case errors.Is(err, errVerificationNotFound):
		http.Error(w, "verification session not found; please try logging in via SSH again", http.StatusBadRequest)
		return
	case err != nil:
		slog.Error("unexpected error during device verification check", "error", err, "token", token)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Add the SSH key to the verified keys and clean up pending key
	err = s.withTx(context.Background(), func(ctx context.Context, queries *exedb.Queries) error {
		// Add SSH key
		err := queries.InsertSSHKeyForEmailUser(ctx, exedb.InsertSSHKeyForEmailUserParams{
			Email:     pendingKey.UserEmail,
			PublicKey: pendingKey.PublicKey,
		})
		if err != nil {
			return err
		}

		// Clean up the pending key
		return queries.DeletePendingSSHKeyByToken(ctx, token)
	})
	if err != nil {
		slog.Error("Failed to add SSH key", "error", err)
		http.Error(w, "Failed to verify device", http.StatusInternalServerError)
		return
	}

	// Signal completion to waiting SSH session
	close(verification.CompleteChan)
	s.deleteEmailVerification(verification)

	data := struct {
		PublicKey string
	}{
		PublicKey: truncatePublicKey(pendingKey.PublicKey),
	}
	s.renderTemplate(w, "device-verified.html", data)
}

var (
	errExpiredToken         = errors.New("verification token has expired")
	errVerificationNotFound = errors.New("verification session not found")
)

func (s *Server) lookUpDeviceVerification(ctx context.Context, token string) (*exedb.PendingSSHKey, *EmailVerification, error) {
	// Look up the pending SSH key to validate token and get info
	pendingKey, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.PendingSSHKey, error) {
		return queries.GetPendingSSHKeyByToken(ctx, token)
	})
	if err != nil {
		return nil, nil, err
	}

	// Check if token has expired
	if time.Now().After(pendingKey.ExpiresAt) {
		// Clean up expired token - use context.Background() to ensure cleanup completes even if client disconnects
		s.withTx(context.Background(), func(ctx context.Context, queries *exedb.Queries) error {
			return queries.DeletePendingSSHKeyByToken(ctx, token)
		})
		return nil, nil, errExpiredToken
	}

	verification := s.lookUpEmailVerification(token)
	if verification == nil {
		return nil, nil, errVerificationNotFound
	}

	return &pendingKey, verification, nil
}

func truncatePublicKey(key string) string {
	if len(key) <= 32 {
		return key
	}
	return key[:32] + "..."
}

// showEmailVerificationForm shows a confirmation form for email verification
func (s *Server) showEmailVerificationForm(w http.ResponseWriter, r *http.Request, token string) {
	var (
		email string
		code  string
	)

	// Check if this is an SSH session token (in-memory)
	verification := s.lookUpEmailVerification(token)

	if verification != nil {
		email = verification.Email
		code = verification.PairingCode
	} else {
		// Check database for HTTP auth token (without consuming it)
		row, err := s.checkEmailVerificationToken(r.Context(), token)
		if err != nil {
			http.Error(w, "Invalid or expired verification token", http.StatusNotFound)
			return
		}
		email = row.Email
		if row.VerificationCode != nil {
			code = *row.VerificationCode
		}
	}

	code = strings.TrimSpace(code)

	// Prepare template data
	data := struct {
		Token       string
		RedirectURL string
		ReturnHost  string
		Email       string
		PairingCode string
	}{
		Token:       token,
		RedirectURL: r.URL.Query().Get("redirect"),
		ReturnHost:  r.URL.Query().Get("return_host"),
		Email:       email,
		PairingCode: code,
	}

	// Render template
	s.renderTemplate(w, "email-verification-form.html", data)
}

// handleEmailVerificationHTTP handles web-based email verification
func (s *Server) handleEmailVerificationHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Missing token parameter", http.StatusBadRequest)
			return
		}
		s.showEmailVerificationForm(w, r, token)
		return
	case http.MethodPost:
		// continued below
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data to get the token from POST
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	if token == "" {
		http.Error(w, "Missing token in form data", http.StatusBadRequest)
		return
	}

	// First check if this is an SSH session token (in-memory)
	verification := s.lookUpEmailVerification(token)

	if verification != nil {
		// This is an SSH session email verification
		user, err := s.createUserWithSSHKey(r.Context(), verification.Email, verification.PublicKey)
		if err != nil {
			slog.Error("failed to create user with SSH key during email verification", "error", err, "token", token)
			http.Error(w, "failed to create user account", http.StatusInternalServerError)
			return
		}

		// Create HTTP auth cookie for this user
		cookieValue, err := s.createAuthCookie(context.Background(), user.UserID, r.Host)
		if err != nil {
			slog.Error("Failed to create auth cookie during SSH email verification", "error", err)
			// Continue anyway - SSH auth will still work
		} else {
			setExeAuthCookie(w, r, cookieValue)
		}

		// Signal completion to SSH session
		close(verification.CompleteChan)

		// Clean up email verification
		s.deleteEmailVerification(verification)
	} else {
		// Not an SSH token, check database for HTTP auth token
		// Try to validate as database token
		userID, err := s.validateEmailVerificationToken(r.Context(), token)
		if err != nil {
			slog.Info("invalid email verification token during verification", "error", err, "token", token, "remote_addr", r.RemoteAddr)
			http.Error(w, "Invalid or expired verification token", http.StatusNotFound)
			return
		}

		// Create HTTP auth cookie for this user
		cookieValue, err := s.createAuthCookie(context.Background(), userID, r.Host)
		if err != nil {
			slog.Error("Failed to create auth cookie during HTTP email verification", "error", err)
			http.Error(w, "Failed to create authentication session", http.StatusInternalServerError)
			return
		}

		setExeAuthCookie(w, r, cookieValue)

		// Clean up the database token (single use)
		err = s.withTx(context.Background(), func(ctx context.Context, queries *exedb.Queries) error {
			return queries.DeleteEmailVerificationByToken(ctx, token)
		})
		if err != nil {
			slog.Error("Failed to cleanup email verification token", "error", err)
			// Continue anyway
		}

		// Check if this is part of a web auth flow with redirect parameters (from form for POST)
		redirectURL := r.FormValue("redirect")
		returnHost := r.FormValue("return_host")
		if redirectURL != "" || returnHost != "" {
			// This is a web auth flow, perform redirect after authentication
			s.redirectAfterAuth(w, r, userID)
			return
		}
	}

	// Send success response (for SSH registrations or standalone verifications)
	s.renderTemplate(w, "email-verified.html", nil)
}

func (s *Server) createUserWithSSHKey(ctx context.Context, email, publicKey string) (*exedb.User, error) {
	// Create the user if they don't exist
	// Note that this is called during email verification,
	// so we must look up the user by email (verified),
	// not by SSH key (which is what we are about to connect to this email).
	user, err := s.GetUserByEmail(ctx, email)
	if err != nil || user == nil {
		slog.Info("User doesn't exist, creating", "email", email)
		// User doesn't exist - create them with their alloc
		user, err = s.createUser(context.Background(), publicKey, email)
		if err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}
		slog.Info("Created new user", "email", email)
	} else {
		slog.Debug("User already exists", "email", email)
	}

	// Store the SSH key as verified
	if publicKey != "" {
		err = s.withTx(context.Background(), func(ctx context.Context, queries *exedb.Queries) error {
			return queries.InsertSSHKeyForEmailUser(ctx, exedb.InsertSSHKeyForEmailUserParams{
				Email:     email,
				PublicKey: publicKey,
			})
		})
		if err != nil {
			slog.Error("Error storing SSH key during verification", "error", err)
		}
	}

	return user, nil
}

// handleAuth handles the main domain authentication flow
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	// Check if user already has a valid exe.dev auth cookie
	if userID, err := s.validateAuthCookie(r); err == nil {
		// User is already authenticated, handle redirect
		s.redirectAfterAuth(w, r, userID)
		return
	}

	// Handle POST request (email submission)
	if r.Method == "POST" {
		s.handleAuthEmailSubmission(w, r)
		return
	}

	// Show authentication form with query parameters
	data := map[string]interface{}{
		"RedirectURL": r.URL.Query().Get("redirect"),
		"ReturnHost":  r.URL.Query().Get("return_host"),
	}
	s.renderTemplate(w, "auth-form.html", data)
}

// handleAuthEmailSubmission handles the email form submission for web auth
func (s *Server) handleAuthEmailSubmission(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" {
		s.showAuthError(w, r, "Please enter a valid email address", "")
		return
	}

	// Basic email validation
	if !strings.Contains(email, "@") || !strings.Contains(email, ".") {
		s.showAuthError(w, r, "Please enter a valid email address", "")
		return
	}

	// Check if user exists
	userID, err := withRxRes(s, r.Context(), func(ctx context.Context, queries *exedb.Queries) (string, error) {
		return queries.GetUserIDByEmail(ctx, email)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.showAuthError(
				w,
				r,
				"No account found with this email address. Please sign up first using SSH.",
				s.formatExeDevConnectionInfo(),
			)
			return
		}
		slog.Error("Database error checking user", "error", err)
		s.showAuthError(w, r, "Database error occurred. Please try again.", "")
		return
	}

	// Generate verification token
	token := s.generateRegistrationToken()

	// Store verification in database (reuse existing email_verifications table)
	err = s.withTx(context.Background(), func(ctx context.Context, queries *exedb.Queries) error {
		return queries.InsertEmailVerification(ctx, exedb.InsertEmailVerificationParams{
			Token:     token,
			Email:     email,
			UserID:    userID,
			ExpiresAt: time.Now().Add(24 * time.Hour),
		})
	})
	if err != nil {
		slog.Error("Failed to store email verification", "error", err)
		s.showAuthError(w, r, "Failed to create verification. Please try again.", "")
		return
	}

	// Create verification link
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	verificationURL := fmt.Sprintf("%s://%s/auth/verify?token=%s", scheme, r.Host, token)

	// Add redirect parameters to the verification URL if present (from form values for POST)
	if redirect := r.FormValue("redirect"); redirect != "" {
		verificationURL += "&redirect=" + url.QueryEscape(redirect)
	}
	if returnHost := r.FormValue("return_host"); returnHost != "" {
		verificationURL += "&return_host=" + url.QueryEscape(returnHost)
	}

	// Send email with proper verification URL that includes redirect params
	scheme2 := "http"
	if r.TLS != nil {
		scheme2 = "https"
	}
	verifyEmailURL := fmt.Sprintf("%s://%s/verify-email?token=%s", scheme2, r.Host, token)

	// Add redirect parameters to the verify-email URL if present (from form values for POST)
	// Both params needed: redirect=path, return_host=subdomain for cross-domain auth flow
	if redirect := r.FormValue("redirect"); redirect != "" {
		verifyEmailURL += "&redirect=" + url.QueryEscape(redirect)
	}
	if returnHost := r.FormValue("return_host"); returnHost != "" {
		verifyEmailURL += "&return_host=" + url.QueryEscape(returnHost)
	}

	// Send custom email for web auth with the proper URL
	subject := "Verify your email - exe.dev"
	body := fmt.Sprintf(`Hello,

Please click the link below to verify your email address:

%s

This link will expire in 24 hours.

Best regards,
The exe.dev team`, verifyEmailURL)

	err = s.sendEmail(email, subject, body)
	if err != nil {
		slog.Error("Failed to send auth email", "error", err)
		s.showAuthError(w, r, "Failed to send email. Please try again or contact support.", "")
		return
	}

	// Show success page
	var devURL string
	if s.devMode != "" && (strings.Contains(r.Host, "localhost") || strings.Contains(r.Host, "127.0.0.1")) {
		devURL = verifyEmailURL
	}
	s.showAuthEmailSent(w, r, email, devURL)
}

// showAuthError displays an authentication error page
func (s *Server) showAuthError(w http.ResponseWriter, r *http.Request, message string, command string) {
	data := struct {
		Message     string
		Command     string
		QueryString string
	}{
		Message:     message,
		Command:     command,
		QueryString: r.URL.RawQuery,
	}

	w.WriteHeader(http.StatusBadRequest)
	s.renderTemplate(w, "auth-error.html", data)
}

// showAuthEmailSent displays the email sent confirmation page
func (s *Server) showAuthEmailSent(w http.ResponseWriter, r *http.Request, email string, devURL string) {
	data := struct {
		Email       string
		QueryString string
		DevURL      string // Development-only URL for easy testing
	}{
		Email:       email,
		QueryString: r.URL.RawQuery,
		DevURL:      devURL,
	}

	s.renderTemplate(w, "email-sent.html", data)
}

// handleAuthCallback handles authentication callbacks with magic tokens
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	var userID string

	// Check if this is an email verification request (/auth/verify?token=...)
	if strings.HasPrefix(r.URL.Path, "/auth/verify") {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Missing token parameter", http.StatusBadRequest)
			return
		}

		// Validate email verification token
		var err error
		userID, err = s.validateEmailVerificationToken(r.Context(), token)
		if err != nil {
			slog.Info("invalid email verification token during auth callback", "error", err, "token", token, "remote_addr", r.RemoteAddr)
			http.Error(w, "Invalid or expired verification token", http.StatusUnauthorized)
			return
		}
	} else {
		// Extract token from path /auth/<token>
		token := strings.TrimPrefix(r.URL.Path, "/auth/")
		if token == "" {
			http.Error(w, "Missing authentication token", http.StatusBadRequest)
			return
		}

		// Validate the auth token
		var err error
		userID, err = s.validateAuthToken(r.Context(), token, "")
		if err != nil {
			slog.Error("Invalid auth token in callback", "error", err)
			http.Error(w, "Invalid or expired authentication token", http.StatusUnauthorized)
			return
		}
	}

	// Create main domain auth cookie
	cookieValue, err := s.createAuthCookie(context.Background(), userID, r.Host)
	if err != nil {
		slog.Error("Failed to create main auth cookie", "error", err)
		http.Error(w, "Failed to create authentication cookie", http.StatusInternalServerError)
		return
	}

	setExeAuthCookie(w, r, cookieValue)

	// Handle redirect after authentication
	s.redirectAfterAuth(w, r, userID)
}

func setExeAuthCookie(w http.ResponseWriter, r *http.Request, cookieValue string) {
	setAuthCookie(w, r, "exe-auth", cookieValue)
}

func setAuthCookie(w http.ResponseWriter, r *http.Request, domain, cookieValue string) {
	cookie := &http.Cookie{
		Name:     domain,
		Value:    cookieValue,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   30 * 24 * 60 * 60, // 30 days
		Secure:   r.TLS != nil,
	}
	http.SetCookie(w, cookie)
}

// handleAuthConfirm handles the interstitial confirmation page for magic auth
func (s *Server) handleAuthConfirm(w http.ResponseWriter, r *http.Request) {
	// Get magic secret from query parameter
	secret := r.URL.Query().Get("secret")
	if secret == "" {
		http.Error(w, "Missing secret parameter", http.StatusBadRequest)
		return
	}

	// Validate the magic secret WITHOUT consuming it (peek only)
	s.magicSecretsMu.RLock()
	magicSecret, exists := s.magicSecrets[secret]
	s.magicSecretsMu.RUnlock()

	if !exists {
		http.Error(w, "Invalid secret", http.StatusUnauthorized)
		return
	}

	// Check expiration
	if time.Now().After(magicSecret.ExpiresAt) {
		http.Error(w, "Secret expired", http.StatusUnauthorized)
		return
	}

	// Check for confirmation or cancellation
	action := r.URL.Query().Get("action")
	if action == "confirm" {
		// User confirmed, redirect to magic auth handler
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		magicURL := fmt.Sprintf("%s://%s/__exe.dev/auth?secret=%s&redirect=%s",
			scheme, r.URL.Query().Get("return_host"), secret, url.QueryEscape(magicSecret.RedirectURL))
		http.Redirect(w, r, magicURL, http.StatusTemporaryRedirect)
		return
	}
	if action == "cancel" {
		// User canceled, clean up the secret and redirect to main domain
		s.magicSecretsMu.Lock()
		delete(s.magicSecrets, secret)
		s.magicSecretsMu.Unlock()
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}

	// Show confirmation page
	returnHost := r.URL.Query().Get("return_host")
	if returnHost == "" {
		http.Error(w, "Missing return_host parameter", http.StatusBadRequest)
		return
	}

	// Extract hostname without port for display
	hostname := returnHost
	if idx := strings.LastIndex(returnHost, ":"); idx > 0 {
		hostname = returnHost[:idx]
	}

	// Parse hostname to get box name
	boxName := s.parseProxyHostname(hostname)
	if boxName == "" {
		http.Error(w, "bad proxy hostname", http.StatusBadRequest)
		return
	}

	// Get user email from database
	userEmail, err := withRxRes(s, r.Context(), func(ctx context.Context, queries *exedb.Queries) (string, error) {
		return queries.GetEmailByUserID(ctx, magicSecret.UserID)
	})
	if err != nil {
		http.Error(w, "User not found", http.StatusInternalServerError)
		return
	}

	// Prepare template data
	currentURL := r.URL.String()
	confirmURL := strings.ReplaceAll(currentURL, "action=", "unused=") + "&action=confirm"
	cancelURL := strings.ReplaceAll(currentURL, "action=", "unused=") + "&action=cancel"

	data := struct {
		SiteDomain string
		ConfirmURL string
		CancelURL  string
		UserEmail  string
	}{
		SiteDomain: hostname,
		ConfirmURL: confirmURL,
		CancelURL:  cancelURL,
		UserEmail:  userEmail,
	}

	s.renderTemplate(w, "login-confirmation.html", data)
}

// Helper functions for authentication and reverse proxy

// createAuthCookie creates a new authentication cookie for the user
func (s *Server) createAuthCookie(ctx context.Context, userID, domain string) (string, error) {
	// Generate a random cookie value
	cookieBytes := make([]byte, 32)
	if _, err := crand.Read(cookieBytes); err != nil {
		return "", fmt.Errorf("failed to generate cookie: %w", err)
	}
	cookieValue := base64.URLEncoding.EncodeToString(cookieBytes)

	// Set expiration to 30 days from now
	expiresAt := time.Now().Add(30 * 24 * time.Hour)

	// Store in database
	err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.InsertAuthCookie(ctx, exedb.InsertAuthCookieParams{
			CookieValue: cookieValue,
			UserID:      userID,
			Domain:      getDomain(domain),
			ExpiresAt:   expiresAt,
		})
	})
	if err != nil {
		return "", fmt.Errorf("failed to store auth cookie: %w", err)
	}

	return cookieValue, nil
}

// validateAuthCookie validates the primary authentication cookie and returns the user_id
func (s *Server) validateAuthCookie(r *http.Request) (string, error) {
	return s.validateNamedAuthCookie(r, "exe-auth")
}

// validateProxyAuthCookie validates the proxy authentication cookie and returns the user_id
func (s *Server) validateProxyAuthCookie(r *http.Request) (string, error) {
	return s.validateNamedAuthCookie(r, "exe-proxy-auth")
}

func (s *Server) validateNamedAuthCookie(r *http.Request, cookieName string) (string, error) {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		// NB: many callers check for errors.Is(err, http.ErrNoCookie),
		// so be sure to wrap the error returned from r.Cookie.
		return "", fmt.Errorf("failed to read %s cookie: %w", cookieName, err)
	}
	if cookie.Value == "" {
		return "", fmt.Errorf("empty %s: %w", cookieName, http.ErrNoCookie)
	}

	ctx := r.Context()
	cookieValue := cookie.Value
	domain := getDomain(r.Host)

	var userID string
	var expiresAt time.Time

	// Get auth cookie info
	if err := s.withRx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		row, err := queries.GetAuthCookieInfo(ctx, exedb.GetAuthCookieInfoParams{
			CookieValue: cookieValue,
			Domain:      domain,
		})
		if err != nil {
			return err
		}
		userID = row.UserID
		expiresAt = row.ExpiresAt
		return nil
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("invalid cookie")
		}
		return "", fmt.Errorf("database error: %w", err)
	}

	// Check if cookie has expired
	if time.Now().After(expiresAt) {
		// Clean up expired cookie - use context.Background() to ensure cleanup completes even if client disconnects
		s.withTx(context.Background(), func(ctx context.Context, queries *exedb.Queries) error {
			return queries.DeleteAuthCookie(ctx, cookieValue)
		})
		return "", fmt.Errorf("cookie expired")
	}

	// Update last used time
	s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpdateAuthCookieLastUsed(ctx, cookieValue)
	})

	return userID, nil
}

// createMagicSecret creates a temporary magic secret for proxy authentication
func (s *Server) createMagicSecret(userID, boxName, redirectURL string) (string, error) {
	// Generate a random secret
	secret := crand.Text()

	// Clean up expired secrets while we're here
	s.cleanupExpiredMagicSecrets()

	// Store in memory with 2-minute expiration
	s.magicSecretsMu.Lock()
	defer s.magicSecretsMu.Unlock()

	s.magicSecrets[secret] = &MagicSecret{
		UserID:      userID,
		BoxName:     boxName,
		RedirectURL: redirectURL,
		ExpiresAt:   time.Now().Add(2 * time.Minute),
		CreatedAt:   time.Now(),
	}

	return secret, nil
}

// validateMagicSecret validates and consumes a magic secret
func (s *Server) validateMagicSecret(secret string) (*MagicSecret, error) {
	s.magicSecretsMu.Lock()
	defer s.magicSecretsMu.Unlock()

	magicSecret, exists := s.magicSecrets[secret]
	if !exists {
		return nil, fmt.Errorf("invalid secret")
	}

	// Check expiration
	if time.Now().After(magicSecret.ExpiresAt) {
		// Clean up expired secret
		delete(s.magicSecrets, secret)
		return nil, fmt.Errorf("secret expired")
	}

	// Secret is valid, consume it (single use)
	result := *magicSecret // Copy the struct
	delete(s.magicSecrets, secret)

	return &result, nil
}

// cleanupExpiredMagicSecrets removes expired magic secrets from memory
func (s *Server) cleanupExpiredMagicSecrets() {
	s.magicSecretsMu.Lock()
	defer s.magicSecretsMu.Unlock()

	now := time.Now()
	for secret, magicSecret := range s.magicSecrets {
		if now.After(magicSecret.ExpiresAt) {
			delete(s.magicSecrets, secret)
		}
	}
}

// redirectAfterAuth handles redirecting user after successful authentication
func (s *Server) redirectAfterAuth(w http.ResponseWriter, r *http.Request, userID string) {
	// Check both URL query params (for GET) and form values (for POST)
	redirectURL := r.URL.Query().Get("redirect")
	if redirectURL == "" {
		redirectURL = r.FormValue("redirect")
	}
	returnHost := r.URL.Query().Get("return_host")
	if returnHost == "" {
		returnHost = r.FormValue("return_host")
	}

	slog.Debug("[REDIRECT] redirectAfterAuth called", "redirectURL", redirectURL, "returnHost", returnHost, "user_id", userID)

	if returnHost != "" && redirectURL != "" {
		if s.isTerminalRequest(returnHost) {
			slog.Debug("[REDIRECT] redirectAfterAuth: detected terminal request", "returnHost", returnHost)
			// Parse hostname to extract box name
			hostname := returnHost
			if idx := strings.LastIndex(returnHost, ":"); idx > 0 {
				hostname = returnHost[:idx]
			}

			boxName, err := s.parseTerminalHostname(hostname)
			if err != nil {
				slog.Error("Failed to parse terminal hostname", "hostname", hostname, "error", err)
				http.Error(w, "Invalid hostname format", http.StatusBadRequest)
				return
			}

			// Create magic secret for the terminal subdomain
			secret, err := s.createMagicSecret(userID, boxName, redirectURL)
			if err != nil {
				slog.Error("Failed to create magic secret", "error", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}

			// Redirect to terminal subdomain with magic secret
			magicURL := fmt.Sprintf("%s://%s/__exe.dev/auth?secret=%s&redirect=%s",
				getScheme(r), returnHost, secret, url.QueryEscape(redirectURL))
			http.Redirect(w, r, magicURL, http.StatusTemporaryRedirect)
			return
		} else if s.isProxyRequest(returnHost) {
			slog.Debug("[REDIRECT] redirectAfterAuth: detected proxy request", "returnHost", returnHost)
			// Parse hostname to extract box and team names
			hostname := returnHost
			if idx := strings.LastIndex(returnHost, ":"); idx > 0 {
				hostname = returnHost[:idx]
			}

			boxName := s.parseProxyHostname(hostname)
			if boxName == "" {
				slog.Info("redirectAfterAuth failed to parse proxy hostname", "hostname", hostname)
				http.Error(w, "invalid hostname format", http.StatusBadRequest)
				return
			}

			// Create magic secret for the proxy subdomain
			secret, err := s.createMagicSecret(userID, boxName, redirectURL)
			if err != nil {
				slog.Error("Failed to create magic secret", "error", err)
				http.Error(w, "Failed to create authentication secret", http.StatusInternalServerError)
				return
			}

			// Redirect to confirmation page with magic secret
			confirmURL := fmt.Sprintf("/auth/confirm?secret=%s&return_host=%s", secret, url.QueryEscape(returnHost))
			slog.Debug("[REDIRECT] redirectAfterAuth creating confirmation URL", "confirmURL", confirmURL)
			http.Redirect(w, r, confirmURL, http.StatusTemporaryRedirect)
			return
		}
	}

	// Default redirect
	if redirectURL != "" {
		http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
	} else {
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
	}
}

// handleUserDashboard renders the user dashboard page
func (s *Server) handleUserDashboard(w http.ResponseWriter, r *http.Request, userID string) {
	// Get user info
	user, err := withRxRes(s, r.Context(), func(ctx context.Context, queries *exedb.Queries) (exedb.User, error) {
		return queries.GetUserWithDetails(ctx, userID)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "User not found", http.StatusNotFound)
		} else {
			slog.Error("Failed to get user info for dashboard", "error", err, "user_id", userID)
			http.Error(w, "Failed to load user information", http.StatusInternalServerError)
		}
		return
	}

	// Get user's SSH keys
	var sshKeys []SSHKey
	err = s.withRx(r.Context(), func(ctx context.Context, queries *exedb.Queries) error {
		publicKeys, err := queries.GetSSHKeysForUser(ctx, user.UserID)
		if err != nil {
			return err
		}
		for _, publicKey := range publicKeys {
			key := SSHKey{PublicKey: publicKey}
			sshKeys = append(sshKeys, key)
		}
		return nil
	})
	if err != nil {
		slog.Error("Failed to get SSH keys for dashboard", "error", err, "email", user.Email)
	}

	// Get user's boxes
	boxResults, err := withRxRes(s, r.Context(), func(ctx context.Context, queries *exedb.Queries) ([]exedb.GetBoxesForUserDashboardRow, error) {
		return queries.GetBoxesForUserDashboard(ctx, user.UserID)
	})
	if err != nil {
		slog.Error("Failed to get boxes for dashboard", "error", err, "user_id", userID)
	}

	// Convert to BoxDisplayInfo format with additional display information
	boxes := make([]BoxDisplayInfo, len(boxResults))
	for i, result := range boxResults {
		box := exedb.Box{
			ID:              result.ID,
			AllocID:         result.AllocID,
			Name:            result.Name,
			Status:          result.Status,
			Image:           result.Image,
			CreatedByUserID: result.CreatedByUserID,
			CreatedAt:       result.CreatedAt,
			UpdatedAt:       result.UpdatedAt,
			LastStartedAt:   result.LastStartedAt,
		}
		if result.ContainerID != "" {
			box.ContainerID = &result.ContainerID
		}

		route := box.GetRoute()
		boxInfo := BoxDisplayInfo{
			Box:         box,
			SSHCommand:  s.formatSSHConnectionInfo(result.Name),
			ProxyURL:    s.httpsProxyAddress(result.Name),
			TerminalURL: s.terminalURL(result.Name),
			VSCodeURL:   s.vscodeURL(result.Name),
			ProxyPort:   route.Port,
			ProxyShare:  route.Share,
		}
		// Only set ShelleyURL for exeuntu images
		if strings.Contains(result.Image, "exeuntu") {
			boxInfo.ShelleyURL = s.shelleyURL(result.Name)
		}
		boxes[i] = boxInfo
	}
	if err != nil {
		slog.Error("Failed to get boxes for dashboard", "error", err, "user_id", userID)
	}

	// Prepare template data
	data := UserPageData{
		User:    user,
		SSHKeys: sshKeys,
		Boxes:   boxes,
	}

	// Render template
	s.renderTemplate(w, "dashboard.html", data)
}

func (s *Server) handleUserProfile(w http.ResponseWriter, r *http.Request, userID string) {
	// Get user info
	user, err := withRxRes(s, r.Context(), func(ctx context.Context, queries *exedb.Queries) (exedb.User, error) {
		return queries.GetUserWithDetails(ctx, userID)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "User not found", http.StatusNotFound)
		} else {
			slog.Error("Failed to get user info for profile", "error", err, "user_id", userID)
			http.Error(w, "Failed to load user information", http.StatusInternalServerError)
		}
		return
	}

	// Get user's SSH keys
	var sshKeys []SSHKey
	err = s.withRx(r.Context(), func(ctx context.Context, queries *exedb.Queries) error {
		publicKeys, err := queries.GetSSHKeysForUser(ctx, user.UserID)
		if err != nil {
			return err
		}
		for _, publicKey := range publicKeys {
			key := SSHKey{PublicKey: publicKey}
			sshKeys = append(sshKeys, key)
		}
		return nil
	})
	if err != nil {
		slog.Error("Failed to get SSH keys for profile", "error", err, "email", user.Email)
	}

	// Prepare template data
	data := UserPageData{
		User:    user,
		SSHKeys: sshKeys,
	}

	// Render template
	s.renderTemplate(w, "user-profile.html", data)
}

// handleLogout logs out the user by clearing their auth cookie
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Get the current user's ID from the main auth cookie
	var userID string
	if id, err := s.validateAuthCookie(r); err == nil {
		// Get the user ID before deleting
		userID = id
	}

	// Clear ALL auth cookies for this user across all domains
	if userID != "" {
		err := s.withTx(r.Context(), func(ctx context.Context, queries *exedb.Queries) error {
			return queries.DeleteAuthCookiesByUserID(ctx, userID)
		})
		if err != nil {
			slog.Error("Failed to delete user's auth cookies from database", "error", err)
		}
	}

	// Clear both cookies in the browser
	http.SetCookie(w, &http.Cookie{
		Name:     "exe-auth",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	http.SetCookie(w, &http.Cookie{
		Name:     "exe-proxy-auth",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	// Redirect to home page
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

// handleLoggedOut displays a logged out confirmation page
func (s *Server) handleLoggedOut(w http.ResponseWriter, r *http.Request) {
	data := struct {
		MainDomain string
	}{
		MainDomain: s.getMainDomain(),
	}
	_ = s.renderTemplate(w, "proxy-logged-out.html", data)
}

// getScheme returns the request scheme
func getScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
