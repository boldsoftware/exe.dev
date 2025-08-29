package exe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
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
	"exe.dev/ctrhosttest"
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
// This handler is called when the Host header matches box.team.exe.dev or box.team.localhost
func (s *Server) handleProxyRequest(w http.ResponseWriter, r *http.Request) {
	// Ensure the port in the Host header matches the listener's local port
	conn, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		slog.Error("Failed to get local address from request context")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	_, localPortStr, err := net.SplitHostPort(conn.String())
	if err != nil {
		slog.Error("Failed to parse local address", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	localPort, err := strconv.Atoi(localPortStr)
	if err != nil {
		slog.Error("Failed to convert local port to integer", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	hostHeaderPort := 0
	hostHeaderHost, hostPortStr, err := net.SplitHostPort(r.Host)
	if err != nil {
		// No port in Host header, that's fine if it's the default port which only
		// happens in HTTPS land...
		hostHeaderHost = r.Host
		if s.httpsLn != nil && s.httpsLn.tcp != nil {
			hostHeaderPort = s.httpsLn.tcp.Port
		} else {
			slog.Error("Host header didn't have port but we're not using default ports.")
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	} else {
		hostHeaderPort, err = strconv.Atoi(hostPortStr)
		if err != nil {
			slog.Error("Failed to convert host port to integer", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}
	if hostHeaderPort != localPort {
		slog.Warn("Host header port mismatch", "host_port", hostHeaderPort, "local_port", localPort)
		http.Error(w, "internal server error", http.StatusBadRequest)
		return
	}

	// Handle magic URL for authentication
	if r.URL.Path == "/__exe.dev/auth" {
		slog.Info("[REDIRECT] Magic auth URL accessed", "host", r.Host, "path", r.URL.Path)
		s.handleMagicAuth(w, r)
		return
	}

	// Handle logout URL
	if r.URL.Path == "/__exe.dev/logout" {
		slog.Info("[REDIRECT] Logout URL accessed", "host", r.Host, "path", r.URL.Path)
		s.handleProxyLogout(w, r)
		return
	}

	// Parse hostname to extract box name and optional explicit target port
	boxName := s.parseProxyHostname(hostHeaderHost)
	if boxName == "" {
		// Don't log a warning here, too noisy.
		http.Error(w, "invalid hostname", http.StatusBadRequest)
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

	slog.Debug("Proxy routing",
		"box", boxName, "route_port", route.Port, "route_share", route.Share,
		"target_port", targetPort, "host", r.Host, "path", r.URL.Path)

	// Apply authentication based on route share setting
	if route.Share == "private" {
		// Check if user is authenticated
		userID, authenticated := s.getAuthenticatedUserID(r)
		if !authenticated {
			// User not authenticated, redirect to auth
			s.redirectToAuth(w, r)
			return
		}

		// User is authenticated, check if they have access to this box
		// Box must belong to user's alloc
		alloc, err := s.getUserAlloc(r.Context(), userID)
		if err != nil || alloc == nil {
			http.Error(w, "Error checking user allocation", http.StatusInternalServerError)
			return
		}
		if box.AllocID != alloc.AllocID {
			// User is authenticated but box belongs to different alloc
			// Tempting to return a 401/403, but that leaks the existence of the box.
			http.Error(w, "Box not found", http.StatusNotFound)
			return
		}
	}

	// Handle debug path in dev mode
	if r.URL.Path == "/__exe.dev/debug" && s.devMode != "" {
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
		slog.Debug("Failed to proxy request", "error", err, "box", boxName)

		// Determine if the requester is the owner of the box's alloc
		isOwner := false
		if userID, ok := s.getAuthenticatedUserID(r); ok {
			if alloc, aerr := s.getUserAlloc(r.Context(), userID); aerr == nil && alloc != nil {
				if box.AllocID == alloc.AllocID {
					isOwner = true
				}
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
				BoxName     string
				Port        int
				TerminalURL string
			}{
				BoxName:     boxName,
				Port:        route.Port,
				TerminalURL: fmt.Sprintf("%s://%s/", scheme, terminalHost),
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
	// Remove server port if present, but keep box:port format intact
	hostname := host
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		// Only remove if this looks like a server port (has domain before :)
		if strings.Contains(host[:idx], ".") {
			hostname = host[:idx]
		}
	}

	// Check for "box:port" format (no domain, just box:port)
	if strings.Contains(hostname, ":") && !strings.Contains(hostname, ".") {
		parts := strings.Split(hostname, ":")
		if len(parts) == 2 {
			// Validate that it's a reasonable box name and port
			boxName := parts[0]
			// Exclude main domain names
			if boxName != "" && !strings.Contains(boxName, ".") && boxName != "localhost" && boxName != "exe.dev" {
				if _, err := strconv.Atoi(parts[1]); err == nil {
					return true
				}
			}
		}
	}

	// Check for production pattern: *.exe.dev (but not just exe.dev)
	if strings.HasSuffix(hostname, ".exe.dev") && hostname != "exe.dev" {
		return true
	}

	// Check for dev pattern: *.localhost (but not just localhost)
	if strings.HasSuffix(hostname, ".localhost") && hostname != "localhost" {
		return true
	}

	return false
}

// parseProxyHostname extracts box and team names from hostname.
// Supports both box.team.exe.dev and box.team.localhost formats.
// Returns an empty string if hostname is not a valid proxy hostname.
func (s *Server) parseProxyHostname(hostname string) (box string) {
	// Remove domain suffix based on dev mode
	expectedDomain := s.getMainDomain()
	expectedSuffix := "." + expectedDomain
	hostname, hadSuffix := strings.CutSuffix(hostname, expectedSuffix)
	if !hadSuffix || hostname == "" || strings.Contains(hostname, ".") {
		return ""
	}

	return hostname
}

// getAuthenticatedUserID checks if the user is authenticated and returns their userID
// Returns (userID, true) if authenticated, ("") if not authenticated
func (s *Server) getAuthenticatedUserID(r *http.Request) (string, bool) {
	userID, err := s.validateProxyAuthCookie(r)
	if err != nil {
		return "", false
	}

	return userID, true
}

// getMainDomain returns the main domain based on dev mode
func (s *Server) getMainDomain() string {
	if s.devMode != "" {
		return "localhost"
	}
	return "exe.dev"
}

// getMainDomainWithPort returns the main domain with port for redirects
func (s *Server) getMainDomainWithPort() string {
	if s.devMode != "" {
		if s.httpLn.tcp != nil {
			port := s.httpLn.tcp.Port
			return fmt.Sprintf("localhost:%d", port)
		}
		return "localhost"
	}
	return "exe.dev"
}

// getProxyPorts returns the list of ports that should be used for proxying
func (s *Server) getProxyPorts() []int {
	if s.devMode != "" {
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
	// Production mode: specific ports
	// TODO: Should we listen to all ports 2000-10000?
	return []int{
		2000, 2080,
		3000, 3080,
		4000, 4080,
		5000, 5080,
		6000, 6080,
		7000, 7080,
		8000, 8001, 8002, 8003, 8004, 8005, 8006, 8007, 8008, 8080, 8088, 8888,
		9000, 9080, 9999,
	}
}

// isDefaultServerPort returns true if the port should use the box's default route
// This includes port 443 (HTTPS) and the server's main HTTP port
func (s *Server) isDefaultServerPort(port int) bool {
	// Port 443 always uses default route
	if port == 443 {
		return true
	}

	// Check if it matches the server's main HTTP port
	if s.httpLn != nil && s.httpLn.tcp != nil && s.httpLn.tcp.Port == port {
		return true
	}

	return false
}

// redirectToAuth redirects the user to authentication
func (s *Server) redirectToAuth(w http.ResponseWriter, r *http.Request) {
	// Create auth URL with redirect parameter
	authURL := fmt.Sprintf("/auth?redirect=%s", url.QueryEscape(r.URL.String()))

	// If we're on a subdomain, redirect to the main domain
	if r.Host != "" {
		mainDomain := s.getMainDomainWithPort()

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		// Include the return host so we can come back after auth
		authURL = fmt.Sprintf("%s://%s%s&return_host=%s", scheme, mainDomain, authURL, url.QueryEscape(r.Host))
	}

	slog.Debug("[REDIRECT] redirectToAuth", "from", r.Host+r.URL.Path, "to", authURL)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// handleMagicAuth handles the magic authentication URL /__exe.dev/auth

func (s *Server) handleMagicAuth(w http.ResponseWriter, r *http.Request) {
	secret := r.URL.Query().Get("secret")
	redirectURL := r.URL.Query().Get("redirect")

	slog.Debug("[REDIRECT] handleMagicAuth called", "host", r.Host, "secret", secret[:min(10, len(secret))]+"...", "redirect", redirectURL)

	if secret == "" {
		http.Error(w, "Missing secret parameter", http.StatusBadRequest)
		return
	}

	// Validate and consume the magic secret
	magicSecret, err := s.validateMagicSecret(secret)
	if err != nil {
		slog.Debug("[REDIRECT] Magic secret validation failed", "error", err)
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

	slog.Debug("[REDIRECT] handleMagicAuth redirecting", "to", finalRedirect)
	http.Redirect(w, r, finalRedirect, http.StatusTemporaryRedirect)
}

// handleProxyLogout handles the logout URL /__exe.dev/logout
func (s *Server) handleProxyLogout(w http.ResponseWriter, r *http.Request) {
	slog.Debug("[REDIRECT] handleProxyLogout called", "host", r.Host)

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
			slog.Error("Failed to delete specific proxy auth cookie from database", "error", err)
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
	mainDomain := s.getMainDomainWithPort()
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	logoutURL := fmt.Sprintf("%s://%s/logged-out", scheme, mainDomain)
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
	// TODO: do this in a single query instead of two separate ones
	alloc, err := s.getUserAlloc(ctx, userID)
	if err != nil || alloc == nil {
		return nil, fmt.Errorf("user has no allocation")
	}

	box, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.GetBoxByNameAndAlloc(ctx, exedb.GetBoxByNameAndAllocParams{
			Name:    boxName,
			AllocID: alloc.AllocID,
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

	// Get the allocation to find the ctrhost
	ctrhost, err := withRxRes(s, r.Context(), func(ctx context.Context, queries *exedb.Queries) (string, error) {
		return queries.GetCtrhostByAllocID(ctx, box.AllocID)
	})
	if err != nil {
		return fmt.Errorf("failed to get allocation ctrhost: %w", err)
	}

	// Determine SSH host address from the allocation's ctrhost
	sshHost := "localhost"
	if ctrhost != "" {
		// Extract hostname from ctrhost URL if it's a URL format
		if strings.Contains(ctrhost, "://") {
			if u, err := url.Parse(ctrhost); err == nil && u.Host != "" {
				if host, _, err := net.SplitHostPort(u.Host); err == nil {
					sshHost = host
				} else {
					sshHost = u.Host
				}
			}
		} else {
			// Direct hostname
			sshHost = ctrhost
		}
	}
	// In dev, if the host doesn't resolve (e.g., lima alias), resolve via SSH config to an IP.
	if s.devMode != "" {
		if _, err := net.LookupHost(sshHost); err != nil {
			if ip := ctrhosttest.ResolveHostFromSSHConfig(sshHost); ip != "" {
				slog.Debug("Resolved host via SSH config for dev", "alias", sshHost, "ip", ip)
				sshHost = ip
			}
		}
	}

	// Try to proxy to the configured port
	err = s.proxyViaSSHPortForward(w, r, sshHost, box, sshKey, route.Port)
	if err != nil {
		return fmt.Errorf("failed to proxy to port %d: %w", route.Port, err)
	}

	return nil
}

// sshConn wraps a net.Conn obtained via an SSH client and ensures the SSH
// client is closed when the connection is closed.
type sshConn struct {
	net.Conn
	client *ssh.Client
}

func (c *sshConn) Close() error {
	// Close the underlying connection first, then the SSH client.
	if c.Conn != nil {
		_ = c.Conn.Close()
	}
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

// sshDialer implements DialContext by establishing an SSH client connection
// and then dialing the provided addr through that SSH connection.
type sshDialer struct {
	sshAddr string
	cfg     *ssh.ClientConfig
}

func (d *sshDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	// Respect context deadline for the initial TCP connect to the SSH server.
	var deadline time.Time
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	} else {
		deadline = time.Now().Add(30 * time.Second)
	}

	// Establish TCP connection to the SSH server
	dialer := &net.Dialer{Deadline: deadline}

	// Note: The old code combined TCP dial + SSH handshake together. We split them here
	// but use the same retry pattern for the SSH handshake part.
	tcpConn, err := dialer.DialContext(ctx, "tcp", d.sshAddr)
	if err != nil {
		return nil, err
	}

	// Perform SSH handshake on the established TCP connection with retries
	var client *ssh.Client
	var sshErrs error
	sshRetries := []time.Duration{
		100 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond,
		1 * time.Second, 1 * time.Second,
		2 * time.Second, 3 * time.Second,
		0,
	}

	for i, wait := range sshRetries {
		cconn, chans, reqs, err := ssh.NewClientConn(tcpConn, d.sshAddr, d.cfg)
		if err == nil {
			client = ssh.NewClient(cconn, chans, reqs)
			break
		}
		sshErrs = errors.Join(sshErrs, err)
		if wait > 0 {
			select {
			case <-ctx.Done():
				_ = tcpConn.Close()
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		} else if i == len(sshRetries)-1 {
			_ = tcpConn.Close()
			return nil, fmt.Errorf("failed SSH handshake with %s: %w", d.sshAddr, sshErrs)
		}
	}

	// Dial the target address through the SSH connection with retries
	var remoteConn net.Conn
	var remoteErrs error
	remoteRetries := []time.Duration{
		0, 100 * time.Millisecond, 200 * time.Millisecond,
		500 * time.Millisecond, 1 * time.Second, 2 * time.Second, 0,
	}

	for i, wait := range remoteRetries {
		var err error
		remoteConn, err = client.Dial(network, addr)
		if err == nil {
			break
		}
		remoteErrs = errors.Join(remoteErrs, err)
		if wait > 0 {
			select {
			case <-ctx.Done():
				_ = client.Close()
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		} else if i == len(remoteRetries)-1 {
			_ = client.Close()
			return nil, fmt.Errorf("failed to dial remote %s via SSH: %w", addr, remoteErrs)
		}
	}

	return &sshConn{Conn: remoteConn, client: client}, nil
}

// proxyViaSSHPortForward establishes an SSH connection and proxies the HTTP request directly
func (s *Server) proxyViaSSHPortForward(w http.ResponseWriter, r *http.Request, sshHost string, box *exedb.Box, sshKey ssh.Signer, targetPort int) error {
	// Build SSH client config
	cfg := &ssh.ClientConfig{
		User:            *box.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(sshKey)},
		HostKeyCallback: box.CreateHostKeyCallback(),
		Timeout:         30 * time.Second,
	}

	sshAddr := net.JoinHostPort(sshHost, strconv.Itoa(int(*box.SSHPort)))

	// Build an HTTP transport that dials through SSH to the target on the SSH host.
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&sshDialer{sshAddr: sshAddr, cfg: cfg}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	// Configure the reverse proxy using NewSingleHostReverseProxy
	targetURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", targetPort))
	rp := httputil.NewSingleHostReverseProxy(targetURL)
	rp.Transport = transport

	// Customize the director to add user headers and remove auth cookie
	defaultDirector := rp.Director
	rp.Director = func(req *http.Request) {
		defaultDirector(req)

		// Add user info headers if authenticated
		if userID, ok := s.getAuthenticatedUserID(r); ok {
			email, err := withRxRes(s, req.Context(), func(ctx context.Context, queries *exedb.Queries) (string, error) {
				return queries.GetEmailByUserID(ctx, userID)
			})
			if err != nil {
				slog.Error("failed to get user email for authenticated proxy headers", "error", err)
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
		slog.Debug("HTTP proxy error", "error", err, "target_port", targetPort)
		proxyErr = err
	}

	// Proxy the request
	rp.ServeHTTP(w, r)
	return proxyErr
}
