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
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"exe.dev/boxname"
	"exe.dev/cobble"
	"exe.dev/dnsresolver"
	"exe.dev/domz"
	"exe.dev/errorz"
	"exe.dev/exedb"
	"exe.dev/exens"
	"exe.dev/llmgateway"
	"exe.dev/metricsbag"
	storageapi "exe.dev/pkg/api/exe/storage/v1"
	"exe.dev/route53"
	"exe.dev/sshkey"
	"exe.dev/stage"
	"exe.dev/tracing"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	sloghttp "github.com/samber/slog-http"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
	_ "modernc.org/sqlite"
	"tailscale.com/client/local"
	"tailscale.com/client/tailscale"
	"tailscale.com/net/tsaddr"
)

const sshKnownHostsPath = "/.well-known/ssh-known-hosts"

// acmeServerAdapter wraps exens.Server to implement route53.LocalACMEProvider.
type acmeServerAdapter struct {
	server *exens.Server
}

func (a *acmeServerAdapter) UpsertTXTRecord(ctx context.Context, name, value string, ttl int64) error {
	a.server.SetTXTRecord(name, value)
	return nil
}

func (a *acmeServerAdapter) DeleteTXTRecord(ctx context.Context, name, value string) error {
	a.server.DeleteTXTRecord(name, value)
	return nil
}

func (s *Server) prepareHandler() http.Handler {
	lg := s.prepareLlmGateway()
	servMux := http.NewServeMux()
	servMux.Handle("/_/gateway/", lg)
	servMux.Handle("/", s)

	h := s.httpMetrics.Wrap(servMux)
	h = metricsbag.Wrap(h)
	h = LoggerMiddleware(s.log)(h)
	h = RecoverHTTPMiddleware(s.log)(h)
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

	lg := llmgateway.NewGateway(s.slog(), s.db, llmgateway.APIKeys{
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

	s.slog().Info("set up wildcard TLS certificates with Route 53", "decision", s.env.UseRoute53, "stage", s.env.String())
	if s.env.UseRoute53 {
		// Only use DNS-01 wildcards for BoxHost (exe.xyz) domains.
		// WebHost (exe.dev) uses standard autocert with TLS-ALPN-01.
		wildcardDomains := []string{s.env.BoxHost, s.env.BoxSub("xterm"), s.env.BoxSub("shelley")}
		wildcardDomains = dedupInPlace(wildcardDomains)
		wildcardDomains = domz.FilterEmpty(wildcardDomains)
		s.wildcardCertManager = route53.NewWildcardCertManager(
			wildcardDomains,
			autocert.DirCache("certs"),
			s.sshMetrics.letsencryptRequests,
		)
		// Enable dual-write ACME TXT records to local DNS server during transition
		if s.dnsServer != nil {
			s.wildcardCertManager.SetLocalDNSProvider(&acmeServerAdapter{s.dnsServer})
		}
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

var (
	errBoxNotFound    = errors.New("box not found")
	errInvalidBoxName = errors.New("invalid box name")
)

// exeNewPathPrompts maps paths on exe.new to pre-filled prompts for the /new page.
var moltbotPrompt = `ANTHROPIC_API_KEY=<fill-this-in>

Set up Moltbot (https://docs.molt.bot/install) on this VM. Moltbot used to be 
called Clawdbot until recently, so be aware if the executable or other docs 
still refer to Clawdbot. Use the non-interactive and accept-risk flags for 
moltbot onboarding. Add the supplied auth or token as needed. Configure nginx 
to forward from the default port 18789 to the root location on the default 
enabled site config, making sure to enable Websocket support. Pairing is done 
by "clawdbot devices list" and "clawdbot device approve <request id>". Make 
sure the dashboard shows that Moltbot's health is OK. exe.dev handles forwarding 
from port 8000 to port 80/443 and HTTPS for us, so the final "reachable" 
should be https://<vm-name>.exe.xyz without port specification.`

var exeNewPathPrompts = map[string]string{
	"/moltbot":  moltbotPrompt,
	"/clawdbot": moltbotPrompt,
}

// resolveBoxName converts a hostname to a box name.
// If hostname is a subdomain of the main domain (e.g., box.exe.dev),
// it returns the box name with the main domain suffix stripped (e.g., "box").
// Shelley subdomains (box.shelley.exe.xyz) are handled by stripping the ".shelley" part.
// For all other hostname values, a CNAME lookup is performed, and the above
// rules are applied to the result; otherwise an error is returned.
func (s *Server) resolveBoxName(ctx context.Context, hostname string) (string, error) {
	hostname = domz.Canonicalize(hostname)
	// Reject empty hostnames (cheap check).
	if hostname == "" {
		return "", errInvalidBoxName
	}
	// Reject exact box domain (apex).
	if hostname == s.env.BoxHost {
		return "", errInvalidBoxName
	}
	// If a subdomain of our box domain, return the box name.
	// Use CutBase (not Label) to handle multi-level subdomains like box.shelley.exe.xyz
	sub, ok := domz.CutBase(hostname, s.env.BoxHost)
	if ok && sub != "" {
		// Handle shelley subdomain: box.shelley.exe.xyz -> box
		if strings.HasSuffix(sub, ".shelley") {
			return strings.TrimSuffix(sub, ".shelley"), nil
		}
		// For regular subdomains, only accept single-level (no dots)
		if !strings.Contains(sub, ".") {
			return sub, nil
		}
	}

	// Reject non-domain hostnames.
	if !strings.Contains(hostname, ".") {
		return "", errInvalidBoxName
	}

	return s.resolveCustomDomainBoxName(ctx, hostname)
}

func (s *Server) lookupCNAME(ctx context.Context, host string) (string, error) {
	if s.lookupCNAMEFunc != nil {
		return s.lookupCNAMEFunc(ctx, host)
	}
	cname, err := dnsresolver.LookupCNAME(ctx, host)
	if err == nil {
		return cname, nil
	}
	if errorz.HasType[*net.DNSError](err) {
		return "", err
	}
	s.slog().WarnContext(ctx, "lookupCNAME: fallback to net resolver", "host", host, "error", err)
	return net.DefaultResolver.LookupCNAME(ctx, host)
}

func (s *Server) lookupA(ctx context.Context, host string) ([]netip.Addr, error) {
	fn := net.DefaultResolver.LookupNetIP
	if s.lookupAFunc != nil {
		fn = s.lookupAFunc
	}
	addrs, err := fn(ctx, "ip4", host)
	for i, addr := range addrs {
		addrs[i] = addr.Unmap()
	}
	return addrs, err
}

// validateHostForTLSCert checks if the given host is valid for TLS certificate issuance.
// The trace_id is added by ConnContext in httpsServer, so it's available in the context
// during TLS handshakes.
func (s *Server) validateHostForTLSCert(ctx context.Context, host string) error {
	host = domz.Canonicalize(host)
	if domz.FirstMatch(host, s.env.BoxHost, s.env.WebHost) != "" {
		return nil
	}
	if host == "exe.new" {
		return nil
	}
	if host == "bold.dev" {
		return nil
	}

	boxName, err := s.resolveCustomDomainBoxName(ctx, host)
	if err != nil {
		return err
	}
	if boxName == "" {
		s.slog().WarnContext(ctx, "hostPolicy: unable to resolve box name", "host", host)
		return fmt.Errorf("unable to resolve VM for %s", host)
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
// - Wildcard exe.xyz certificates (via Route 53 DNS-01) when configured
// - Standard autocert for exe.dev and custom domains
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
			cert, err := s.wildcardCertManager.GetCertificate(hello)
			if errors.Is(err, route53.ErrUnrecognizedDomain) {
				s.slog().Debug("wildcard GetCertificate rejected unrecognized domain", "error", err)
			} else if err != nil {
				s.slog().Error("wildcard GetCertificate failed; giving up", "error", err)
			}
			return cert, err
		}

		// fall through to standard autocert
	}

	// 3) WebHost (exe.dev) and custom domains use standard autocert (TLS-ALPN-01)

	if s.certManager == nil {
		s.slog().Error("no certificate manager configured; was https enabled at startup?", "serverName", serverName)
		return nil, fmt.Errorf("no certificate manager configured for %s", serverName)
	}

	return s.certManager.GetCertificate(hello)
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

// isClientDisconnectError returns true for errors caused by client disconnects
// (e.g., user navigated away, refreshed, or closed the connection).
func isClientDisconnectError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrShortWrite) {
		return true
	}
	msg := err.Error()
	disconnectPatterns := []string{
		"http2: stream closed",
		"broken pipe",
		"connection reset by peer",
		"client disconnected",
		"connection timed out",
	}
	for _, pattern := range disconnectPatterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}

// renderTemplate is a helper method that handles template parsing and execution
func (s *Server) renderTemplate(ctx context.Context, w http.ResponseWriter, templateName string, data interface{}) error {
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

	isKnownHostsRequest := r.URL.Path == sshKnownHostsPath
	hostname := domz.Canonicalize(domz.StripPort(r.Host))

	// Redirect requests to BoxHost apex (exe.xyz) to WebHost (exe.dev).
	// BoxHost is only for box subdomains (vmname.exe.xyz); the apex itself should
	// redirect to WebHost to avoid passkey RPID mismatch errors during auth.
	if s.env.BoxHost != s.env.WebHost {
		if hostname == s.env.BoxHost && !isKnownHostsRequest {
			target := fmt.Sprintf("%s://%s%s", getScheme(r), s.env.WebHost, r.URL.RequestURI())
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			return
		}
	}

	// Redirect requests to exe.new to WebHost/new (exe.dev/new).
	// This is a vanity domain that lets users start a new box from a memorable URL.
	// Special paths like /moltbot and /clawdbot redirect with a pre-filled prompt.
	if hostname == "exe.new" {
		target := fmt.Sprintf("%s://%s/new", getScheme(r), s.env.WebHost)
		if prompt := exeNewPathPrompts[r.URL.Path]; prompt != "" {
			target += "?prompt=" + url.QueryEscape(prompt)
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		return
	}

	// Redirect requests to bold.dev to WebHost (exe.dev).
	if hostname == "bold.dev" {
		target := fmt.Sprintf("https://%s%s", s.env.WebHost, r.URL.RequestURI())
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		return
	}

	// Check if this should be handled by the proxy handler.
	// Shelley subdomain (vm.shelley.exe.xyz) is also handled as a proxy request.
	isProxy := s.isProxyRequest(r.Host)
	isTerminal := s.isTerminalRequest(r.Host)

	// Add request classification to logs
	if isProxy {
		sloghttp.AddCustomAttributes(r, slog.Bool("proxy", true))
	}
	if isTerminal {
		sloghttp.AddCustomAttributes(r, slog.Bool("terminal", true))
	}

	// Try to get userID from auth cookie for logging and tracking
	var loggedUserID string
	if userID, err := s.validateAuthCookie(r); err == nil {
		loggedUserID = userID
		sloghttp.AddCustomAttributes(r, slog.String("user_id", userID))
	}

	if isProxy {
		metricsbag.SetLabel(r.Context(), LabelProxy, "true")
		// box label is set in handleProxyRequest after resolving the box name
		s.handleProxyRequest(w, r)
		return
	}

	// Non-proxy content (main site, terminal) should only be served on the main port.
	if !s.isRequestOnMainPort(w, r) {
		return
	}

	if isTerminal {
		metricsbag.SetLabel(r.Context(), LabelProxy, "false")
		metricsbag.SetLabel(r.Context(), LabelPath, "/terminal")
		s.handleTerminalRequest(w, r)
		return
	}

	// Set labels for non-proxy HTTP metrics
	metricsbag.SetLabel(r.Context(), LabelProxy, "false")
	metricsbag.SetLabel(r.Context(), LabelPath, normalizePath(r.URL.Path))

	// Track unique web visitors (main site only, not proxy or terminal)
	if s.hllTracker != nil && loggedUserID != "" {
		s.hllTracker.NoteEvent("web-visit", loggedUserID)
	}

	// Handle root path and user dashboard
	path := r.URL.Path
	// Debug endpoints (pprof, expvar), gated by localhost or Tailscale access
	if strings.HasPrefix(path, "/debug") {
		requireLocalAccess(s.handleDebug)(w, r)
		return
	} else if strings.HasPrefix(path, "/docs") || path == "/llms.txt" || path == "/docs.md" {
		if s.docs != nil && s.docs.Handle(w, r) {
			return
		}
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
		requireLocalAccess(s.handleMetrics)(w, r)
	case sshKnownHostsPath:
		s.handleKnownHosts(w, r)
		return
	case "/sitemap.xml":
		s.handleSitemap(w, r)
	case "/robots.txt":
		s.handleRobots(w, r)
	case "/about":
		s.serveStaticFile(w, r, "about.html")
	case "/pricing":
		http.Redirect(w, r, "/docs/pricing", http.StatusTemporaryRedirect)
		return
	case "/love":
		s.handleLovePage(w, r)
	case "/jobs":
		s.serveStaticFile(w, r, "jobs.html")
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
	case "/take-my-money":
		http.Redirect(w, r, "/billing/update", http.StatusMovedPermanently)
	case "/auth":
		s.handleAuth(w, r)
	case "/auth/confirm":
		s.handleAuthConfirm(w, r)
	case "/link-discord":
		s.handleLinkDiscord(w, r)

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

		// Handle passkey routes
		if strings.HasPrefix(path, "/passkey/") {
			s.handlePasskeyRoutes(w, r)
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

// serveStaticFile serves a file from the embedded static directory.
// Uses the binary's VCS build time as the modification time to enable HTTP caching.
func (s *Server) serveStaticFile(w http.ResponseWriter, r *http.Request, filename string) {
	// Create a sub-filesystem from the static directory
	staticSubFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	f, err := staticSubFS.Open(filename)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	http.ServeContent(w, r, filename, buildTime(), bytes.NewReader(data))
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
		userID, err := queries.GetUserIDByEmail(ctx, pendingKey.UserEmail)
		if err != nil {
			return fmt.Errorf("failed to get user ID: %w", err)
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
			s.render401(w, r, unauthorizedData{InvalidToken: true})
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

	// Prepare template data
	data := UserPageData{
		Env:         s.env,
		SSHCommand:  s.replSSHConnectionCommand(),
		User:        user,
		SSHKeys:     sshKeys,
		Boxes:       boxes,
		SharedBoxes: sharedBoxes,
		ActivePage:  "boxes",
		IsLoggedIn:  true,
		InviteCount: inviteCount,
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
	account, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetAccountWithBillingStatus, userID)
	if err == nil {
		// BillingStatus is the derived status from billing_events table
		billingStatus = account.BillingStatus
		if billingStatus == "active" {
			hasBilling = true
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
	}

	// Render template
	s.renderTemplate(r.Context(), w, "user-profile.html", data)
}

// getScheme returns the request scheme
func getScheme(r *http.Request) string {
	return schemeForTLS(r.TLS != nil)
}

// getScheme returns the http(s) request scheme for useTLS.
func schemeForTLS(useTLS bool) string {
	if useTLS {
		return "https"
	}
	return "http"
}

// isValidRedirectURL validates that a redirect URL is safe (relative path only).
// This prevents open redirect attacks where an attacker could redirect users
// to a malicious external site after authentication.
func isValidRedirectURL(redirectURL string) bool {
	if redirectURL == "" {
		return false
	}
	u, err := url.Parse(redirectURL)
	if err != nil {
		return false
	}
	// Block absolute URLs (has scheme like https:, javascript:, data:)
	// and protocol-relative URLs (//evil.com which have a Host but no Scheme)
	if u.Scheme != "" || u.Host != "" {
		return false
	}
	return path.IsAbs(u.Path)
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
	enc.Encode(map[string]interface{}{
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
	json.NewEncoder(w).Encode(map[string]interface{}{
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

	// Check if user has billing set up
	hasBilling := false
	billingStatus, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserBillingStatus, userID)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to check billing status for invite request", "error", err, "user_id", userID)
	} else {
		hasBilling = !userNeedsBilling(&billingStatus)
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
