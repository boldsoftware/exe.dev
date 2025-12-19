package execore

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"exe.dev/container"
	"exe.dev/domz"
	"exe.dev/exedb"
	"exe.dev/metricsbag"
	"exe.dev/stage"
)

// countingConn wraps a net.Conn to count bytes read and written.
type countingConn struct {
	net.Conn
	metrics *HTTPMetrics
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.metrics.AddProxyBytes("in", n)
	}
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.metrics.AddProxyBytes("out", n)
	}
	return n, err
}

// exe.dev provides a "magic" proxy for user's boxes. When a user requests https://vmname.exe.dev/,
// we terminate TLS, and send that request on to the box using HTTP. This allows users to serve
// web sites without dealing with, for example, TLS. The port we go to is determined by the "route" command.
// We also provide some basic auth. By default, you have to have access to the box (which we do via
// a redirect dance) to have access to the proxy, but we also let you mark it public.
//
// If you have multiple web servers, for certain ports, we also redirect those requests. So,
// https://vmname.exe.dev:8080/ will go to port 8080 on the box. These non-default ports are always
// private.

// handleProxyRequest handles requests that should be proxied to containers
// This handler is called when the Host header matches box.exe.dev or box.exe.local
func (s *Server) handleProxyRequest(w http.ResponseWriter, r *http.Request) {
	// Ensure the port in the Host header matches the listener's local port
	conn, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		s.slog().ErrorContext(r.Context(), "Failed to get local address from request context")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	_, localPortStr, err := net.SplitHostPort(conn.String())
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to parse local address", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	localPort, err := strconv.Atoi(localPortStr)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to convert local port to integer", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	hostHeaderPort := 0
	hostHeaderHost, hostPortStr, err := net.SplitHostPort(r.Host)
	if err != nil {
		// No port in Host header, that's fine if it's the default port which only
		// happens in HTTPS land...
		hostHeaderHost = r.Host
		if s.servingHTTPS() {
			hostHeaderPort = s.httpsPort()
		} else {
			s.slog().ErrorContext(r.Context(), "Host header didn't have port but we're not using default ports.")
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	} else {
		hostHeaderPort, err = strconv.Atoi(hostPortStr)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to convert host port to integer", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}
	if hostHeaderPort != localPort {
		s.slog().WarnContext(r.Context(), "Host header port mismatch", "host_port", hostHeaderPort, "local_port", localPort)
		http.Error(w, "internal server error", http.StatusBadRequest)
		return
	}

	// Handle magic URL for authentication
	if r.URL.Path == "/__exe.dev/auth" {
		s.slog().InfoContext(r.Context(), "[REDIRECT] Magic auth URL accessed", "host", r.Host, "path", r.URL.Path)
		s.handleMagicAuth(w, r)
		return
	}

	// Handle login URL
	if r.URL.Path == "/__exe.dev/login" {
		s.handleProxyLogin(w, r)
		return
	}

	// Handle logout URL
	if r.URL.Path == "/__exe.dev/logout" {
		s.slog().InfoContext(r.Context(), "[REDIRECT] Logout URL accessed", "host", r.Host, "path", r.URL.Path)
		s.handleProxyLogout(w, r)
		return
	}

	// Parse hostname to extract box name and optional explicit target port
	boxName, err := s.resolveBoxName(r.Context(), hostHeaderHost)
	if err != nil {
		s.slog().WarnContext(r.Context(), "Failed to resolve box name", "host", r.Host, "error", err)
		http.Error(w, "Invalid Hostname", http.StatusBadRequest)
		return
	}
	if boxName == "" {
		// Don't log a warning here, too noisy.
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}

	// Set box name label for metrics
	metricsbag.SetLabel(r.Context(), LabelBox, boxName)

	// Find the box.
	// Careful: we aren't checking the team or owner in this look-up, so we must do it below.
	box, err := withRxRes1(s, r.Context(), (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Box doesn't exist - show 401 to avoid leaking existence
			s.renderAccessRequired(w, r)
		} else {
			s.slog().ErrorContext(r.Context(), "Failed to look up box", "error", err, "box_name", boxName)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	// Determine final route:
	// - If no explicit targetPort (0), or it matches server default ports, or equals box's default, use box route
	// - Otherwise create an ad-hoc private route for the requested port
	var route exedb.Route
	boxRoute := box.GetRoute()
	targetPort := hostHeaderPort
	if targetPort == 0 || targetPort == boxRoute.Port || s.isDefaultServerPort(targetPort) {
		route = boxRoute
	} else {
		route = exedb.Route{Port: targetPort, Share: "private"}
	}

	// Apply authentication based on route share setting
	if route.Share == "private" {
		// Check if user is authenticated on this subdomain
		userID, authenticated := s.getAuthenticatedUserID(r, box)
		if !authenticated {
			// Not authenticated on subdomain - redirect to main domain auth
			// This will check if they have exe-auth cookie and handle accordingly
			s.redirectToAuth(w, r)
			return
		}

		// User is authenticated - check if they have access
		hasAccess := false

		// Check access
		accessType, err := s.hasUserAccessToBox(r.Context(), userID, &box)
		if err == nil && (accessType == BoxAccessOwner || accessType == BoxAccessEmailShare) {
			hasAccess = true
		}

		// Check share link access
		if !hasAccess && s.checkShareLinkAccess(r, box.ID) {
			if shareToken := r.URL.Query().Get("share"); shareToken != "" {
				// Valid share link - increment usage
				_ = s.incrementShareLinkUsage(r.Context(), shareToken)

				// Auto-create email-based share for this user
				// This allows the user to access the box even if the share link is later revoked
				_ = s.autoCreateShareFromLink(r.Context(), userID, box.ID, shareToken)
			}
			hasAccess = true
		}

		// Check support access: user is root support and box has support_access_allowed
		if !hasAccess && s.FindBoxForSupportUser(r.Context(), userID, boxName) != nil {
			s.slog().InfoContext(r.Context(), "proxy support access granted", "box", boxName, "user_id", userID)
			hasAccess = true
		}

		if !hasAccess {
			// User is authenticated but doesn't have access
			// Show 401 to avoid leaking box existence
			s.renderAccessRequired(w, r)
			return
		}
	}

	// Handle debug path in dev/test environments
	if r.URL.Path == "/__exe.dev/debug" && s.env.WebDev {
		// Show debug info for /__exe.dev/debug in dev mode
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "Proxy handler - Route matched!\n")
		fmt.Fprintf(w, "Box: %s\n", boxName)
		fmt.Fprintf(w, "Route port: %d\n", route.Port)
		fmt.Fprintf(w, "Route share: %s\n", route.Share)
		fmt.Fprintf(w, "Request method: %s\n", r.Method)
		fmt.Fprintf(w, "Request path: %s\n", r.URL.Path)

		// Show current user info
		if userID, err := s.validateProxyAuthCookie(r); err == nil {
			// Ignore error
			userEmail, _ := withRxRes1(s, r.Context(), (*exedb.Queries).GetEmailByUserID, userID)
			fmt.Fprintf(w, "Logged in user: %q (%q)\n", userEmail, userID)
		} else if errors.Is(err, http.ErrNoCookie) {
			fmt.Fprintf(w, "Not logged in\n")
		} else {
			fmt.Fprintf(w, "Invalid auth cookie: %v\n", err)
		}
		return
	}

	// Proxy the request to the container
	err = s.proxyToContainer(w, r, &box, route)
	if err != nil {
		s.slog().DebugContext(r.Context(), "Failed to proxy request", "error", err, "box", boxName)

		// Determine if the requester is the owner of the box
		isOwner := false
		if userID, ok := s.getAuthenticatedUserID(r, box); ok {
			if box.CreatedByUserID == userID {
				isOwner = true
			}
		}

		if isOwner {
			// Render owner-facing help page
			data := struct {
				stage.Env
				BoxName         string
				SSHCommand      string
				Port            int
				TerminalURL     string
				ShowWelcomeStep bool
				IsShelleyPort   bool
				ShelleyURL      string
			}{
				Env:             s.env,
				BoxName:         boxName,
				SSHCommand:      s.boxSSHConnectionCommand(boxName),
				Port:            route.Port,
				TerminalURL:     s.xtermURL(boxName, r.TLS != nil),
				ShowWelcomeStep: strings.Contains(box.Image, "exeuntu") && route.Port == 8000,
				IsShelleyPort:   route.Port == 9999,
				ShelleyURL:      s.shelleyURL(boxName),
			}

			w.WriteHeader(http.StatusBadGateway)
			_ = s.renderTemplate(w, "proxy-unreachable.html", data)
			return
		}

		// Non-owner: render 503 page
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = s.renderTemplate(w, "503.html", nil)
		return
	}
}

// isProxyRequest reports whether a request to host should be handled by the proxy.
// The proxy handles requests to VMs, which are can single subdomains of the box domain,
// or third party domains pointing here.
func (s *Server) isProxyRequest(host string) bool {
	// Given that we cannot enumerate all proxy hosts,
	// implement by explicitly excluding known non-proxy hosts, and then allowing the rest through.
	// TODO: When we have public ips, we could make this decision based on the IP the request came in on,
	// or at least note when that decision varies from the hostname-based one.
	host = domz.Canonicalize(domz.StripPort(host))
	switch host {
	case "":
		return false // refuse the temptation to guess
	case s.env.BoxHost:
		return false // box apex is not a proxy target
	case "blog" + "." + s.env.WebHost:
		return true // special main webserver subdomains that are actually served on VMs, whee
	}
	if s.env.WebDev {
		// When doing local development, it's useful to be able to reach the webserver
		// via the local machine's hostname, not just localhost.
		// This lets you do something like "socat TCP-LISTEN:8081,fork TCP:localhost:8080"
		// and try out the mobile dashboard from your phone.
		oshost, err := os.Hostname()
		if err == nil && host == oshost {
			return false
		}
	}
	// Exclude our internal debug pages and the public web server.
	if domz.FirstMatch(host, s.tsDomain, s.env.WebHost, s.env.BoxSub("xterm")) != "" {
		return false
	}
	if domz.IsIPAddr(host) {
		return false // refuse IP addresses
	}
	// We've excluded known non-proxy hosts.
	// At this point, anything domain-like is fair game.
	return strings.Contains(host, ".")
}

// getAuthenticatedUserID checks if the user is authenticated and returns their userID
// Returns (userID, true) if authenticated, ("", false) if not authenticated
func (s *Server) getAuthenticatedUserID(r *http.Request, box exedb.Box) (string, bool) {
	if userID, err := s.validateProxyAuthCookie(r); err == nil {
		return userID, true
	}

	// Basic auth -- token is provided as username, password is ignored
	if username, _, ok := r.BasicAuth(); ok && username != "" {
		if userID, err := s.validateProxyBearerToken(r.Context(), username, box.ID); err == nil {
			r.URL.User = nil
			return userID, true
		}
	}

	// Bearer token auth
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.Fields(authHeader)
		if len(parts) >= 2 && strings.EqualFold(parts[0], "Bearer") {
			token := strings.TrimSpace(parts[1])
			if token != "" {
				if userID, err := s.validateProxyBearerToken(r.Context(), token, box.ID); err == nil {
					r.Header.Del("Authorization")
					return userID, true
				}
			}
		}
	}

	return "", false
}

func (s *Server) webBaseURLNoRequest() string {
	return fmt.Sprintf("%s://%s%s", s.bestScheme(), s.env.WebHost, s.bestURLPort())
}

func (s *Server) webBaseURL(r *http.Request) string {
	_, port, _ := net.SplitHostPort(r.Host)
	if port == "" {
		// Use listener port if not default for the scheme
		port = s.urlPort(r.TLS != nil)
	} else {
		// Respect port from Host header
		port = ":" + port
	}
	return fmt.Sprintf("%s://%s%s", getScheme(r), s.env.WebHost, port)
}

// getProxyPorts returns the list of ports that should be used for proxying.
// TEST_PROXY_PORTS env var overrides the stage config (used by e1e tests).
func (s *Server) getProxyPorts() []int {
	if testPorts := os.Getenv("TEST_PROXY_PORTS"); testPorts != "" {
		var ports []int
		for _, portStr := range strings.Split(testPorts, ",") {
			if port, err := strconv.Atoi(portStr); err == nil {
				ports = append(ports, port)
			}
		}
		return ports
	}
	return s.env.ProxyPorts
}

// isDefaultServerPort returns true if the port should use the box's default route
// This includes port 443 (HTTPS) and the server's main HTTP port
func (s *Server) isDefaultServerPort(port int) bool {
	// Port 443 always uses default route
	if port == 443 {
		return true
	}

	// Check if it matches the server's main HTTP port
	if s.servingHTTP() && s.httpPort() == port {
		return true
	}

	return false
}

func makeAuthURL(typ string, r *http.Request, q url.Values) string {
	return fmt.Sprintf("%s://%s/__exe.dev/%s?%s",
		getScheme(r),
		r.Host,
		typ,
		q.Encode(),
	)
}

// proxyAuthCookieName returns the cookie name for proxy authentication on a specific port.
// Each port gets its own cookie to prevent cross-port authentication.
func proxyAuthCookieName(port int) string {
	return fmt.Sprintf("login-with-exe-%d", port)
}

// getRequestPort extracts the port number from an HTTP request's Host header.
// For requests without an explicit port, it returns the default port for the scheme
// (443 for HTTPS, 80 for HTTP).
func getRequestPort(r *http.Request) (int, error) {
	_, portStr, err := net.SplitHostPort(r.Host)
	if err != nil {
		// No port in Host header - use default port for the scheme
		if r.TLS != nil {
			return 443, nil
		}
		return 80, nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("invalid port: %w", err)
	}
	return port, nil
}

// renderAccessRequired renders the 401.html page for unauthorized access.
// This is shown when a box doesn't exist OR when user doesn't have access
// to avoid leaking box existence information.
func (s *Server) renderAccessRequired(w http.ResponseWriter, r *http.Request) {
	var email string
	if uid, err := s.validateProxyAuthCookie(r); err == nil {
		email, _ = withRxRes1(s, r.Context(), (*exedb.Queries).GetEmailByUserID, uid)
	}

	u := &url.URL{
		Scheme: getScheme(r),
		Host:   r.Host,
		Path:   r.URL.Path,
	}

	data := unauthorizedData{
		Email:        email,
		AuthURL:      s.webBaseURLNoRequest() + "/auth",
		RedirectURL:  u.String(),
		ReturnHost:   r.Host,
		LoginWithExe: true,
		// PasskeyEnabled is false: box subdomains can't use passkeys (RPID mismatch)
	}

	w.WriteHeader(http.StatusUnauthorized)
	s.renderTemplate(w, "401.html", data)
}

// redirectToAuth redirects the user to the /__exe.dev/login URL
// which will then redirect to the main domain auth flow
func (s *Server) redirectToAuth(w http.ResponseWriter, r *http.Request) {
	// Redirect to /__exe.dev/login with the current URL as the redirect parameter.
	// It must be absolute to avoid being treated as relative, which when local will
	// redirect the user to localhost over https which does not have a valid cert.

	// Do not modify r.URL in place so we don't mess with a caller's
	// understanding of reality.
	returnURL := *r.URL
	returnURL.Scheme = getScheme(r)
	returnURL.Host = cmp.Or(returnURL.Host, r.Host)

	authURL := makeAuthURL("login", r, url.Values{
		"redirect": {returnURL.String()},
	})

	s.slog().DebugContext(r.Context(), "[REDIRECT] redirectToAuth", "from", returnURL, "to", authURL)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// handleMagicAuth handles the magic authentication URL /__exe.dev/auth

func (s *Server) handleMagicAuth(w http.ResponseWriter, r *http.Request) {
	secret := r.URL.Query().Get("secret")
	redirectURL := r.URL.Query().Get("redirect")

	s.slog().DebugContext(r.Context(), "[REDIRECT] handleMagicAuth called", "host", r.Host, "secret", secret[:min(10, len(secret))]+"...", "redirect", redirectURL)

	if secret == "" {
		http.Error(w, "Missing secret parameter", http.StatusBadRequest)
		return
	}

	// Validate and consume the magic secret
	magicSecret, err := s.validateMagicSecret(secret)
	if err != nil {
		s.slog().DebugContext(r.Context(), "[REDIRECT] Magic secret validation failed", "error", err)
		http.Error(w, "Invalid or expired secret", http.StatusUnauthorized)
		return
	}

	// Create authentication cookie for this subdomain
	cookieValue, err := s.createAuthCookie(r.Context(), magicSecret.UserID, r.Host)
	if err != nil {
		http.Error(w, "Failed to create authentication cookie", http.StatusInternalServerError)
		return
	}

	// Determine cookie name based on request type (terminal vs proxy)
	var cookieName string
	if s.isTerminalRequest(r.Host) {
		cookieName = "exe-auth"
	} else {
		port, err := getRequestPort(r)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to get port from request", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		cookieName = proxyAuthCookieName(port)
	}

	setAuthCookie(w, r, cookieName, cookieValue)

	// Redirect to the original URL or the redirect from the magic secret
	finalRedirect := redirectURL
	if finalRedirect == "" {
		finalRedirect = magicSecret.RedirectURL
	}
	if finalRedirect == "" {
		finalRedirect = "/" // Default fallback
	}

	s.slog().DebugContext(r.Context(), "[REDIRECT] handleMagicAuth redirecting", "to", finalRedirect)
	http.Redirect(w, r, finalRedirect, http.StatusSeeOther)
}

// handleProxyLogin handles the login URL /__exe.dev/login
// It redirects to the main domain auth flow with redirect and return_host parameters
func (s *Server) handleProxyLogin(w http.ResponseWriter, r *http.Request) {
	s.slog().DebugContext(r.Context(), "[REDIRECT] handleProxyLogin called", "host", r.Host)

	redirect := r.URL.Query().Get("redirect")
	if redirect == "" {
		redirect = "/"
	}

	// Use webBaseURLNoRequest to get the main domain URL without copying the request's port.
	// The main domain (exe.dev) always runs on the default HTTPS port (443),
	// even when the proxy request came in on a non-standard port like 9999.
	authURL := fmt.Sprintf("%s/auth?redirect=%s&return_host=%s", s.webBaseURLNoRequest(), url.QueryEscape(redirect), url.QueryEscape(r.Host))

	s.slog().DebugContext(r.Context(), "[REDIRECT] handleProxyLogin redirecting to main domain", "to", authURL)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// handleProxyLogout handles the logout URL /__exe.dev/logout
// GET: renders a simple confirmation form
// POST: performs the logout and redirects to logged-out page
func (s *Server) handleProxyLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		// Render a simple logout confirmation form
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Logout</title></head>
<body>
<h1>Logout</h1>
<p>Are you sure you want to log out?</p>
<form method="POST" action="/__exe.dev/logout">
<button type="submit">Yes, log out</button>
</form>
<a href="/">Cancel</a>
</body>
</html>`)
		return
	}

	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// POST: perform the logout
	port, err := getRequestPort(r)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get port from request for logout", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	cookieName := proxyAuthCookieName(port)

	// Get the specific cookie value to delete
	var cookieValue string
	cookie, err := r.Cookie(cookieName)
	if err == nil && cookie.Value != "" {
		cookieValue = cookie.Value
	}

	// Delete only this specific cookie from the database
	if cookieValue != "" {
		err := withTx1(s, r.Context(), (*exedb.Queries).DeleteAuthCookieByValue, cookieValue)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to delete specific proxy auth cookie from database", "error", err)
		}
	}

	// Clear the proxy auth cookie in the browser
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	// Redirect to logged out page on main domain
	logoutURL := fmt.Sprintf("%s/logged-out", s.webBaseURLNoRequest())
	http.Redirect(w, r, logoutURL, http.StatusTemporaryRedirect)
}

// getBoxForUser retrieves a box for the given user/team/name
func (s *Server) getBoxForUser(ctx context.Context, publicKey, boxName string) (*exedb.Box, error) {
	user, err := s.getUserByPublicKey(ctx, publicKey)
	if err != nil || user == nil {
		return nil, fmt.Errorf("user not found")
	}
	return s.boxForNameUserID(ctx, boxName, user.UserID)
}

func (s *Server) boxForNameUserID(ctx context.Context, boxName, userID string) (*exedb.Box, error) {
	box, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxByNameAndAlloc, exedb.GetBoxByNameAndAllocParams{
		Name:            boxName,
		CreatedByUserID: userID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("VM '%s' not found or access denied", boxName)
		}
		return nil, fmt.Errorf("database error: %v", err)
	}
	return &box, nil
}

// proxyToContainer proxies the HTTP request to a container via SSH port forwarding
func (s *Server) proxyToContainer(w http.ResponseWriter, r *http.Request, box *exedb.Box, route exedb.Route) error {
	// Validate box has SSH credentials
	if len(box.SSHClientPrivateKey) == 0 || box.SSHPort == nil {
		return fmt.Errorf("VM missing SSH credentials")
	}

	// Parse the SSH private key
	sshKey, err := container.CreateSSHSigner(string(box.SSHClientPrivateKey))
	if err != nil {
		return fmt.Errorf("failed to parse SSH private key: %w", err)
	}

	// Determine SSH host address from the box's ctrhost
	sshHost := box.SSHHost()

	// Try to proxy to the configured port
	err = s.proxyViaSSHPortForward(w, r, sshHost, box, sshKey, route.Port)
	if err != nil {
		return fmt.Errorf("failed to proxy to port %d: %w", route.Port, err)
	}

	return nil
}

// createSSHTunnelTransport creates an HTTP transport that tunnels through SSH to a container
func (s *Server) createSSHTunnelTransport(sshHost string, box *exedb.Box, sshKey ssh.Signer) *http.Transport {
	// Build an HTTP transport that dials through SSH to the target on the SSH host.
	// The sshDialer uses the connection pool for SSH connections
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			cfg := &ssh.ClientConfig{
				User:            *box.SSHUser,
				Auth:            []ssh.AuthMethod{ssh.PublicKeys(sshKey)},
				HostKeyCallback: box.CreateHostKeyCallback(),
				Timeout:         30 * time.Second,
			}
			// Use a tight deadline to quickly detect unbound ports
			ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			conn, err := s.sshPool.DialWithRetries(ctx, network, addr, sshHost, *box.SSHUser, int(*box.SSHPort), sshKey, cfg, []time.Duration{
				10 * time.Millisecond,
			})
			if err != nil {
				return nil, fmt.Errorf("SSH dial failed: %w", err)
			}
			return &countingConn{Conn: conn, metrics: s.httpMetrics}, nil
		},
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// proxyViaSSHPortForward establishes an SSH connection and proxies the HTTP request directly
func (s *Server) proxyViaSSHPortForward(w http.ResponseWriter, r *http.Request, sshHost string, box *exedb.Box, sshKey ssh.Signer, targetPort int) error {
	transport := s.createSSHTunnelTransport(sshHost, box, sshKey)

	// Configure the reverse proxy using NewSingleHostReverseProxy
	targetURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", targetPort))
	rp := httputil.NewSingleHostReverseProxy(targetURL)
	rp.Transport = transport

	// Customize the director to add user headers and remove auth cookie
	defaultDirector := rp.Director
	rp.Director = func(req *http.Request) {
		defaultDirector(req)
		clearExeDevHeaders(req)
		setForwardedHeaders(req, r)

		// Add user info headers if authenticated
		if userID, ok := s.getAuthenticatedUserID(r, *box); ok {
			email, err := withRxRes1(s, req.Context(), (*exedb.Queries).GetEmailByUserID, userID)
			if err != nil {
				s.slog().ErrorContext(r.Context(), "failed to get user email for authenticated proxy headers", "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			req.Header.Set("X-ExeDev-UserID", userID)
			req.Header.Set("X-ExeDev-Email", email)
		}

		// Remove login-with-exe-* cookies (port-specific proxy auth cookies)
		nCookies := len(req.Cookies())
		var cookies []*http.Cookie
		for _, c := range req.Cookies() {
			if !strings.HasPrefix(c.Name, "login-with-exe-") {
				cookies = append(cookies, c)
			}
		}
		if len(cookies) != nCookies {
			// Clear all cookies, re-add only the non-auth ones
			req.Header.Del("Cookie")
			for _, c := range cookies {
				req.AddCookie(c)
			}
		}
	}

	// Capture proxy errors and return them to the caller
	var proxyErr error
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		s.slog().DebugContext(r.Context(), "HTTP proxy error", "error", err, "target_port", targetPort)
		proxyErr = err
	}

	// Proxy the request
	rp.ServeHTTP(w, r)
	return proxyErr
}

// setForwardedHeaders ensures downstream services are aware of the original request context.
// It sets X-Forwarded-Proto, X-Forwarded-Host, and X-Forwarded-For so apps can reconstruct the public URL.
func setForwardedHeaders(outgoing, incoming *http.Request) {
	if outgoing == nil || incoming == nil {
		return
	}

	outgoing.Header.Set("X-Forwarded-Proto", getScheme(incoming))

	if host := incoming.Host; host != "" {
		outgoing.Header.Set("X-Forwarded-Host", host)
	}

	existingXFF := strings.TrimSpace(incoming.Header.Get("X-Forwarded-For"))
	clientIP := clientIPFromRemoteAddr(incoming.RemoteAddr)
	switch {
	case existingXFF != "" && clientIP != "":
		outgoing.Header.Set("X-Forwarded-For", existingXFF+", "+clientIP)
	case existingXFF != "":
		outgoing.Header.Set("X-Forwarded-For", existingXFF)
	case clientIP != "":
		outgoing.Header.Set("X-Forwarded-For", clientIP)
	}
}

func clientIPFromRemoteAddr(addr string) string {
	if addr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return host
	}
	if ip := net.ParseIP(addr); ip != nil {
		return ip.String()
	}
	return addr
}

// clearExeDevHeaders removes any X-ExeDev-* headers from the outbound proxy request.
// This prevents clients from spoofing authentication state via custom headers
// and reserves the entire X-ExeDev- namespace for our use.
func clearExeDevHeaders(req *http.Request) {
	if req == nil {
		return
	}
	for key := range req.Header {
		if strings.HasPrefix(strings.ToLower(key), "x-exedev-") {
			req.Header.Del(key)
		}
	}
}

// checkShareLinkAccess checks if the request has a valid share link token
func (s *Server) checkShareLinkAccess(r *http.Request, boxID int) bool {
	shareToken := r.URL.Query().Get("share")
	if shareToken == "" {
		return false
	}

	valid, err := s.validateShareLinkForBox(r.Context(), shareToken, boxID)
	if err != nil {
		s.slog().DebugContext(r.Context(), "share link validation error", "error", err)
		return false
	}

	return valid
}

// incrementShareLinkUsage increments the usage counter for a share link
func (s *Server) incrementShareLinkUsage(ctx context.Context, shareToken string) error {
	return withTx1(s, ctx, (*exedb.Queries).IncrementShareLinkUsage, shareToken)
}

// autoCreateShareFromLink creates an email-based share for a user who accessed via share link
// This allows the user to retain access even if the share link is later revoked
func (s *Server) autoCreateShareFromLink(ctx context.Context, userID string, boxID int, shareToken string) error {
	// Get the share link to find who created it
	shareLink, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxShareLinkByTokenAndBoxID, exedb.GetBoxShareLinkByTokenAndBoxIDParams{
		ShareToken: shareToken,
		BoxID:      int64(boxID),
	})
	if err != nil {
		return err
	}

	// Create email-based share (will fail silently if already exists due to UNIQUE constraint)
	return s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		_, err := queries.CreateBoxShare(ctx, exedb.CreateBoxShareParams{
			BoxID:            int64(boxID),
			SharedWithUserID: userID,
			SharedByUserID:   shareLink.CreatedByUserID,
			Message:          nil, // No message for auto-created shares
		})
		// Ignore duplicate errors
		if err != nil && strings.Contains(err.Error(), "UNIQUE constraint") {
			return nil
		}
		return err
	})
}
