// Package exe implements the bulk of the exed server. This file
// has any web-related stuff in it.
package execore

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
	"slices"
	"strings"
	"sync"
	"time"

	"exe.dev/boxname"
	"exe.dev/cobble"
	"exe.dev/domz"
	"exe.dev/exedb"
	"exe.dev/llmgateway"
	"exe.dev/route53"
	"exe.dev/sqlite"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
	_ "modernc.org/sqlite"
	"tailscale.com/client/tailscale"
	"tailscale.com/net/tsaddr"
)

const proxyBearerTokenTTL = 30 * 24 * time.Hour

func (s *Server) prepareHandler() http.Handler {
	lg := s.prepareLlmGateway()
	servMux := http.NewServeMux()
	servMux.Handle("/_/gateway/", lg)
	servMux.Handle("/", s)

	// Use standard promhttp instrumentation
	instrumentedHandler := promhttp.InstrumentMetricHandler(
		s.metricsRegistry,
		servMux)

	h := LoggerMiddleware(s.log)(instrumentedHandler)
	return h
}

// setupHTTPServer configures the HTTP server
func (s *Server) setupHTTPServer() {
	if s.httpLn.ln == nil {
		return
	}

	h := s.prepareHandler()

	s.httpServer = &http.Server{
		Addr:     s.httpLn.addr,
		Handler:  h,
		ErrorLog: s.netHTTPLog(),
	}
}

func (s *Server) prepareLlmGateway() http.Handler {
	anthropicAPIKey := os.Getenv("ANTHROPIC_API_KEY")
	fireworksAPIKey := os.Getenv("FIREWORKS_API_KEY")
	openaiAPIKey := os.Getenv("OPENAI_API_KEY")

	lg := llmgateway.NewGateway(s.slog(), s.db, s, llmgateway.APIKeys{
		Anthropic: anthropicAPIKey,
		Fireworks: fireworksAPIKey,
		OpenAI:    openaiAPIKey,
	}, s.env.DevMode != "")
	return lg
}

var tailscaleAcknowledgeUnstableAPI = sync.OnceFunc(func() {
	tailscale.I_Acknowledge_This_API_Is_Unstable = true
})

type slogWriter struct {
	log *slog.Logger
}

func (sw *slogWriter) Write(p []byte) (n int, err error) {
	sw.log.Info(strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}

func dedupInPlace(values []string) []string {
	slices.Sort(values)
	return slices.Compact(values)
}

// setupHTTPSServer configures the HTTPS server with Let's Encrypt if enabled
func (s *Server) setupHTTPSServer() {
	if s.httpsLn.ln == nil {
		return
	}

	s.slog().Info("set up wildcard TLS certificates with Route 53", "decision", s.env.UseRoute53, "stage", s.env.String())
	if s.env.UseRoute53 {
		wildcardDomains := []string{s.getMainDomain(), s.getMainDomain("xterm")}
		wildcardDomains = dedupInPlace(wildcardDomains)
		wildcardDomains = domz.FilterEmpty(wildcardDomains)
		s.wildcardCertManager = route53.NewWildcardCertManager(
			wildcardDomains,
			autocert.DirCache("certs"),
			s.sshMetrics.letsencryptRequests,
		)
	}

	s.certManager = &autocert.Manager{
		Cache:      autocert.DirCache("certs"),
		Prompt:     autocert.AcceptTOS,
		HostPolicy: s.validateHostForTLSCert,
	}

	if s.env.UseCobble {
		s.slog().Info("starting Pebble ACME test server for TLS certificates")
		stone, err := cobble.Start(context.Background(), &cobble.Config{
			AlwaysValid: true,
			Log:         &slogWriter{log: s.slog()},
		})
		if err != nil {
			s.slog().Error("failed to start Pebble ACME server", "error", err)
			return
		}
		s.stopCobble = stone.Stop
		s.certManager.Client = stone.Client()
	}

	// Single TLS dispatcher for all domains (exe.dev and Tailscale)
	s.httpsServer = &http.Server{
		Addr:     s.httpsLn.addr,
		Handler:  s.prepareHandler(),
		ErrorLog: s.netHTTPLog(),
		TLSConfig: &tls.Config{
			GetCertificate: s.getCertificate,
			NextProtos:     []string{"h2", "http/1.1", acme.ALPNProto},
		},
	}

	// Discover Tailscale DNS name early; certificate retrieval can happen lazily in getCertificate
	// If certs don't work, you might need to run the following in prod:
	//  sudo tailscale set --operator=$USER
	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tailscaleAcknowledgeUnstableAPI()
		lc := &tailscale.LocalClient{}
		st, err := lc.Status(ctx)
		if err != nil || st == nil || st.Self == nil || st.Self.DNSName == "" {
			if err != nil {
				s.slog().Debug("tailscale status unavailable", "error", err)
			} else {
				s.slog().Debug("tailscale DNS name not found")
			}
			return
		}
		s.tsDomain = strings.TrimSuffix(st.Self.DNSName, ".")

		// Try to eagerly fetch and cache cert, but it's optional
		certPEM, keyPEM, err := lc.CertPair(ctx, s.tsDomain)
		if err != nil {
			s.slog().Debug("tailscale cert pair not preloaded", "error", err)
			return
		}
		c, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			s.slog().Debug("tailscale x509 keypair parse error", "error", err)
			return
		}
		if len(c.Certificate) > 0 {
			if leaf, err := x509.ParseCertificate(c.Certificate[0]); err == nil {
				c.Leaf = leaf
			}
		}
		s.tsCert = &c
		s.slog().Info("tailscale cert loaded", "domain", s.tsDomain)
	}()
}

var (
	errBoxNotFound    = errors.New("box not found")
	errInvalidBoxName = errors.New("invalid box name")
)

// resolveBoxName converts a hostname to a box name.
// If hostname is a subdomain of the main domain (e.g., box.exe.dev),
// it returns the box name with the main domain suffix stripped (e.g., "box").
// In dev mode, also handles .localhost subdomains the same way.
// For all other hostname values, a CNAME lookup is performed, and the above
// rules are applied to the result; otherwise an error is returned.
func (s *Server) resolveBoxName(ctx context.Context, hostname string) (string, error) {
	hostname = domz.Canonicalize(hostname)
	if hostname == "" {
		return "", errInvalidBoxName
	}

	if hostname == s.getMainDomain() {
		return "", errInvalidBoxName
	}

	parse := func(hostname string) string {
		if host, ok := domz.CutBase(hostname, s.getMainDomain()); ok {
			return host
		}

		// In dev mode, also try localhost suffix
		if s.env.DevMode != "" {
			if host, ok := domz.CutBase(hostname, "localhost"); ok {
				return host
			}
		}

		return ""
	}

	if boxName := parse(hostname); boxName != "" {
		return boxName, nil
	}

	if !strings.Contains(hostname, ".") {
		return "", errInvalidBoxName
	}

	return s.resolveCustomDomainBoxName(ctx, hostname)
}

func (s *Server) lookupCNAME(ctx context.Context, host string) (string, error) {
	if s.lookupCNAMEFunc != nil {
		return s.lookupCNAMEFunc(ctx, host)
	}
	return net.DefaultResolver.LookupCNAME(ctx, host)
}

func (s *Server) lookupA(ctx context.Context, host string) ([]netip.Addr, error) {
	if s.lookupAFunc != nil {
		return s.lookupAFunc(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
}

// validateHostForTLSCert checks if the given host is valid for TLS certificate issuance.
func (s *Server) validateHostForTLSCert(ctx context.Context, host string) error {
	host = domz.Canonicalize(host)
	if domz.Matches(host, s.getMainDomain()) {
		return nil
	}

	boxName, err := s.resolveCustomDomainBoxName(ctx, host)
	if err != nil {
		return err
	}
	if boxName == "" {
		s.slog().WarnContext(ctx, "hostPolicy: unable to resolve box name", "host", host)
		return fmt.Errorf("unable to resolve box for %s", host)
	}
	if !s.boxExists(ctx, boxName) {
		s.slog().WarnContext(ctx, "hostPolicy: no box found for subdomain", "subdomain", host)
		return fmt.Errorf("%w: %s", errBoxNotFound, boxName)
	}
	return nil
}

// getCertificate is the single TLS certificate dispatcher for HTTPS.
// It serves:
// - Tailscale node certificate for the machine's Tailscale DNS name
// - Wildcard exe.dev certificates (via Route 53 DNS-01) when configured
// - Standard autocert for exe.dev when wildcard manager is not configured
func (s *Server) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello == nil || hello.ServerName == "" {
		return nil, fmt.Errorf("no server name provided")
	}

	serverName := domz.Canonicalize(hello.ServerName)

	// 1) Serve Tailscale certificate for exact Tailscale DNS name
	if s.tsDomain != "" && serverName == strings.ToLower(s.tsDomain) {
		if s.tsCert != nil {
			return s.tsCert, nil
		}
		return nil, fmt.Errorf("tailscale certificate not available for %s", s.tsDomain)
	}

	// 2) Main domain handling
	if domz.Matches(serverName, s.getMainDomain()) {
		if s.wildcardCertManager != nil {
			cert, err := s.wildcardCertManager.GetCertificate(hello)
			if err != nil {
				s.slog().Error("wildcard GetCertificate failed; giving up", "error", err)
			}
			return cert, err
		}

		// fall through to standard autocert for custom domains
	}

	if s.certManager == nil {
		s.slog().Error("no certificate manager configured; was https enabled at startup?", "serverName", serverName)
		return nil, fmt.Errorf("no certificate manager configured for %s", s.getMainDomain())
	}

	s.slog().Debug("getCertificate", "serverName", serverName)
	defer s.slog().Debug("getCertificate done", "serverName", serverName)

	return s.certManager.GetCertificate(hello)
}

// setupProxyServers configures additional listeners for proxy ports
func (s *Server) setupProxyServers() {
	proxyPorts := s.getProxyPorts()
	s.proxyLns = make([]*listener, 0, len(proxyPorts))

	// Create listeners for each proxy port
	for _, port := range proxyPorts {
		addr := fmt.Sprintf(":%d", port)
		ln, err := startListener(s.slog(), fmt.Sprintf("proxy-%d", port), addr)
		if err != nil {
			s.slog().Warn("Failed to listen on proxy port, skipping", "port", port, "error", err)
			continue
		}

		s.proxyLns = append(s.proxyLns, ln)
		// s.slog().Debug("proxy listener configured", "addr", ln.tcp.String(), "port", ln.tcp.Port)
	}
}

// renderTemplate is a helper method that handles template parsing and execution
func (s *Server) renderTemplate(w http.ResponseWriter, templateName string, data interface{}) error {
	w.Header().Set("Content-Type", "text/html")
	if err := s.templates.ExecuteTemplate(w, templateName, data); err != nil {
		s.slog().Error("Failed to execute template", "error", err, "template", templateName)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return err
	}

	return nil
}

// ServeHTTP implements http.Handler for the HTTP server
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.stopping.Load() {
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
		// Serve responsive page (desktop welcome, mobile new box form)
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
		if err := s.renderTemplate(w, "welcome.html", data); err != nil {
			s.log.ErrorContext(r.Context(), "failed to render welcome page", "error", err)
			return
		}
		return
	case "/alpha", "/beta":
		// Redirect aliases to the canonical welcome page
		http.Redirect(w, r, "/welcome", http.StatusTemporaryRedirect)
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
	case "/new":
		// New box creation page
		s.handleMobileNew(w, r)
		return
	case "/check-hostname":
		s.handleMobileHostnameCheck(w, r)
		return
	case "/create-vm":
		s.handleMobileCreateVM(w, r)
		return
	case "/shelley/download":
		s.handleShelleyDownload(w, r)
		return
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
		s.slog().ErrorContext(r.Context(), "failed to insert waitlist entry", "err", err)
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
	if wasNew {
		subject := "You're on the exe.dev waitlist"
		body := "Hello,\n\nThanks for your interest in exe.dev. You're on the waitlist. We'll reach out as soon as we have space for you.\n\nIn the meantime, we're heads down building a great SSH-first experience.\n\n— exe.dev"
		if sendErr := s.sendEmail(email, subject, body); sendErr != nil {
			s.slog().WarnContext(r.Context(), "failed to send waitlist email", "email", email, "err", sendErr)
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
		s.slog().ErrorContext(r.Context(), "unexpected error during device verification check", "error", err, "token", token)
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
		PublicKey:   pendingKey.PublicKey,
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
		s.slog().ErrorContext(r.Context(), "unexpected error during device verification check", "error", err, "token", token)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Add the SSH key to the verified keys and clean up pending key
	err = s.withTx(context.WithoutCancel(r.Context()), func(ctx context.Context, queries *exedb.Queries) error {
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
		s.slog().ErrorContext(r.Context(), "Failed to add SSH key", "error", err)
		http.Error(w, "Failed to verify device", http.StatusInternalServerError)
		return
	}

	// Signal completion to waiting SSH session
	close(verification.CompleteChan)
	s.deleteEmailVerification(verification)

	data := struct {
		PublicKey string
	}{
		PublicKey: pendingKey.PublicKey,
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
		// Clean up expired token - use context.WithoutCancel to ensure cleanup completes even if client disconnects
		s.withTx(context.WithoutCancel(ctx), func(ctx context.Context, queries *exedb.Queries) error {
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

// showEmailVerificationForm shows a confirmation form for email verification
func (s *Server) showEmailVerificationForm(w http.ResponseWriter, r *http.Request, token, source string) {
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
		Source      string
	}{
		Token:       token,
		RedirectURL: r.URL.Query().Get("redirect"),
		ReturnHost:  r.URL.Query().Get("return_host"),
		Email:       email,
		PairingCode: code,
		Source:      source,
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
		s.showEmailVerificationForm(w, r, token, r.URL.Query().Get("s"))
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

	// Extract source parameter (from query params or form data)
	source := r.URL.Query().Get("s")
	if source == "" {
		source = r.FormValue("source")
	}

	// First check if this is an SSH session token (in-memory)
	verification := s.lookUpEmailVerification(token)

	if verification != nil {
		// This is an SSH session email verification
		user, err := s.createUserWithSSHKey(r.Context(), verification.Email, verification.PublicKey)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "failed to create user with SSH key during email verification", "error", err, "token", token)
			http.Error(w, "failed to create user account", http.StatusInternalServerError)
			return
		}

		// Create HTTP auth cookie for this user
		cookieValue, err := s.createAuthCookie(context.WithoutCancel(r.Context()), user.UserID, r.Host)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to create auth cookie during SSH email verification", "error", err)
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
			s.slog().InfoContext(r.Context(), "invalid email verification token during verification", "error", err, "token", token, "remote_addr", r.RemoteAddr)
			http.Error(w, "Invalid or expired verification token", http.StatusNotFound)
			return
		}

		// Create HTTP auth cookie for this user
		cookieValue, err := s.createAuthCookie(context.WithoutCancel(r.Context()), userID, r.Host)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to create auth cookie during HTTP email verification", "error", err)
			http.Error(w, "Failed to create authentication session", http.StatusInternalServerError)
			return
		}

		setExeAuthCookie(w, r, cookieValue)

		// Clean up the database token (single use)
		err = s.withTx(context.WithoutCancel(r.Context()), func(ctx context.Context, queries *exedb.Queries) error {
			return queries.DeleteEmailVerificationByToken(ctx, token)
		})
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to cleanup email verification token", "error", err)
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
	data := struct {
		Source string
	}{
		Source: source,
	}
	s.renderTemplate(w, "email-verified.html", data)
}

func (s *Server) createUserWithSSHKey(ctx context.Context, email, publicKey string) (*exedb.User, error) {
	// Create the user if they don't exist
	// Note that this is called during email verification,
	// so we must look up the user by email (verified),
	// not by SSH key (which is what we are about to connect to this email).
	user, err := s.GetUserByEmail(ctx, email)
	if err != nil || user == nil {
		s.slog().InfoContext(ctx, "User doesn't exist, creating", "email", email)
		// User doesn't exist - create them with their alloc
		user, err = s.createUser(context.WithoutCancel(ctx), publicKey, email)
		if err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}
		s.slog().InfoContext(ctx, "Created new user", "email", email)
	} else {
		s.slog().DebugContext(ctx, "User already exists", "email", email)
	}

	// Store the SSH key as verified
	if publicKey != "" {
		err = s.withTx(context.WithoutCancel(ctx), func(ctx context.Context, queries *exedb.Queries) error {
			return queries.InsertSSHKeyForEmailUser(ctx, exedb.InsertSSHKeyForEmailUserParams{
				Email:     email,
				PublicKey: publicKey,
			})
		})
		if err != nil {
			s.slog().ErrorContext(ctx, "Error storing SSH key during verification", "error", err)
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

	// Check if user exists, create if not
	var userID string
	err := s.withTx(r.Context(), func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		userID, err = queries.GetUserIDByEmail(ctx, email)
		if errors.Is(err, sql.ErrNoRows) {
			// User doesn't exist, create them
			userID, err = s.createUserRecord(ctx, queries, email)
			if err != nil {
				return err
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to check user existence: %w", err)
		}
		return nil
	})
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Database error during user lookup/creation", "error", err)
		s.showAuthError(w, r, "Database error occurred. Please try again.", "")
		return
	}

	// Generate verification token
	token := generateRegistrationToken()

	// Store verification in database (reuse existing email_verifications table)
	err = s.withTx(context.WithoutCancel(r.Context()), func(ctx context.Context, queries *exedb.Queries) error {
		return queries.InsertEmailVerification(ctx, exedb.InsertEmailVerificationParams{
			Token:     token,
			Email:     email,
			UserID:    userID,
			ExpiresAt: time.Now().Add(24 * time.Hour),
		})
	})
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to store email verification", "error", err)
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
		s.slog().ErrorContext(r.Context(), "Failed to send auth email", "error", err)
		s.showAuthError(w, r, "Failed to send email. Please try again or contact support.", "")
		return
	}

	// Show success page
	var devURL string
	if s.env.DevMode != "" && (strings.Contains(r.Host, "localhost") || strings.Contains(r.Host, "127.0.0.1")) {
		devURL = verifyEmailURL
	}
	s.showAuthEmailSent(w, r, email, devURL)
}

// showAuthError displays an authentication error page
func (s *Server) showAuthError(w http.ResponseWriter, r *http.Request, message, command string) {
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
func (s *Server) showAuthEmailSent(w http.ResponseWriter, r *http.Request, email, devURL string) {
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
			s.slog().InfoContext(r.Context(), "invalid email verification token during auth callback", "error", err, "token", token, "remote_addr", r.RemoteAddr)
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
			s.slog().ErrorContext(r.Context(), "Invalid auth token in callback", "error", err)
			http.Error(w, "Invalid or expired authentication token", http.StatusUnauthorized)
			return
		}
	}

	// Create main domain auth cookie
	cookieValue, err := s.createAuthCookie(context.WithoutCancel(r.Context()), userID, r.Host)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to create main auth cookie", "error", err)
		http.Error(w, "Failed to create authentication cookie", http.StatusInternalServerError)
		return
	}

	setExeAuthCookie(w, r, cookieValue)
	s.recordUserEventBestEffort(r.Context(), userID, userEventSetBrowserCookies)

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
		http.Redirect(w, r, magicURL, http.StatusSeeOther)
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
	hostname := domz.StripPort(returnHost)
	boxName, err := s.resolveBoxName(r.Context(), hostname)
	if errors.Is(err, errInvalidBoxName) {
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}
	if err != nil {
		// TODO(bmizerany): return a nicer error page
		http.Error(w, "Failed to resolve box name", http.StatusInternalServerError)
		return
	}
	if boxName == "" {
		http.Error(w, "Invalid box name", http.StatusBadRequest)
		return
	}

	// Find the box by name. We don't check ownership here because:
	// 1. The box might be shared with the user
	// 2. We verify access rights below via hasUserAccessToBox
	box, err := withRxRes(s, r.Context(), func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxNamed(ctx, boxName)
	})
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var isOwner bool
	accessType, err := s.hasUserAccessToBox(r.Context(), magicSecret.UserID, &box)
	if err == nil && (accessType == BoxAccessOwner) {
		isOwner = true
	}

	// If user owns the box, skip confirmation and redirect directly
	if isOwner {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		magicURL := fmt.Sprintf("%s://%s/__exe.dev/auth?secret=%s&redirect=%s",
			scheme, returnHost, secret, url.QueryEscape(magicSecret.RedirectURL))
		http.Redirect(w, r, magicURL, http.StatusTemporaryRedirect)
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

// createProxyBearerToken creates a bearer token for HTTP Basic auth proxy access scoped to a box.
func (s *Server) createProxyBearerToken(ctx context.Context, userID string, boxID int) (string, error) {
	token := crand.Text()
	expiresAt := time.Now().Add(proxyBearerTokenTTL)

	err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.InsertProxyBearerToken(ctx, exedb.InsertProxyBearerTokenParams{
			Token:     token,
			UserID:    userID,
			BoxID:     int64(boxID),
			ExpiresAt: expiresAt,
		})
	})
	if err != nil {
		return "", fmt.Errorf("failed to store proxy bearer token: %w", err)
	}

	return token, nil
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
		// Clean up expired cookie - use context.WithoutCancel to ensure cleanup completes even if client disconnects
		s.withTx(context.WithoutCancel(ctx), func(ctx context.Context, queries *exedb.Queries) error {
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

// validateProxyBearerToken ensures a bearer token is valid for the provided box and returns the associated user.
func (s *Server) validateProxyBearerToken(ctx context.Context, token string, boxID int) (string, error) {
	if token == "" {
		return "", fmt.Errorf("empty proxy bearer token")
	}

	record, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.ProxyBearerToken, error) {
		return queries.GetProxyBearerToken(ctx, token)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("proxy bearer token not found")
		}
		return "", fmt.Errorf("fetching proxy bearer token: %w", err)
	}

	if record.BoxID != int64(boxID) {
		return "", fmt.Errorf("proxy bearer token is not valid for this box")
	}

	if time.Now().After(record.ExpiresAt) {
		return "", fmt.Errorf("proxy bearer token expired")
	}

	if err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpdateProxyBearerTokenLastUsed(ctx, token)
	}); err != nil {
		s.slog().WarnContext(ctx, "failed to update proxy bearer token last used", "error", err)
	}

	return record.UserID, nil
}

// userHasActiveAuthCookie returns true when the user has at least one non-expired auth cookie record.
func (s *Server) userHasActiveAuthCookie(ctx context.Context, userID string) (bool, error) {
	hasCookie, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (int64, error) {
		return queries.UserHasAuthCookie(ctx, userID)
	})
	if err != nil {
		return false, err
	}
	return hasCookie > 0, nil
}

// userHasActiveAuthCookieBestEffort logs on error and returns false when the query fails.
func (s *Server) userHasActiveAuthCookieBestEffort(ctx context.Context, userID string) bool {
	hasCookie, err := s.userHasActiveAuthCookie(ctx, userID)
	if err != nil {
		s.slog().WarnContext(ctx, "userHasActiveAuthCookie database error", "userID", userID, "error", err)
		return false
	}
	return hasCookie
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

	s.slog().DebugContext(r.Context(), "[REDIRECT] redirectAfterAuth called", "redirectURL", redirectURL, "returnHost", returnHost, "user_id", userID)

	// Check if returnHost is actually a subdomain that needs proxy/terminal auth
	// Skip the proxy/terminal flow if returnHost is the main domain itself
	shouldDoProxyFlow := returnHost != "" && redirectURL != "" && !s.isMainDomain(returnHost)

	if shouldDoProxyFlow {
		if s.isTerminalRequest(returnHost) {
			s.slog().DebugContext(r.Context(), "[REDIRECT] redirectAfterAuth: detected terminal request", "returnHost", returnHost)
			// Parse hostname to extract box name
			hostname := domz.StripPort(returnHost)

			boxName, err := s.parseTerminalHostname(hostname)
			if err != nil {
				s.slog().ErrorContext(r.Context(), "Failed to parse terminal hostname", "hostname", hostname, "error", err)
				http.Error(w, "Invalid hostname format", http.StatusBadRequest)
				return
			}

			// Create magic secret for the terminal subdomain
			secret, err := s.createMagicSecret(userID, boxName, redirectURL)
			if err != nil {
				s.slog().ErrorContext(r.Context(), "Failed to create magic secret", "error", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}

			// Redirect to terminal subdomain with magic secret
			magicURL := fmt.Sprintf("%s://%s/__exe.dev/auth?secret=%s&redirect=%s",
				getScheme(r), returnHost, secret, url.QueryEscape(redirectURL))
			http.Redirect(w, r, magicURL, http.StatusTemporaryRedirect)
			return
		} else if s.isProxyRequest(returnHost) {
			s.slog().DebugContext(r.Context(), "[REDIRECT] redirectAfterAuth: detected proxy request", "returnHost", returnHost)
			// Parse hostname to extract box name (including custom domains via CNAME)
			hostname := domz.StripPort(returnHost)

			boxName, err := s.resolveBoxName(r.Context(), hostname)
			if err != nil || boxName == "" {
				s.slog().InfoContext(r.Context(), "redirectAfterAuth failed to resolve box name", "hostname", hostname, "error", err)
				http.Error(w, "invalid hostname format", http.StatusBadRequest)
				return
			}

			// Check if user has access to the box before creating magic secret
			box, err := withRxRes(s, r.Context(), func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
				return queries.BoxNamed(ctx, boxName)
			})
			if err != nil {
				s.slog().InfoContext(r.Context(), "redirectAfterAuth box not found", "box_name", boxName, "error", err)
				http.Error(w, "box not found", http.StatusNotFound)
				return
			}

			accessType, err := s.hasUserAccessToBox(r.Context(), userID, &box)
			if err != nil {
				s.slog().ErrorContext(r.Context(), "redirectAfterAuth failed to check access", "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			if accessType == BoxAccessNone {
				// Check if there's a valid share link token in the redirect URL
				hasShareLinkAccess := false
				if parsedRedirect, err := url.Parse(redirectURL); err == nil {
					if shareToken := parsedRedirect.Query().Get("share"); shareToken != "" {
						if valid, err := s.validateShareLinkForBox(r.Context(), shareToken, box.ID); err == nil && valid {
							hasShareLinkAccess = true
							s.slog().DebugContext(r.Context(), "redirectAfterAuth: valid share link found", "box_name", boxName, "user_id", userID)
						}
					}
				}
				if !hasShareLinkAccess {
					s.slog().InfoContext(r.Context(), "redirectAfterAuth access denied", "box_name", boxName, "user_id", userID)
					// Return 404 to not leak box existence
					http.Error(w, "box not found", http.StatusNotFound)
					return
				}
			}

			// Create magic secret for the proxy subdomain
			secret, err := s.createMagicSecret(userID, boxName, redirectURL)
			if err != nil {
				s.slog().ErrorContext(r.Context(), "Failed to create magic secret", "error", err)
				http.Error(w, "Failed to create authentication secret", http.StatusInternalServerError)
				return
			}

			// Redirect to confirmation page with magic secret
			confirmURL := fmt.Sprintf("/auth/confirm?secret=%s&return_host=%s", secret, url.QueryEscape(returnHost))
			s.slog().DebugContext(r.Context(), "[REDIRECT] redirectAfterAuth creating confirmation URL", "confirmURL", confirmURL)
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
			s.slog().ErrorContext(r.Context(), "Failed to get user info for dashboard", "error", err, "user_id", userID)
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
		s.slog().ErrorContext(r.Context(), "Failed to get SSH keys for dashboard", "error", err, "email", user.Email)
	}

	// Get user's boxes
	boxResults, err := withRxRes(s, r.Context(), func(ctx context.Context, queries *exedb.Queries) ([]exedb.GetBoxesForUserDashboardRow, error) {
		return queries.GetBoxesForUserDashboard(ctx, user.UserID)
	})
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get boxes for dashboard", "error", err, "user_id", userID)
	}

	// Convert to BoxDisplayInfo format with additional display information
	boxes := make([]BoxDisplayInfo, len(boxResults))
	for i, result := range boxResults {
		box := exedb.Box{
			ID:              result.ID,
			CreatedByUserID: result.CreatedByUserID,
			Name:            result.Name,
			Status:          result.Status,
			Image:           result.Image,
			CreatedAt:       result.CreatedAt,
			UpdatedAt:       result.UpdatedAt,
			LastStartedAt:   result.LastStartedAt,
		}
		if result.ContainerID != "" {
			box.ContainerID = &result.ContainerID
		}
		if result.CreationLog != "" {
			box.CreationLog = &result.CreationLog
		}

		route := box.GetRoute()
		// Get sharing information
		sharedUserCount, shareLinkCount, err := s.countTotalShares(r.Context(), box.ID)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to count shares for dashboard", "error", err, "box_id", box.ID, "box_name", result.Name)
			http.Error(w, "Failed to load sharing information", http.StatusInternalServerError)
			return
		}
		sharedEmails := s.getSharedEmails(r.Context(), box.ID)
		shareLinks := s.getShareLinks(r.Context(), box.ID, result.Name, user.UserID)

		boxInfo := BoxDisplayInfo{
			Box:             box,
			SSHCommand:      s.boxSSHConnectionCommand(result.Name),
			ProxyURL:        s.httpsProxyAddress(result.Name),
			TerminalURL:     s.terminalURL(result.Name),
			VSCodeURL:       template.URL(s.vscodeURL(result.Name)),
			ProxyPort:       route.Port,
			ProxyShare:      route.Share,
			SharedUserCount: sharedUserCount,
			ShareLinkCount:  shareLinkCount,
			TotalShareCount: sharedUserCount + shareLinkCount,
			SharedEmails:    sharedEmails,
			ShareLinks:      shareLinks,
		}
		// Only set ShelleyURL for exeuntu images
		if strings.Contains(result.Image, "exeuntu") {
			boxInfo.ShelleyURL = s.shelleyURL(result.Name)
		}
		boxes[i] = boxInfo
	}
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get boxes for dashboard", "error", err, "user_id", userID)
	}

	// Get boxes shared with this user
	sharedBoxResults, err := withRxRes(s, r.Context(), func(ctx context.Context, queries *exedb.Queries) ([]exedb.GetBoxesSharedWithUserRow, error) {
		return queries.GetBoxesSharedWithUser(ctx, user.UserID)
	})
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get shared boxes for dashboard", "error", err, "user_id", userID)
	}

	// Convert shared boxes to SharedBoxDisplayInfo
	sharedBoxes := make([]SharedBoxDisplayInfo, len(sharedBoxResults))
	for i, result := range sharedBoxResults {
		sharedBoxInfo := SharedBoxDisplayInfo{
			Name:       result.Name,
			OwnerEmail: result.OwnerEmail,
			ProxyURL:   s.httpsProxyAddress(result.Name),
		}
		sharedBoxes[i] = sharedBoxInfo
	} // Prepare template data
	data := UserPageData{
		User:        user,
		SSHKeys:     sshKeys,
		Boxes:       boxes,
		SharedBoxes: sharedBoxes,
		ActivePage:  "boxes",
		IsLoggedIn:  true,
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
			s.slog().ErrorContext(r.Context(), "Failed to get user info for profile", "error", err, "user_id", userID)
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
		s.slog().ErrorContext(r.Context(), "Failed to get SSH keys for profile", "error", err, "email", user.Email)
	}

	// Prepare template data
	data := UserPageData{
		User:       user,
		SSHKeys:    sshKeys,
		ActivePage: "profile",
		IsLoggedIn: true,
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
			s.slog().ErrorContext(r.Context(), "Failed to delete user's auth cookies from database", "error", err)
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

// isMainDomain checks if the given host (with optional port) is the main domain
func (s *Server) isMainDomain(host string) bool {
	hostname := domz.StripPort(host)
	mainDomain := s.getMainDomain()

	// Check if it's exactly the main domain or www subdomain
	return hostname == mainDomain || hostname == "www."+mainDomain ||
		(s.env.DevMode != "" && (hostname == "localhost" || hostname == "exe.local"))
}
