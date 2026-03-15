// Package exe implements the bulk of the exed server. This file
// has any web-related stuff in it.
package execore

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"exe.dev/billing"
	"exe.dev/billing/tender"
	"exe.dev/boxname"
	"exe.dev/cobble"
	"exe.dev/domz"
	"exe.dev/exedb"
	"exe.dev/exedebug"
	"exe.dev/exeweb"
	"exe.dev/llmgateway"
	"exe.dev/metricsbag"
	storageapi "exe.dev/pkg/api/exe/storage/v1"
	"exe.dev/sshkey"
	"exe.dev/stage"
	"exe.dev/tracing"
	"exe.dev/webstatic"
	"exe.dev/wildcardcert"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	sloghttp "github.com/samber/slog-http"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
	_ "modernc.org/sqlite"
	"tailscale.com/client/local"
	"tailscale.com/client/tailscale"
)

func (s *Server) prepareHandler() http.Handler {
	lg := s.prepareLlmGateway()

	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "cross-origin request denied", http.StatusForbidden)
	}))
	// The /verify-email link is clicked from email (cross-origin navigation)
	// and the page auto-submits a confirmation form via JS. Browsers propagate
	// the cross-site Sec-Fetch-Site context to this POST. Safe to allow:
	// it only confirms an already-issued, single-use token.
	cop.AddInsecureBypassPattern("POST /verify-email")
	cop.AddInsecureBypassPattern("POST /auth/verify-code")

	servMux := http.NewServeMux()
	servMux.Handle("/_/gateway/", lg)
	servMux.HandleFunc("POST /_/gateway/email/send", s.handleVMEmailSend)
	servMux.HandleFunc("GET /_/integration-config", s.handleIntegrationConfig)
	servMux.HandleFunc("GET /_/integration-cert", s.handleIntegrationCert)
	servMux.Handle("/", cop.Handler(s))

	h := s.httpMetrics.Wrap(servMux)
	h = metricsbag.Wrap(h)
	h = exeweb.HSTSMiddleware(h)
	h = LoggerMiddleware(s.log)(h)
	h = RecoverHTTPMiddleware(s.log)(h)
	return h
}

// setupHTTPServer configures the HTTP server.
func (s *Server) setupHTTPServer() {
	if s.httpLn.ln == nil {
		return
	}
	var h http.Handler
	if s.env.RedirectHTTPToHTTPS && s.servingHTTPS() {
		// Redirect all HTTP traffic to HTTPS.
		h = s.httpToHTTPSHandler()
	} else {
		h = s.prepareHandler()
	}
	s.httpServer = &http.Server{
		Addr:     s.httpLn.addr,
		Handler:  h,
		ErrorLog: s.netHTTPLog(),
	}
}

// httpToHTTPSHandler returns an HTTP handler that redirects all requests to HTTPS.
func (s *Server) httpToHTTPSHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if port := s.httpsLn.tcp.Port; port != 443 {
			host = net.JoinHostPort(host, strconv.Itoa(port))
		}
		target := "https://" + host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}

func (s *Server) prepareLlmGateway() http.Handler {
	anthropicAPIKey := os.Getenv("ANTHROPIC_API_KEY")
	fireworksAPIKey := os.Getenv("FIREWORKS_API_KEY")
	openaiAPIKey := os.Getenv("OPENAI_API_KEY")

	lg := llmgateway.NewGateway(s.slog(), &llmgateway.DBGatewayData{DB: s.db}, llmgateway.APIKeys{
		Anthropic: anthropicAPIKey,
		Fireworks: fireworksAPIKey,
		OpenAI:    openaiAPIKey,
	}, s.env)
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

	// Set up wildcard certificate manager for BoxHost (exe.xyz) using DNS-01 challenges
	// This requires the embedded DNS server (exens) to be running
	if s.dnsServer != nil {
		wildcardDomains := []string{s.env.BoxHost, s.env.BoxSub("xterm"), s.env.BoxSub("shelley"), s.env.BoxSub("int")}
		wildcardDomains = dedupInPlace(wildcardDomains)
		wildcardDomains = domz.FilterEmpty(wildcardDomains)
		s.wildcardCertManager = wildcardcert.NewManager(
			wildcardDomains,
			autocert.DirCache("certs"),
			s.sshMetrics.letsencryptRequests,
			s.dnsServer,
		)
		s.slog().Info("wildcard certificate manager initialized", "domains", wildcardDomains)
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
		// ConnContext adds a trace_id to the connection context, which becomes
		// the parent context for all requests on this connection. This ensures
		// the same trace_id is used for TLS handshake logging and subsequent
		// HTTP request logging.
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			traceID := tracing.GenerateTraceID()
			return tracing.ContextWithTraceID(ctx, traceID)
		},
	}

	// Discover Tailscale DNS name early; certificate retrieval can happen lazily in getCertificate
	// If certs don't work, you might need to run the following in prod:
	//  sudo tailscale set --operator=$USER
	if s.env.PreloadTailscaleCert {
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tailscaleAcknowledgeUnstableAPI()
			lc := new(local.Client)
			st, err := lc.Status(ctx)
			if err != nil || st == nil || st.Self == nil || st.Self.DNSName == "" {
				if err != nil {
					s.slog().ErrorContext(ctx, "tailscale status unavailable", "error", err)
				} else {
					s.slog().ErrorContext(ctx, "tailscale DNS name not found")
				}
				return
			}
			s.tsDomain = domz.Canonicalize(st.Self.DNSName)

			// Try to eagerly fetch and cache cert, but it's optional
			certPEM, keyPEM, err := lc.CertPair(ctx, s.tsDomain)
			if err != nil {
				s.slog().ErrorContext(ctx, "tailscale cert pair not preloaded", "error", err)
				return
			}
			c, err := tls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				s.slog().ErrorContext(ctx, "tailscale x509 keypair parse error", "error", err)
				return
			}
			if len(c.Certificate) > 0 {
				if leaf, err := x509.ParseCertificate(c.Certificate[0]); err == nil {
					c.Leaf = leaf
				}
			}
			s.tsCertMu.Lock()
			s.tsCert = &c
			s.tsCertMu.Unlock()
			s.slog().InfoContext(ctx, "tailscale cert loaded", "domain", s.tsDomain)
		}()
	}
}

// validateHostForTLSCert checks if the given host is valid for TLS certificate issuance.
func (s *Server) validateHostForTLSCert(ctx context.Context, host string) error {
	return s.proxyServer().ValidateHostForTLSCert(ctx, host)
}

// getCertificate is the single TLS certificate dispatcher for HTTPS.
// It serves:
// - Tailscale node certificate for the machine's Tailscale DNS name
// - Wildcard certificates for BoxHost (exe.xyz) via DNS-01 challenges
// - Standard autocert (TLS-ALPN-01) for WebHost (exe.dev) and custom domains
func (s *Server) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello == nil || hello.ServerName == "" {
		return nil, fmt.Errorf("no server name provided")
	}

	serverName := domz.Canonicalize(hello.ServerName)

	// 1) Serve Tailscale certificate for exact Tailscale DNS name
	if s.tsDomain != "" && serverName == s.tsDomain {
		cert, err := s.tailscaleCertificate()
		if err != nil {
			return nil, fmt.Errorf("tailscale certificate not available for %s: %w", s.tsDomain, err)
		}
		return cert, nil
	}

	// 2) BoxHost (exe.xyz) uses wildcard certs via DNS-01
	if domz.FirstMatch(serverName, s.env.BoxHost) != "" {
		if s.wildcardCertManager != nil {
			cert, err := s.wildcardCertManager.GetCertificate(serverName)
			if errors.Is(err, wildcardcert.ErrUnrecognizedDomain) {
				s.slog().DebugContext(hello.Context(), "wildcard GetCertificate rejected unrecognized domain", "error", err)
			} else if err != nil {
				s.slog().ErrorContext(hello.Context(), "wildcard GetCertificate failed; giving up", "error", err)
			}
			return cert, err
		}
		// fall through to standard autocert if no wildcard manager
	}

	// 3) WebHost (exe.dev) and custom domains use standard autocert (TLS-ALPN-01)
	if s.certManager == nil {
		s.slog().ErrorContext(hello.Context(), "no certificate manager configured; was https enabled at startup?", "serverName", serverName)
		return nil, fmt.Errorf("no certificate manager configured for %s", serverName)
	}

	cert, err := s.certManager.GetCertificate(hello)
	if err != nil {
		s.slog().WarnContext(hello.Context(), "getting certificate failed", "serverName", hello.ServerName, "error", err)
	}

	return cert, err
}

func (s *Server) tailscaleCertificate() (*tls.Certificate, error) {
	if s.tsDomain == "" {
		return nil, fmt.Errorf("tailscale domain not configured")
	}

	s.tsCertMu.Lock()
	defer s.tsCertMu.Unlock()
	if s.tsCert != nil {
		return s.tsCert, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tailscaleAcknowledgeUnstableAPI()
	lc := &tailscale.LocalClient{}
	certPEM, keyPEM, err := lc.CertPair(ctx, s.tsDomain)
	if err != nil {
		return nil, fmt.Errorf("tailscale cert pair not available: %w", err)
	}

	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("tailscale x509 keypair parse error: %w", err)
	}
	if len(c.Certificate) > 0 {
		if leaf, err := x509.ParseCertificate(c.Certificate[0]); err == nil {
			c.Leaf = leaf
		}
	}
	s.tsCert = &c
	s.slog().InfoContext(ctx, "tailscale cert loaded", "domain", s.tsDomain)

	return s.tsCert, nil
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
	// Log the ports. For small numbers of ports, list them explicitly (for e1e tests).
	// For large numbers, show range (it's always contiguous in production).
	if len(s.proxyLns) <= 10 {
		ports := make([]int, len(s.proxyLns))
		for i, ln := range s.proxyLns {
			ports[i] = ln.tcp.Port
		}
		s.slog().Info("proxy listeners set up", "count", len(s.proxyLns), "ports", ports)
	} else {
		s.slog().Info("proxy listeners set up", "count", len(s.proxyLns),
			"min_port", s.proxyLns[0].tcp.Port,
			"max_port", s.proxyLns[len(s.proxyLns)-1].tcp.Port)
	}
}

// renderTemplate is a helper method that handles template parsing and execution
func (s *Server) renderTemplate(ctx context.Context, w http.ResponseWriter, templateName string, data any) error {
	w.Header().Set("Content-Type", "text/html")
	var buf bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		s.slog().ErrorContext(ctx, "Failed to execute template", "error", err, "template", templateName)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// isRequestOnMainPort checks that the request came in on the main HTTP/HTTPS port.
// Returns true if the request should continue processing, false if an error response was sent.
// Non-proxy content (main website, xterm, etc) should only be served on the main port.
// Checks both the actual connection port and the Host header port.
func (s *Server) isRequestOnMainPort(w http.ResponseWriter, r *http.Request) bool {
	// Check the actual local address the request came in on from the context.
	conn, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if ok && conn != nil {
		_, localPortStr, err := net.SplitHostPort(conn.String())
		if err != nil {
			s.slog().ErrorContext(r.Context(), "failed to parse local address", "error", err, "addr", conn.String())
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return false
		}
		localPort, err := strconv.Atoi(localPortStr)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "failed to convert local port", "error", err, "port", localPortStr)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return false
		}
		if !s.isMainListenerPort(localPort) {
			http.Error(w, "404 page not found", http.StatusNotFound)
			return false
		}
	}

	// Also check the Host header if it contains a port.
	_, hostPortStr, err := net.SplitHostPort(r.Host)
	if err == nil && hostPortStr != "" {
		// Host header has an explicit port - verify it matches main port.
		hostPort, err := strconv.Atoi(hostPortStr)
		if err != nil {
			http.Error(w, "invalid port in host header", http.StatusBadRequest)
			return false
		}
		if !s.isMainListenerPort(hostPort) {
			http.Error(w, "404 page not found", http.StatusNotFound)
			return false
		}
	}
	// No port in Host header is fine - browsers don't send port for 80/443.

	return true
}

// ServeHTTP implements http.Handler for the HTTP server
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.stopping.Load() {
		http.Error(w, "Server is shutting down", http.StatusServiceUnavailable)
		return
	}

	if target := exeweb.NonProxyRedirect(&s.env, r); target != "" {
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		return
	}

	// docs.exe.dev → exe.dev/docs (permanent redirect)
	if domz.Canonicalize(domz.StripPort(r.Host)) == "docs."+s.env.WebHost {
		target := "https://" + s.env.WebHost + "/docs" + r.URL.Path
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}

	// Check if this should be handled by the proxy handler.
	// Shelley subdomain (vm.shelley.exe.xyz) is also handled as a proxy request.
	isProxy := s.isProxyRequest(r.Host)
	isTerminal := s.isTerminalRequest(r.Host)

	// Add request classification to logs
	if isProxy {
		sloghttp.AddCustomAttributes(r, slog.Bool("proxy", true))
		sloghttp.AddCustomAttributes(r, slog.String("request_type", "proxy"))
	} else if isTerminal {
		sloghttp.AddCustomAttributes(r, slog.Bool("terminal", true))
		sloghttp.AddCustomAttributes(r, slog.String("request_type", "terminal"))
	} else {
		sloghttp.AddCustomAttributes(r, slog.String("request_type", "web"))
	}

	// Try to get userID from auth cookie for logging and tracking
	var loggedUserID string
	if userID, err := s.validateAuthCookie(r); err == nil {
		loggedUserID = userID
		sloghttp.AddCustomAttributes(r, slog.String("user_id", userID))
	}

	if (isProxy || isTerminal) && s.env.ExedWarnProxy {
		s.slog().WarnContext(r.Context(), "exed saw proxy/terminal request that should have gone to exeprox", "host", r.Host, "isProxy", isProxy, "isTerminal", isTerminal, "userID", loggedUserID)
	}

	if isProxy {
		metricsbag.SetLabel(r.Context(), exeweb.LabelProxy, "true")
		// box label is set in handleProxyRequest after resolving the box name
		s.handleProxyRequest(w, r)
		return
	}

	// /exelet-desired must be routed before isRequestOnMainPort because
	// exelets reach exed through a TCP proxy whose port differs from
	// exed's main listener port, causing isRequestOnMainPort to reject
	// the request. Access is already restricted by requireLocalAccess.
	if r.URL.Path == "/exelet-desired" {
		exedebug.RequireLocalAccess(http.HandlerFunc(s.handleExeletDesired)).ServeHTTP(w, r)
		return
	}

	// Non-proxy content (main site, terminal) should only be served on the main port.
	if !s.isRequestOnMainPort(w, r) {
		return
	}

	if isTerminal {
		metricsbag.SetLabel(r.Context(), exeweb.LabelProxy, "false")
		metricsbag.SetLabel(r.Context(), exeweb.LabelPath, "/terminal")
		s.handleTerminalRequest(w, r)
		return
	}

	// Set labels for non-proxy HTTP metrics
	metricsbag.SetLabel(r.Context(), exeweb.LabelProxy, "false")
	metricsbag.SetLabel(r.Context(), exeweb.LabelPath, exeweb.SanitizePath(r.URL.Path))

	// Track unique web visitors (main site only, not proxy or terminal)
	if s.hllTracker != nil && loggedUserID != "" {
		s.hllTracker.NoteEvent("web-visit", loggedUserID)
	}

	// Handle root path and user dashboard
	path := r.URL.Path
	// Debug endpoints (pprof, expvar), gated by localhost or Tailscale access
	if strings.HasPrefix(path, "/debug") {
		exedebug.RequireLocalAccess(http.HandlerFunc(s.handleDebug)).ServeHTTP(w, r)
		return
	} else if strings.HasPrefix(path, "/docs") || path == "/llms.txt" || path == "/llms-full.txt" || path == "/docs.md" {
		if s.docs != nil && s.docs.Handle(w, r) {
			return
		}
	} else if path == "/security" || strings.HasPrefix(path, "/security/") {
		if s.security != nil && s.security.Handle(w, r) {
			return
		}
	} else if key, ok := isRedirectRequest(path); ok {
		s.handleRedirect(w, r, key)
		return
	}

	switch path {
	case "/":
		// Easter egg: curl the homepage and get a shell script
		if strings.HasPrefix(r.UserAgent(), "curl/") {
			w.Header().Set("Content-Type", "text/x-shellscript")
			// </dev/tty redirects stdin from the terminal (not the pipe)
			fmt.Fprintf(w, "#!/bin/sh\n%s </dev/tty\n", s.replSSHConnectionCommand())
			return
		}
		// If authenticated, show user dashboard; otherwise show index page
		if userID, err := s.validateAuthCookie(r); err == nil {
			s.handleUserDashboard(w, r, userID)
			return
		}
		hostnameSuggestion := boxname.Random()
		data := struct {
			stage.Env
			SSHCommand         string
			HostnameSuggestion string
			IsLoggedIn         bool
			ActivePage         string
			Testimonials       []Testimonial
		}{
			Env:                s.env,
			SSHCommand:         s.replSSHConnectionCommand(),
			HostnameSuggestion: hostnameSuggestion,
			IsLoggedIn:         false,
			ActivePage:         "",
			Testimonials:       ApprovedTestimonials(),
		}
		if err := s.renderTemplate(r.Context(), w, "index.html", data); err != nil {
			return
		}
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
	case "/invite":
		// Invite allocation page - require authentication, POST to allocate
		userID, err := s.validateAuthCookie(r)
		if err != nil {
			authURL := fmt.Sprintf("/auth?redirect=%s", url.QueryEscape(r.URL.String()))
			http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
			return
		}
		s.handleInvite(w, r, userID)
		return
	case "/invite/request":
		// Request more invites - require authentication
		userID, err := s.validateAuthCookie(r)
		if err != nil {
			authURL := fmt.Sprintf("/auth?redirect=%s", url.QueryEscape(r.URL.String()))
			http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
			return
		}
		s.handleInviteRequest(w, r, userID)
		return
	case "/team/invite/accept":
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		userID, err := s.validateAuthCookie(r)
		if err != nil {
			authURL := fmt.Sprintf("/auth?redirect=%s", url.QueryEscape("/user"))
			http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
			return
		}
		s.handleTeamInviteAccept(w, r, userID)
		return
	case "/team/invite/decline":
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		userID, err := s.validateAuthCookie(r)
		if err != nil {
			authURL := fmt.Sprintf("/auth?redirect=%s", url.QueryEscape("/user"))
			http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
			return
		}
		s.handleTeamInviteDecline(w, r, userID)
		return
	case "/health":
		s.handleHealth(w, r)
	case "/pull-exeuntu-everywhere-517c8a904":
		s.handlePullExeuntuEverywhere(w, r)
	case "/clear-exeuntu-latest-cache-517c8a904":
		s.handleClearExeuntuLatestCache(w, r)
	case "/update-exelet-usage-517c8a904":
		s.updateAllExeletUsage(r.Context())
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok")
	case "/metrics":
		exedebug.RequireLocalAccess(http.HandlerFunc(s.handleMetrics)).ServeHTTP(w, r)
	case exeweb.SSHKnownHostsPath:
		s.handleKnownHosts(w, r)
		return
	case "/sitemap.xml":
		s.handleSitemap(w, r)
	case "/robots.txt":
		s.handleRobots(w, r)
	case "/blog":
		http.Redirect(w, r, "https://blog.exe.dev", http.StatusTemporaryRedirect)
		return
	case "/about":
		http.Redirect(w, r, "/docs/what-is-exe", http.StatusTemporaryRedirect)
		return
	case "/pricing":
		http.Redirect(w, r, "/docs/pricing", http.StatusTemporaryRedirect)
		return
	case "/love":
		s.handleLovePage(w, r)
	case "/jobs":
		s.serveStaticFile(w, r, "jobs.html")
	case "/presskit":
		s.serveStaticFile(w, r, "presskit.html")
	case "/shelley":
		s.serveStaticFile(w, r, "shelley.html")
	case "/verify-email":
		s.handleEmailVerificationHTTP(w, r)
	case "/verify-device":
		s.handleDeviceVerificationHTTP(w, r)
	case "/billing/update":
		s.handleBillingUpdate(w, r)
	case "/billing/success":
		s.handleBillingSuccess(w, r)
	case "/credits/buy":
		s.handleCreditsBuy(w, r)
	case "/credits/success":
		s.handleCreditsSuccess(w, r)
	case "/take-my-money":
		http.Redirect(w, r, "/billing/update", http.StatusMovedPermanently)
	case "/auth":
		s.handleAuth(w, r)
	case "/auth/verify-code":
		s.handleAppTokenVerifyCode(w, r)
	case "/auth/confirm":
		s.handleAuthConfirm(w, r)
	case "/oauth/google/callback":
		s.handleOAuthGoogleCallback(w, r)
	case "/oauth/oidc/callback":
		s.handleOAuthOIDCCallback(w, r)
	case "/oauth/oidc/login":
		s.handleOAuthOIDCLogin(w, r)
	case "/newsletter-subscribe":
		s.handleNewsletterSubscribe(w, r)
	case "/link-discord":
		s.handleLinkDiscord(w, r)
	case "/github/callback":
		s.handleGitHubCallback(w, r)

	case "/logout":
		s.handleLogout(w, r)
	case "/logged-out":
		s.handleLoggedOut(w, r)
	case "/shell":
		s.handleWebShell(w, r)
	case "/shell/ws":
		s.handleWebShellWS(w, r)
	case "/new":
		s.handleNewBox(w, r)
		return
	case "/check-hostname":
		s.handleHostnameCheck(w, r)
		return
	case "/create-vm":
		s.handleCreateVM(w, r)
		return
	case "/creating/stream":
		s.handleCreatingStream(w, r)
		return
	case "/box/creation-log":
		s.handleBoxCreationLog(w, r)
		return
	case "/cmd":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleRunCommand(w, r)
		return
	case "/shelley/download":
		s.handleShelleyDownload(w, r)
		return
	case "/exec":
		s.handleExec(w, r)
		return
	case "/api/ideas":
		s.handleTemplatesAPI(w, r)
		return
	case "/api/ideas/rate":
		s.handleTemplateRateAPI(w, r)
		return
	case "/api/ideas/my-ratings":
		s.handleMyRatingsAPI(w, r)
		return
	case "/api/ideas/submit":
		s.handleTemplateSubmitAPI(w, r)
		return
	case "/idea":
		s.handleIdeaPage(w, r)
		return
	case "/ideas":
		http.Redirect(w, r, "/idea", http.StatusMovedPermanently)
		return
	default:
		// /idea/<slug> is the idea page with a specific idea pre-opened
		if _, ok := strings.CutPrefix(path, "/idea/"); ok {
			s.handleIdeaPage(w, r)
			return
		}
		// /new/<shortname> is equivalent to /new?idea=<shortname>
		if shortname, ok := strings.CutPrefix(path, "/new/"); ok && shortname != "" {
			q := r.URL.Query()
			if q.Get("idea") == "" {
				q.Set("idea", shortname)
				r.URL.RawQuery = q.Encode()
			}
			s.handleNewBox(w, r)
			return
		}

		if strings.HasPrefix(path, "/auth/") {
			s.handleAuthCallback(w, r)
			return
		}

		// Handle passkey routes
		if strings.HasPrefix(path, "/passkey/") {
			s.handlePasskeyRoutes(w, r)
			return
		}

		// Serve embedded static assets under /static/
		if filename, ok := strings.CutPrefix(path, "/static/"); ok {
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

// serveStaticFile serves a file from the embedded static directory.
// Uses the binary's VCS build time as the modification time to enable HTTP caching.
func (s *Server) serveStaticFile(w http.ResponseWriter, r *http.Request, filename string) {
	webstatic.Serve(w, r, s.slog(), filename)
}

// handleHealth handles health check requests
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","timestamp":"%s"}`, time.Now().Format(time.RFC3339))
}

// handleLovePage serves the /love page with testimonials.
func (s *Server) handleLovePage(w http.ResponseWriter, r *http.Request) {
	approved := ApprovedTestimonials()
	rand.Shuffle(len(approved), func(i, j int) {
		approved[i], approved[j] = approved[j], approved[i]
	})
	data := struct {
		Testimonials []Testimonial
	}{
		Testimonials: approved,
	}
	if err := s.renderTemplate(r.Context(), w, "love.html", data); err != nil {
		return
	}
}

// handleSitemap serves the sitemap.xml for search engines.
func (s *Server) handleSitemap(w http.ResponseWriter, _ *http.Request) {
	baseURL := "https://" + s.env.WebHost

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url>
    <loc>`)
	fmt.Fprint(w, baseURL)
	fmt.Fprint(w, `/</loc>
  </url>
`)

	if s.docs != nil {
		store := s.docs.Store()
		if store != nil {
			for _, entry := range store.Entries() {
				fmt.Fprint(w, `  <url>
    <loc>`)
				fmt.Fprint(w, baseURL)
				fmt.Fprint(w, entry.Path)
				fmt.Fprint(w, `</loc>
  </url>
`)
			}
		}
	}

	fmt.Fprint(w, `</urlset>
`)
}

// handleRobots serves robots.txt for search engine crawlers.
func (s *Server) handleRobots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "User-agent: *\n")
	fmt.Fprint(w, "Allow: /\n")
	fmt.Fprint(w, "\n")
	fmt.Fprintf(w, "Sitemap: https://%s/sitemap.xml\n", s.env.WebHost)
}

// handleMetrics serves Prometheus metrics, gated by localhost or Tailscale access
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	handler := promhttp.HandlerFor(s.metricsRegistry, promhttp.HandlerOpts{})
	handler.ServeHTTP(w, r)
}

// handleKnownHosts exposes the SSH CA line for clients to trust the host certificate.
func (s *Server) handleKnownHosts(w http.ResponseWriter, r *http.Request) {
	host := domz.Canonicalize(domz.StripPort(r.Host))
	switch host {
	case s.env.ReplHost, s.env.BoxHost:
		// ok
	case "":
		http.Error(w, "missing host header", http.StatusBadRequest)
		return
	default:
		http.NotFound(w, r)
		return
	}

	line, err := s.knownHostsLine(r.Context(), host)
	if err != nil {
		s.log.ErrorContext(r.Context(), "failed to render known hosts entry", "error", err)
		http.Error(w, "ssh host certificate unavailable", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	fmt.Fprintln(w, line)
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
		stage.Env
		SSHCommand  string
		Email       string
		PublicKey   string
		Token       string
		PairingCode string
	}{
		Env:         s.env,
		SSHCommand:  s.replSSHConnectionCommand(),
		Email:       pendingKey.UserEmail,
		PublicKey:   pendingKey.PublicKey,
		Token:       token,
		PairingCode: verification.PairingCode,
	}

	s.renderTemplate(r.Context(), w, "device-verification.html", data)
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

	// Add the SSH key to the verified keys and clean up pending key.
	// Generate a key-N comment since SSH key canonicalization strips comments.
	// Use InsertSSHKeyForEmailUserIfNotExists to handle concurrent requests gracefully
	// (e.g., user double-clicking the verification link).
	err = s.withTx(context.WithoutCancel(r.Context()), func(ctx context.Context, queries *exedb.Queries) error {
		fingerprint, err := sshkey.Fingerprint(pendingKey.PublicKey)
		if err != nil {
			return fmt.Errorf("failed to parse public key: %w", err)
		}
		canonicalEmail, err := canonicalEmailPtr(pendingKey.UserEmail)
		if err != nil {
			return fmt.Errorf("invalid email: %w", err)
		}
		userID, err := queries.GetUserIDByEmail(ctx, canonicalEmail)
		if err != nil {
			return fmt.Errorf("failed to get user ID: %w", err)
		}
		// Check if user is locked out - don't add SSH key for locked out users
		isLockedOut, err := queries.GetUserIsLockedOut(ctx, userID)
		if err != nil {
			return fmt.Errorf("failed to check lockout status: %w", err)
		}
		if isLockedOut {
			return errUserLockedOut
		}
		comment, err := nextSSHKeyComment(ctx, queries, userID)
		if err != nil {
			return fmt.Errorf("failed to generate SSH key comment: %w", err)
		}
		result, err := queries.InsertSSHKeyForEmailUserIfNotExists(ctx, exedb.InsertSSHKeyForEmailUserIfNotExistsParams{
			Email:       pendingKey.UserEmail,
			PublicKey:   pendingKey.PublicKey,
			Comment:     comment,
			Fingerprint: fingerprint,
		})
		if err != nil {
			return err
		}

		// Security check: if the key already exists, verify it belongs to this user.
		// This prevents a TOCTOU attack where two users race to register the same key.
		rowsAffected, _ := result.RowsAffected()
		if rowsAffected == 0 {
			existingUserID, err := queries.GetUserIDBySSHKey(ctx, pendingKey.PublicKey)
			if err != nil {
				return fmt.Errorf("failed to verify key ownership: %w", err)
			}
			if existingUserID != userID {
				return errKeyBelongsToOther
			}
			// Key already belongs to this user (double-click scenario) - that's fine
		}

		// Clean up the pending key
		return queries.DeletePendingSSHKeyByToken(ctx, token)
	})
	if err != nil {
		if errors.Is(err, errUserLockedOut) {
			// Signal failure to waiting SSH session
			verification.Err = fmt.Errorf("account locked")
			verification.Close()
			s.deleteEmailVerification(verification)
			// Show lockout message
			traceID := tracing.TraceIDFromContext(r.Context())
			s.slog().WarnContext(r.Context(), "locked out user attempted device verification", "email", pendingKey.UserEmail, "trace_id", traceID)
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, "contact support@exe.dev (trace: %s)", traceID)
			return
		}
		if !errors.Is(err, errKeyBelongsToOther) {
			s.slog().ErrorContext(r.Context(), "Failed to add SSH key", "error", err)
		}
		http.Error(w, "Failed to verify device", http.StatusInternalServerError)
		return
	}

	// Signal completion to waiting SSH session
	verification.Close()
	s.deleteEmailVerification(verification)

	data := struct {
		stage.Env
		SSHCommand string
		PublicKey  string
	}{
		Env:        s.env,
		SSHCommand: s.replSSHConnectionCommand(),
		PublicKey:  pendingKey.PublicKey,
	}
	s.renderTemplate(r.Context(), w, "device-verified.html", data)
}

var (
	errExpiredToken         = errors.New("verification token has expired")
	errVerificationNotFound = errors.New("verification session not found")
	errKeyBelongsToOther    = errors.New("SSH key is already registered to another account")
	errUserLockedOut        = errors.New("user account is locked out")
)

func (s *Server) lookUpDeviceVerification(ctx context.Context, token string) (*exedb.PendingSSHKey, *EmailVerification, error) {
	// Look up the pending SSH key to validate token and get info
	pendingKey, err := withRxRes1(s, ctx, (*exedb.Queries).GetPendingSSHKeyByToken, token)
	if err != nil {
		return nil, nil, err
	}

	// Check if token has expired
	if time.Now().After(pendingKey.ExpiresAt) {
		// Clean up expired token - use context.WithoutCancel to ensure cleanup completes even if client disconnects
		withTx1(s, context.WithoutCancel(ctx), (*exedb.Queries).DeletePendingSSHKeyByToken, token)
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
	q := r.URL.Query()

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
			s.render401(w, r, exeweb.UnauthorizedData{InvalidToken: true})
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
		stage.Env
		Token       string
		RedirectURL string
		ReturnHost  string
		Email       string
		PairingCode string
		Source      string
	}{
		Env:         s.env,
		Token:       token,
		RedirectURL: q.Get("redirect"),
		ReturnHost:  q.Get("return_host"),
		Email:       email,
		PairingCode: code,
		Source:      source,
	}

	// Render template
	s.renderTemplate(r.Context(), w, "email-verification-form.html", data)
}

func (s *Server) createUserWithSSHKey(ctx context.Context, email, publicKey string, qc QualityCheck, inviterEmail string) (*exedb.User, error) {
	// Create the user if they don't exist
	// Note that this is called during email verification,
	// so we must look up the user by email (verified),
	// not by SSH key (which is what we are about to connect to this email).
	user, err := s.GetUserByEmail(ctx, email)
	if err != nil || user == nil {
		s.slog().InfoContext(ctx, "User doesn't exist, creating", "email", email)
		// User doesn't exist - create them with their alloc
		// Note: createUser calls resolvePendingShares internally
		user, err = s.createUser(context.WithoutCancel(ctx), publicKey, email, qc)
		if err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}
		s.slog().InfoContext(ctx, "Created new user", "email", email)
		s.slackFeed.NewUser(ctx, user.UserID, email, "ssh", inviterEmail)
	} else {
		s.slog().DebugContext(ctx, "User already exists", "email", email)
		// User already exists - still need to resolve pending shares
		// This handles the case where a box was shared with an existing user's email
		// after they registered, but before they logged in again
		if err := s.resolvePendingShares(ctx, email, user.UserID); err != nil {
			return nil, fmt.Errorf("resolve pending shares: %w", err)
		}
		if err := s.resolvePendingTeamInvites(ctx, email, user.UserID); err != nil {
			return nil, fmt.Errorf("resolve pending team invites: %w", err)
		}
	}

	// Store the SSH key as verified
	if publicKey != "" {
		// First check if this key already belongs to another user
		existingUserID, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserIDBySSHKey, publicKey)
		if err == nil && existingUserID != user.UserID {
			// Key belongs to another user - this is a security violation
			s.slog().WarnContext(ctx, "Attempted to verify with SSH key belonging to another user",
				"email", email, "key_owner_user_id", existingUserID)
			return nil, fmt.Errorf("this SSH key is already associated with another account")
		}

		fingerprint, err := sshkey.Fingerprint(publicKey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse public key: %w", err)
		}
		// Key doesn't exist or belongs to this user - safe to insert/skip
		// Use InsertSSHKeyForEmailUserIfNotExists to handle the case where
		// the key is already associated with this user (re-verification).
		// Generate a key-N comment since SSH key canonicalization strips comments.
		err = s.withTx(context.WithoutCancel(ctx), func(ctx context.Context, queries *exedb.Queries) error {
			comment, err := nextSSHKeyComment(ctx, queries, user.UserID)
			if err != nil {
				return fmt.Errorf("failed to generate SSH key comment: %w", err)
			}
			_, err = queries.InsertSSHKeyForEmailUserIfNotExists(ctx, exedb.InsertSSHKeyForEmailUserIfNotExistsParams{
				Email:       email,
				PublicKey:   publicKey,
				Comment:     comment,
				Fingerprint: fingerprint,
			})
			return err
		})
		if err != nil {
			s.slog().ErrorContext(ctx, "Error storing SSH key during verification", "error", err)
		}
	}

	return user, nil
}

// handleUserDashboard renders the user dashboard page
func (s *Server) handleUserDashboard(w http.ResponseWriter, r *http.Request, userID string) {
	// Get user info
	user, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserWithDetails, userID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get user info for dashboard", "error", err, "user_id", userID)
		http.Error(w, "Failed to load user information", http.StatusInternalServerError)
		return
	}

	// Check if user is locked out
	if user.IsLockedOut {
		s.renderLockedOutPage(w, r, userID)
		return
	}

	// Get user's SSH keys
	var sshKeys []SSHKey
	err = s.withRx(r.Context(), func(ctx context.Context, queries *exedb.Queries) error {
		dbKeys, err := queries.GetSSHKeysForUser(ctx, user.UserID)
		if err != nil {
			return err
		}
		for _, dbKey := range dbKeys {
			key := SSHKey{
				PublicKey:   dbKey.PublicKey,
				Comment:     dbKey.Comment,
				Fingerprint: dbKey.Fingerprint,
				AddedAt:     dbKey.AddedAt,
				LastUsedAt:  dbKey.LastUsedAt,
			}
			sshKeys = append(sshKeys, key)
		}
		return nil
	})
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get SSH keys for dashboard", "error", err, "email", user.Email)
	}

	// If there are active creation streams, wait for the boxes to appear in the DB.
	// We poll until all actively-being-created boxes appear, so the dashboard shows
	// them with status="creating" and can display the live creation output.
	// See https://github.com/boldsoftware/exe/issues/250.
	creatingHostnames := s.getActiveCreationHostnames(userID)
	deadline := time.Now().Add(5 * time.Second)
	for len(creatingHostnames) > 0 && time.Now().Before(deadline) {
		// Check which boxes have appeared in the DB
		var stillMissing []string
		for _, hostname := range creatingHostnames {
			exists, err := withRxRes1(s, r.Context(), (*exedb.Queries).BoxWithNameExists, hostname)
			if err != nil || exists == 0 {
				stillMissing = append(stillMissing, hostname)
			}
		}
		if len(stillMissing) == 0 {
			break
		}
		creatingHostnames = stillMissing
		time.Sleep(100 * time.Millisecond)
	}

	// Get user's boxes
	boxResults, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetBoxesForUserDashboard, user.UserID)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get boxes for dashboard", "error", err, "user_id", userID)
	}

	// Basic users (created for login-with-exe, no SSH keys, no boxes) should only see the profile tab.
	if len(boxResults) == 0 && s.isBasicUser(r.Context(), user, len(sshKeys)) {
		http.Redirect(w, r, "/user", http.StatusTemporaryRedirect)
		return
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
			Routes:          result.Routes,
			Region:          result.Region,
			Tags:            result.Tags,
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
			ProxyURL:        s.boxProxyAddress(result.Name),
			TerminalURL:     s.xtermURL(result.Name, r.TLS != nil),
			VSCodeURL:       template.URL(s.vscodeURL(result.Name)),
			ProxyPort:       route.Port,
			ProxyShare:      route.Share,
			RouteKnown:      box.Routes != nil && *box.Routes != "",
			SharedUserCount: sharedUserCount,
			ShareLinkCount:  shareLinkCount,
			TotalShareCount: sharedUserCount + shareLinkCount,
			SharedEmails:    sharedEmails,
			ShareLinks:      shareLinks,
			DisplayTags:     box.GetTags(),
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
	sharedBoxResults, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetBoxesSharedWithUser, user.UserID)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get shared boxes for dashboard", "error", err, "user_id", userID)
	}

	// Convert shared boxes to SharedBoxDisplayInfo
	sharedBoxes := make([]SharedBoxDisplayInfo, len(sharedBoxResults))
	for i, result := range sharedBoxResults {
		sharedBoxInfo := SharedBoxDisplayInfo{
			Name:       result.Name,
			OwnerEmail: result.OwnerEmail,
			ProxyURL:   s.boxProxyAddress(result.Name),
		}
		sharedBoxes[i] = sharedBoxInfo
	}

	// Get invite count for user
	inviteCount, err := withRxRes1(s, r.Context(), (*exedb.Queries).CountUnusedInviteCodesForUser, &user.UserID)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get invite count for dashboard", "error", err, "user_id", userID)
	}

	// Get team VMs for team admins
	var teamBoxes []TeamBoxDisplayInfo
	teamBoxResults, _ := s.ListTeamBoxesForAdmin(r.Context(), user.UserID)
	for _, result := range teamBoxResults {
		teamBoxInfo := TeamBoxDisplayInfo{
			Name:         result.Name,
			CreatorEmail: result.CreatorEmail,
			Status:       result.Status,
			ProxyURL:     s.boxProxyAddress(result.Name),
			SSHCommand:   s.boxSSHConnectionCommand(result.Name),
			DisplayTags:  parseTags(result.Tags),
		}
		teamBoxes = append(teamBoxes, teamBoxInfo)
	}

	// Check billing status for invite request button
	var hasBilling bool
	if s.env.SkipBilling {
		hasBilling = true
	} else {
		userBilling, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserBillingStatus, userID)
		if err == nil && userBilling.BillingStatus == "active" {
			hasBilling = true
		}
	}

	// Prepare template data
	data := UserPageData{
		Env:         s.env,
		SSHCommand:  s.replSSHConnectionCommand(),
		User:        user,
		SSHKeys:     sshKeys,
		Boxes:       boxes,
		SharedBoxes: sharedBoxes,
		TeamBoxes:   teamBoxes,
		ActivePage:  "boxes",
		IsLoggedIn:  true,
		InviteCount: inviteCount,
		HasBilling:  hasBilling,
		ShareVM:     r.URL.Query().Get("share_vm"),
		ShareEmail:  r.URL.Query().Get("share_email"),
	}

	// Render template
	s.renderTemplate(r.Context(), w, "dashboard.html", data)
}

func (s *Server) handleUserProfile(w http.ResponseWriter, r *http.Request, userID string) {
	// Get user info
	user, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserWithDetails, userID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get user info for profile", "error", err, "user_id", userID)
		http.Error(w, "Failed to load user information", http.StatusInternalServerError)
		return
	}

	// Check if user is locked out
	if user.IsLockedOut {
		s.renderLockedOutPage(w, r, userID)
		return
	}

	// Get user's SSH keys
	var sshKeys []SSHKey
	err = s.withRx(r.Context(), func(ctx context.Context, queries *exedb.Queries) error {
		dbKeys, err := queries.GetSSHKeysForUser(ctx, user.UserID)
		if err != nil {
			return err
		}
		for _, dbKey := range dbKeys {
			key := SSHKey{
				PublicKey:   dbKey.PublicKey,
				Comment:     dbKey.Comment,
				Fingerprint: dbKey.Fingerprint,
				AddedAt:     dbKey.AddedAt,
				LastUsedAt:  dbKey.LastUsedAt,
			}
			sshKeys = append(sshKeys, key)
		}
		return nil
	})
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get SSH keys for profile", "error", err, "email", user.Email)
	}

	// Get user's passkeys
	passkeys, err := s.getPasskeysForUser(r.Context(), userID)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get passkeys for profile", "error", err, "email", user.Email)
	}

	// Get site sessions (cookies for sites hosted by exe, excluding the main domain)
	// De-duplicate by domain (keep the most recently used)
	var siteSessions []SiteSession
	siteCookies, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetSiteCookiesForUser, exedb.GetSiteCookiesForUserParams{
		UserID: userID,
		Domain: s.env.WebHost,
	})
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get site cookies for profile", "error", err, "email", user.Email)
	} else {
		seenDomains := make(map[string]bool)
		for _, cookie := range siteCookies {
			// Skip duplicates (query is ordered by last_used_at DESC, so first one wins)
			if seenDomains[cookie.Domain] {
				continue
			}
			seenDomains[cookie.Domain] = true

			var lastUsed string
			if cookie.LastUsedAt != nil {
				lastUsed = cookie.LastUsedAt.Format("Jan 2, 2006")
			} else {
				lastUsed = "Never"
			}
			siteSessions = append(siteSessions, SiteSession{
				Domain:     cookie.Domain,
				URL:        "https://" + cookie.Domain,
				LastUsedAt: lastUsed,
			})
		}
	}

	// Get boxes shared with this user
	sharedBoxResults, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetBoxesSharedWithUser, user.UserID)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get shared boxes for profile", "error", err, "email", user.Email)
	}

	// Convert shared boxes to SharedBoxDisplayInfo
	sharedBoxes := make([]SharedBoxDisplayInfo, len(sharedBoxResults))
	for i, result := range sharedBoxResults {
		sharedBoxes[i] = SharedBoxDisplayInfo{
			Name:       result.Name,
			OwnerEmail: result.OwnerEmail,
			ProxyURL:   s.boxProxyAddress(result.Name),
		}
	}

	// Check if this is a basic user (created for login-with-exe, no SSH keys, no boxes)
	basicUser := s.isBasicUser(r.Context(), user, len(sshKeys))

	// Check billing status
	var hasBilling bool
	var billingStatus string
	userBilling, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserBillingStatus, userID)
	if err == nil {
		// BillingStatus is the derived status from billing_events table
		// GetUserBillingStatus checks if ANY account connected to the user has active billing
		billingStatus = userBilling.BillingStatus
		if billingStatus == "active" {
			hasBilling = true
		}
	}

	// Fetch credit balance if credit purchases are enabled and user has a billing account.
	creditBalance := tender.Zero()
	var purchases []PurchaseRow
	account, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetAccountByUserID, userID)
	if err == nil {
		balance, err := s.billing.SpendCredits(r.Context(), account.ID, 0, tender.Zero())
		if err != nil {
			s.slog().ErrorContext(r.Context(), "failed to fetch credit balance", "error", err, "user_id", userID)
		} else {
			creditBalance = balance
		}

		// Collect credit purchases from the last 30 days.
		cutoff := time.Now().AddDate(0, 0, -30)
		credits, err := withRxRes1(s, r.Context(), (*exedb.Queries).ListBillingCreditsForAccount, account.ID)
		if err != nil {
			s.slog().WarnContext(r.Context(), "failed to list billing credits", "error", err, "user_id", userID)
		}
		receiptURLs, err := s.billing.ReceiptURLs(r.Context(), account.ID)
		if err != nil {
			s.slog().WarnContext(r.Context(), "failed to fetch receipt URLs", "error", err, "user_id", userID)
		}
		for _, c := range credits {
			if c.Amount > 0 && c.StripeEventID != nil && c.CreatedAt.After(cutoff) {
				credits := c.Amount / 1_000_000
				p := PurchaseRow{
					Amount: fmt.Sprintf("%d", credits),
					Date:   c.CreatedAt.Format("02 Jan 2006"),
				}
				if c.StripeEventID != nil && receiptURLs != nil {
					p.ReceiptURL = receiptURLs[*c.StripeEventID]
				}
				purchases = append(purchases, p)
			}
		}
	}

	var shelleyFreeCreditRemainingPct float64
	var shelleyCreditsAvailable float64
	var shelleyCreditsMax float64
	var hasShelleyFreeCreditPct bool
	creditState, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserLLMCredit, userID)
	var creditPtr *exedb.UserLlmCredit
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.slog().WarnContext(r.Context(), "failed to fetch shelley free credit state", "error", err, "user_id", userID)
		}
	} else {
		creditPtr = &creditState
	}
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		plan, err := llmgateway.PlanForUser(r.Context(), s.db, userID, creditPtr)
		if err != nil {
			s.slog().WarnContext(r.Context(), "failed to resolve shelley free credit plan", "error", err, "user_id", userID)
		} else if plan.MaxCredit > 0 {
			effectiveAvailable := creditState.AvailableCredit
			if creditPtr == nil {
				effectiveAvailable = plan.MaxCredit
			} else if plan.Refresh != nil {
				effectiveAvailable, _ = plan.Refresh(creditState.AvailableCredit, creditState.LastRefreshAt, time.Now())
			}
			shelleyFreeCreditRemainingPct = (effectiveAvailable / plan.MaxCredit) * 100
			if shelleyFreeCreditRemainingPct < 0 {
				shelleyFreeCreditRemainingPct = 0
			}
			if shelleyFreeCreditRemainingPct > 100 {
				shelleyFreeCreditRemainingPct = 100
			}
			shelleyCreditsAvailable = effectiveAvailable
			if shelleyCreditsAvailable < 0 {
				shelleyCreditsAvailable = 0
			}
			shelleyCreditsMax = plan.MaxCredit
			hasShelleyFreeCreditPct = true
		}
	}

	extraCreditsUSD := float64(creditBalance.Microcents()) / 1_000_000
	var bonusRemaining float64
	if creditPtr != nil && creditPtr.BillingUpgradeBonusGranted == 1 && shelleyCreditsAvailable > shelleyCreditsMax {
		bonusRemaining = shelleyCreditsAvailable - shelleyCreditsMax
	}
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: shelleyCreditsAvailable,
		planMaxCredit:           shelleyCreditsMax,
		bonusRemaining:          bonusRemaining,
		extraCreditsUSD:         extraCreditsUSD,
	})
	monthlyBarPct := bar.monthlyBarPct
	bonusBarPct := bar.bonusBarPct
	extraBarPct := bar.extraBarPct
	totalRemainingPct := bar.totalRemainingPct
	usedCreditsUSD := bar.usedCreditsUSD
	usedBarPct := bar.usedBarPct
	totalCapacity := bar.totalCapacity

	// Fetch integrations for sudoers
	// TODO: integrations are sudoer-only while the feature is still hidden; will be public soon.
	isSudoer := s.UserHasExeSudo(r.Context(), userID)
	var integrations []IntegrationDisplayInfo
	if isSudoer {
		dbIntegrations, err := withRxRes1(s, r.Context(), (*exedb.Queries).ListIntegrationsByUser, userID)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to get integrations for profile", "error", err, "user_id", userID)
		}
		for _, ig := range dbIntegrations {
			info := IntegrationDisplayInfo{
				Name:        ig.Name,
				Type:        ig.Type,
				Attachments: ig.GetAttachments(),
			}
			if ig.Type == "http-proxy" {
				var cfg httpProxyConfig
				if err := json.Unmarshal([]byte(ig.Config), &cfg); err == nil {
					info.Target = redactURLPassword(cfg.Target)
					if name, _, ok := strings.Cut(cfg.Header, ":"); ok {
						info.HeaderName = name
					}
				}
			}
			integrations = append(integrations, info)
		}
	}

	// Prepare template data
	data := UserPageData{
		Env:           s.env,
		User:          user,
		SSHKeys:       sshKeys,
		Passkeys:      passkeys,
		SiteSessions:  siteSessions,
		SharedBoxes:   sharedBoxes,
		ActivePage:    "profile",
		IsLoggedIn:    true,
		BasicUser:     basicUser,
		HasBilling:    hasBilling,
		BillingStatus: billingStatus,

		CreditBalance:                 creditBalance,
		ShelleyFreeCreditRemainingPct: shelleyFreeCreditRemainingPct,
		ShelleyCreditsAvailable:       shelleyCreditsAvailable,
		ShelleyCreditsMax:             shelleyCreditsMax,
		ExtraCreditsUSD:               extraCreditsUSD,
		TotalCreditsUSD:               shelleyCreditsAvailable + extraCreditsUSD,
		TotalRemainingPct:             totalRemainingPct,
		MonthlyBarPct:                 monthlyBarPct,
		BonusBarPct:                   bonusBarPct,
		BonusRemainingUSD:             bar.bonusRemaining,
		MonthlyAvailableUSD:           bar.monthlyAvailable,
		ExtraBarPct:                   extraBarPct,
		UsedCreditsUSD:                usedCreditsUSD,
		TotalCapacityUSD:              totalCapacity,
		UsedBarPct:                    usedBarPct,
		HasShelleyFreeCreditPct:       hasShelleyFreeCreditPct,
		MonthlyCreditsResetAt:         nextUTCMonthStart().Format("15:04 on Jan 2"),
		Purchases:                     purchases,

		IsSudoer:     isSudoer,
		Integrations: integrations,
	}

	// Fetch team data if user is in a team
	if team, err := s.GetTeamForUser(r.Context(), userID); err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get team for user", "error", err, "user_id", userID)
	} else if team != nil {
		ti := &TeamDisplayInfo{
			DisplayName: team.DisplayName,
			Role:        team.Role,
			IsAdmin:     team.Role != "user",
		}
		if members, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetTeamMembers, team.TeamID); err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to get team members", "error", err, "team_id", team.TeamID)
		} else {
			for _, m := range members {
				mdi := TeamMemberDisplayInfo{
					Email: m.Email,
					Role:  m.Role,
				}
				if t, err := time.Parse("2006-01-02 15:04:05", m.JoinedAt); err == nil {
					mdi.JoinedAt = &t
				}
				ti.Members = append(ti.Members, mdi)
			}
		}
		data.TeamInfo = ti
	} else {
		// Not in a team — check for pending invites
		ce := canonicalizeEmail(user.Email)
		if invites, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetPendingTeamInvitesForUser, ce); err == nil {
			// Get user's VM count once for all invites
			var vmCount int64
			if len(invites) > 0 {
				vmCount, _ = withRxRes1(s, r.Context(), (*exedb.Queries).CountBoxesForUser, userID)
			}
			for _, inv := range invites {
				data.PendingTeamInvites = append(data.PendingTeamInvites, PendingTeamInviteInfo{
					Token:     inv.Token,
					TeamName:  inv.TeamName,
					InvitedBy: inv.InvitedByEmail,
					ExpiresAt: inv.ExpiresAt,
					VMCount:   vmCount,
				})
			}
		}
	}

	// Render template
	s.renderTemplate(r.Context(), w, "user-profile.html", data)
}

func nextUTCMonthStart() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
}

// handleCreditsBuy handles POST /credits/buy to start a credit purchase checkout.
func (s *Server) handleCreditsBuy(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Redirect(w, r, "/auth?redirect=/user", http.StatusTemporaryRedirect)
		return
	}

	dollars, err := strconv.ParseInt(r.FormValue("dollars"), 10, 64)
	if err != nil || dollars <= 0 {
		http.Error(w, "Invalid amount", http.StatusBadRequest)
		return
	}

	user, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "failed to get user for credit purchase", "error", err)
		http.Error(w, "Failed to load user", http.StatusInternalServerError)
		return
	}

	account, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetAccountByUserID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Redirect(w, r, "/billing/update?source=credits", http.StatusSeeOther)
		return
	}
	if err != nil {
		s.slog().ErrorContext(r.Context(), "failed to get billing account for credit purchase", "error", err, "user_id", userID)
		http.Error(w, "Failed to load billing account", http.StatusInternalServerError)
		return
	}

	billingStatus, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserBillingStatus, userID)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "failed to load billing status for credit purchase", "error", err, "user_id", userID)
		http.Error(w, "Failed to load billing status", http.StatusInternalServerError)
		return
	}
	if !userIsPaying(&billingStatus) {
		http.Redirect(w, r, "/billing/update?source=credits", http.StatusSeeOther)
		return
	}

	baseURL := getScheme(r) + "://" + r.Host
	checkoutURL, err := s.billing.BuyCredits(r.Context(), account.ID, &billing.BuyCreditsParams{
		Email:      user.Email,
		Amount:     tender.Mint(dollars*100, 0),
		SuccessURL: baseURL + "/credits/success",
		CancelURL:  baseURL + "/user",
	})
	if err != nil {
		s.slog().ErrorContext(r.Context(), "failed to create credit checkout", "error", err, "user_id", userID)
		http.Error(w, "Failed to start checkout", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, checkoutURL, http.StatusSeeOther)
}

// handleCreditsSuccess handles GET /credits/success after a completed credit purchase.
func (s *Server) handleCreditsSuccess(w http.ResponseWriter, r *http.Request) {
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Redirect(w, r, "/auth?redirect=/user", http.StatusTemporaryRedirect)
		return
	}

	account, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetAccountByUserID, userID)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "no billing account for credit sync", "error", err, "user_id", userID)
		http.Redirect(w, r, "/user", http.StatusSeeOther)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	if err := s.billing.SyncCredits(ctx, account.CreatedAt); err != nil {
		s.slog().ErrorContext(ctx, "failed to sync credits", "error", err, "user_id", userID)
	}

	http.Redirect(w, r, "/user", http.StatusSeeOther)
}

// getScheme returns the request scheme, respecting X-Forwarded-Proto
// for requests arriving through a reverse proxy (e.g., exe.dev TLS proxy).
func getScheme(r *http.Request) string {
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" || proto == "http" {
		return proto
	}
	return schemeForTLS(r.TLS != nil)
}

// getScheme returns the http(s) request scheme for useTLS.
func schemeForTLS(useTLS bool) string {
	if useTLS {
		return "https"
	}
	return "http"
}

// handlePullExeuntuEverywhere pulls the exeuntu image to all exelet hosts in parallel.
func (s *Server) handlePullExeuntuEverywhere(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tag := r.URL.Query().Get("tag")
	if tag == "" {
		http.Error(w, "missing required parameter: tag", http.StatusBadRequest)
		return
	}

	image := fmt.Sprintf("ghcr.io/boldsoftware/exeuntu:%s", tag)
	s.slog().InfoContext(ctx, "pulling image to all exelet hosts", "image", image)

	if len(s.exeletClients) == 0 {
		http.Error(w, "no exelet hosts configured", http.StatusServiceUnavailable)
		return
	}

	type pullResult struct {
		Host   string `json:"host"`
		Digest string `json:"digest,omitempty"`
		Error  string `json:"error,omitempty"`
	}

	results := make([]pullResult, len(s.exeletClients))
	var wg sync.WaitGroup

	idx := 0
	for _, ec := range s.exeletClients {
		if !ec.up.Load() {
			continue
		}

		wg.Add(1)
		go func(idx int, ec *exeletClient) {
			defer wg.Done()

			result := pullResult{Host: ec.addr}
			resp, err := ec.client.LoadFilesystem(ctx, &storageapi.LoadFilesystemRequest{Image: image})
			if err != nil {
				s.slog().ErrorContext(ctx, "failed to pull image", "host", ec.addr, "image", image, "error", err)
				result.Error = err.Error()
			} else {
				s.slog().InfoContext(ctx, "image pulled successfully", "host", ec.addr, "image", image, "digest", resp.ID)
				result.Digest = resp.ID
			}
			results[idx] = result
		}(idx, ec)

		idx++
	}

	wg.Wait()

	results = results[:idx]

	// Check if all succeeded
	allSucceeded := true
	for _, r := range results {
		if r.Error != "" {
			allSucceeded = false
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if !allSucceeded {
		w.WriteHeader(http.StatusInternalServerError)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(map[string]any{
		"image":   image,
		"success": allSucceeded,
		"results": results,
	})
}

// handleClearExeuntuLatestCache clears the tag resolver cache for the exeuntu:latest tag.
// This should be called after pushing a new :latest tag to force fresh resolution.
func (s *Server) handleClearExeuntuLatestCache(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if s.tagResolver == nil {
		http.Error(w, "tag resolver not configured", http.StatusServiceUnavailable)
		return
	}

	registry := "ghcr.io"
	repository := "boldsoftware/exeuntu"
	tag := "latest"

	s.slog().InfoContext(ctx, "clearing tag resolver cache", "registry", registry, "repository", repository, "tag", tag)

	err := s.tagResolver.DeleteTag(ctx, registry, repository, tag)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to clear tag resolver cache", "error", err)
		http.Error(w, "failed to clear cache: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"cleared": fmt.Sprintf("%s/%s:%s", registry, repository, tag),
		"success": true,
	})
}

// handleInvite allocates and displays a single invite code.
// POST: allocates the next available invite and shows it.
// GET: redirects to dashboard (must POST to allocate).
func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()

	// Only POST can allocate an invite
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Get user info
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to get user info for invite", "error", err, "user_id", userID)
		http.Error(w, "Failed to load user information", http.StatusInternalServerError)
		return
	}

	// Get and allocate the next unallocated invite
	invite, err := withRxRes1(s, ctx, (*exedb.Queries).GetNextUnallocatedInviteForUser, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		// No more invites available
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to get invite code", "error", err, "user_id", userID)
		http.Error(w, "Failed to load invite code", http.StatusInternalServerError)
		return
	}

	// Mark the invite as allocated
	err = withTx1(s, ctx, (*exedb.Queries).AllocateInviteCode, invite.ID)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to allocate invite code", "error", err, "invite_id", invite.ID)
		http.Error(w, "Failed to allocate invite code", http.StatusInternalServerError)
		return
	}

	data := struct {
		stage.Env
		User       exedb.User
		InviteCode exedb.InviteCode
		IsLoggedIn bool
		ActivePage string
		BasicUser  bool
	}{
		Env:        s.env,
		User:       user,
		InviteCode: invite,
		IsLoggedIn: true,
		ActivePage: "invites",
		BasicUser:  false,
	}

	s.renderTemplate(ctx, w, "invite.html", data)
}

// handleInviteRequest handles the request for more invite codes.
// It sends a Slack notification to the #feed channel.
func (s *Server) handleInviteRequest(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()

	// Get user info
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to get user info for invite request", "error", err, "user_id", userID)
		http.Error(w, "Failed to load user information", http.StatusInternalServerError)
		return
	}

	// Check if user has billing set up — only users with active billing can request more invites
	hasBilling := s.env.SkipBilling
	if !hasBilling {
		billingStatus, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserBillingStatus, userID)
		if err != nil {
			s.slog().ErrorContext(ctx, "Failed to check billing status for invite request", "error", err, "user_id", userID)
		} else {
			hasBilling = !userNeedsBilling(&billingStatus)
		}
	}

	if !hasBilling {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Send Slack notification
	s.slackFeed.InviteRequest(ctx, user.Email, hasBilling)

	// Render confirmation page
	data := struct {
		stage.Env
		User       exedb.User
		IsLoggedIn bool
		ActivePage string
		BasicUser  bool
	}{
		Env:        s.env,
		User:       user,
		IsLoggedIn: true,
		ActivePage: "invites",
		BasicUser:  false,
	}

	s.renderTemplate(ctx, w, "invite-requested.html", data)
}
