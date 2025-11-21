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
)

// exe.dev provides a "magic" proxy for user's boxes. When a user requests https://boxname.exe.dev/,
// we terminate TLS, and send that request on to the box using HTTP. This allows users to serve
// web sites without dealing with, for example, TLS. The port we go to is determined by the "route" command.
// We also provide some basic auth. By default, you have to have access to the box (which we do via
// a redirect dance) to have access to the proxy, but we also let you mark it public.
//
// If you have multiple web servers, for certain ports, we also redirect those requests. So,
// https://boxname.exe.dev:8080/ will go to port 8080 on the box. These non-default ports are always
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

	// Reject HTTPS requests to localhost domains in dev mode
	if r.TLS != nil && s.env.DevMode != "" && strings.HasSuffix(hostHeaderHost, ".localhost") {
		s.slog().WarnContext(r.Context(), "HTTPS not supported for localhost domains", "host", r.Host)
		http.Error(w, "HTTPS not supported for localhost domains. Use exe.local instead.", http.StatusBadRequest)
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

	// Find the box.
	// Careful: we aren't checking the team or owner in this look-up, so we must do it below.
	box, err := withRxRes(s, r.Context(), func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxNamed(ctx, boxName)
	})
	if err != nil {
		http.Error(w, "Box not found", http.StatusNotFound)
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
		// Check if user is authenticated
		userID, authenticated := s.getAuthenticatedUserID(r, box)
		if !authenticated {
			// Not authenticated - redirect to auth (preserving share token if present)
			// The share link will be checked again after authentication
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

		if !hasAccess {
			// User is authenticated but doesn't have access
			// Don't leak box existence
			http.Error(w, "Box not found", http.StatusNotFound)
			return
		}
	}

	// Handle debug path in dev mode
	if r.URL.Path == "/__exe.dev/debug" && s.env.DevMode != "" {
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
			userEmail, _ := withRxRes(s, r.Context(), func(ctx context.Context, queries *exedb.Queries) (string, error) {
				return queries.GetEmailByUserID(ctx, userID)
			})
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
			terminalHost := fmt.Sprintf("%s.xterm.%s", boxName, s.getMainDomainWithPort())
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			data := struct {
				BoxName         string
				Port            int
				TerminalURL     string
				ShowWelcomeStep bool
			}{
				BoxName:         boxName,
				Port:            route.Port,
				TerminalURL:     fmt.Sprintf("%s://%s/", scheme, terminalHost),
				ShowWelcomeStep: strings.Contains(box.Image, "exeuntu") && route.Port == 8000,
			}

			w.WriteHeader(http.StatusBadGateway)
			_ = s.renderTemplate(w, "proxy-unreachable.html", data)
			return
		}

		// Non-owner: return a terse 502
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
}

// isProxyRequest determines if a request should be handled by the proxy
func (s *Server) isProxyRequest(host string) bool {
	hostname, port, _ := net.SplitHostPort(host)
	if hostname == "" {
		hostname = host
	}
	hostname = domz.Canonicalize(hostname)
	if hostname == "" || domz.IsLocalhost(hostname) {
		return false
	}
	switch hostname {
	case s.getMainDomain(), s.getMainDomain("www"), s.tsDomain:
		return false
	}

	if s.env.DevMode != "" {
		// There are special rules for dev mode:
		//    - If port is specified, it must be numeric
		//    - If host contains only ":", it is not a proxy request
		//    - If host contains more than one ":", it is not a proxy request
		return isNumericPort(port) && hostname != ":" && strings.Count(hostname, ":") <= 1
	}

	// Any other hostname with at least one dot is a proxy request,
	// otherwise it's invalid.
	return strings.Contains(hostname, ".")
}

func isNumericPort(port string) bool {
	if port == "" {
		return true // No port is considered valid (default port)
	}
	_, err := strconv.Atoi(port)
	return err == nil
}

// parseProxyHostname extracts box name from hostname.
// Supports box.exe.dev (production) and box.exe.local (dev).
// In dev mode also supports box.localhost for HTTP-only requests.
// Returns an empty string if hostname is not a valid proxy hostname.
func (s *Server) parseProxyHostname(hostname string) (box string) {
	hostname = domz.Canonicalize(domz.StripPort(hostname))
	if hostname == "" {
		return ""
	}

	domains := []string{s.getMainDomain()}
	if s.env.DevMode != "" {
		domains = append(domains, "localhost")
	}

	for _, dom := range domains {
		if box := domz.Label(hostname, dom); box != "" {
			return box
		}
	}
	return ""
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

// getMainDomain returns the main domain based on dev mode.
// If sub is provided, it returns sub.domain (e.g., "www.exe.local" or "box.exe.dev").
// If sub is empty, it returns just the domain (e.g., "exe.local" or "exe.dev").
func (s *Server) getMainDomain(sub ...string) string {
	domain := s.env.WebHost
	if s.env.DevMode != "" {
		if s.servingHTTPS() {
			domain = "exe.local"
		} else {
			domain = "localhost"
		}
	}
	if len(sub) > 0 && sub[0] != "" {
		return sub[0] + "." + domain
	}
	return domain
}

// getMainDomainWithPort returns the main domain with port for redirects
func (s *Server) getMainDomainWithPort() string {
	domain := s.getMainDomain()

	// In dev mode, add the HTTP port
	if s.env.DevMode != "" && s.servingHTTP() {
		port := s.httpPort()
		// Only add port if it's not the default HTTP port (80)
		if port != 80 {
			return fmt.Sprintf("%s:%d", domain, port)
		}
	}

	return domain
}

// getProxyPorts returns the list of ports that should be used for proxying
func (s *Server) getProxyPorts() []int {
	if s.env.DevMode != "" {
		// Check if test infrastructure provided specific ports
		if testPorts := os.Getenv("TEST_PROXY_PORTS"); testPorts != "" {
			var ports []int
			for _, portStr := range strings.Split(testPorts, ",") {
				portStr = strings.TrimSpace(portStr)
				if portStr == "" {
					continue
				}
				if port, err := strconv.Atoi(portStr); err == nil {
					ports = append(ports, port)
				}
			}
			if len(ports) > 0 {
				return ports
			}
		}
		// Dev mode fallback: ports 8001-8008 and 9999
		return []int{8001, 8002, 8003, 8004, 8005, 8006, 8007, 8008, 9999}
	}
	// Production mode: all ports 2000-9999
	ports := make([]int, 0, 8000)
	for port := 2000; port <= 9999; port++ {
		ports = append(ports, port)
	}
	return ports
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
		func() string {
			if r.TLS != nil {
				return "https"
			}
			return "http"
		}(),
		r.Host,
		typ,
		q.Encode(),
	)
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
	returnURL.Scheme = "https"
	if r.TLS == nil {
		returnURL.Scheme = "http"
	}
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
	cookieName := "exe-proxy-auth"
	if s.isTerminalRequest(r.Host) {
		cookieName = "exe-auth"
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

	// Determine scheme and domain based on request
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	// In dev mode, choose domain based on scheme:
	// - HTTP uses localhost
	// - HTTPS uses exe.local
	// In production, always use exe.dev
	var mainDomain string
	if s.env.DevMode != "" {
		if r.TLS != nil {
			mainDomain = "exe.local"
		} else {
			mainDomain = "localhost"
		}
	} else {
		mainDomain = "exe.dev"
	}

	// Parse the port from the incoming request
	_, portStr, err := net.SplitHostPort(r.Host)
	var mainHost string
	if err != nil {
		// No port in Host header - use default port for scheme
		mainHost = mainDomain
	} else {
		// Port is explicit - use it (e.g., localhost:8080)
		mainHost = fmt.Sprintf("%s:%s", mainDomain, portStr)
	}

	authURL := fmt.Sprintf("%s://%s/auth?redirect=%s&return_host=%s",
		scheme, mainHost, url.QueryEscape(redirect), url.QueryEscape(r.Host))

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
	// Get the specific cookie value to delete
	var cookieValue string
	cookie, err := r.Cookie("exe-proxy-auth")
	if err == nil && cookie.Value != "" {
		cookieValue = cookie.Value
	}

	// Delete only this specific cookie from the database
	if cookieValue != "" {
		err := s.withTx(r.Context(), func(ctx context.Context, queries *exedb.Queries) error {
			return queries.DeleteAuthCookieByValue(ctx, cookieValue)
		})
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to delete specific proxy auth cookie from database", "error", err)
		}
	}

	// Clear the proxy auth cookie in the browser
	http.SetCookie(w, &http.Cookie{
		Name:     "exe-proxy-auth",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	// Redirect to logged out page on main domain
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	// In dev mode, choose domain based on scheme
	var mainDomain string
	if s.env.DevMode != "" {
		if r.TLS != nil {
			mainDomain = "exe.local"
		} else {
			mainDomain = "localhost"
		}
	} else {
		mainDomain = "exe.dev"
	}

	// Parse the port from the incoming request
	_, portStr, err := net.SplitHostPort(r.Host)
	var mainHost string
	if err != nil {
		mainHost = mainDomain
	} else {
		mainHost = fmt.Sprintf("%s:%s", mainDomain, portStr)
	}

	logoutURL := fmt.Sprintf("%s://%s/logged-out", scheme, mainHost)
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
	box, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.GetBoxByNameAndAlloc(ctx, exedb.GetBoxByNameAndAllocParams{
			Name:            boxName,
			CreatedByUserID: userID,
		})
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("box '%s' not found or access denied", boxName)
		}
		return nil, fmt.Errorf("database error: %v", err)
	}
	return &box, nil
}

// proxyToContainer proxies the HTTP request to a container via SSH port forwarding
func (s *Server) proxyToContainer(w http.ResponseWriter, r *http.Request, box *exedb.Box, route exedb.Route) error {
	// Validate box has SSH credentials
	if len(box.SSHClientPrivateKey) == 0 || box.SSHPort == nil {
		return fmt.Errorf("box missing SSH credentials")
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
			retries := []time.Duration{
				100 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond,
				1 * time.Second, 1 * time.Second, 2 * time.Second, 3 * time.Second,
			}
			conn, errs := s.sshPool.DialWithRetries(ctx, network, addr, sshHost, *box.SSHUser, int(*box.SSHPort), sshKey, cfg, retries)
			if conn != nil {
				return conn, nil
			}
			return nil, fmt.Errorf("SSH dial failed after retries: %w", errors.Join(errs...))
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
		setForwardedHeaders(req, r)

		// Add user info headers if authenticated
		if userID, ok := s.getAuthenticatedUserID(r, *box); ok {
			email, err := withRxRes(s, req.Context(), func(ctx context.Context, queries *exedb.Queries) (string, error) {
				return queries.GetEmailByUserID(ctx, userID)
			})
			if err != nil {
				s.slog().ErrorContext(r.Context(), "failed to get user email for authenticated proxy headers", "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			req.Header.Set("X-ExeDev-UserID", userID)
			req.Header.Set("X-ExeDev-Email", email)
		}

		// Remove the exe-proxy-auth cookie
		nCookies := len(req.Cookies())
		var cookies []*http.Cookie
		for _, c := range req.Cookies() {
			if c.Name != "exe-proxy-auth" {
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

	proto := "http"
	if incoming.TLS != nil {
		proto = "https"
	}
	outgoing.Header.Set("X-Forwarded-Proto", proto)

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
	return s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.IncrementShareLinkUsage(ctx, shareToken)
	})
}

// autoCreateShareFromLink creates an email-based share for a user who accessed via share link
// This allows the user to retain access even if the share link is later revoked
func (s *Server) autoCreateShareFromLink(ctx context.Context, userID string, boxID int, shareToken string) error {
	// Get the share link to find who created it
	shareLink, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.BoxShareLink, error) {
		return queries.GetBoxShareLinkByTokenAndBoxID(ctx, exedb.GetBoxShareLinkByTokenAndBoxIDParams{
			ShareToken: shareToken,
			BoxID:      int64(boxID),
		})
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
