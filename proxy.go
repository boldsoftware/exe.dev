package exe

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"exe.dev/container"
	"exe.dev/exedb"
)

// handleProxyRequest handles requests that should be proxied to containers
// This handler is called when the Host header matches box.team.exe.dev or box.team.localhost
func (s *Server) handleProxyRequest(w http.ResponseWriter, r *http.Request) {
	slog.Debug("[REDIRECT] handleProxyRequest called", "host", r.Host, "path", r.URL.Path)
	// Handle magic URL for authentication
	if r.URL.Path == "/__exe.dev/auth" {
		s.handleMagicAuth(w, r)
		return
	}

	// Handle logout URL
	if r.URL.Path == "/__exe.dev/logout" {
		s.handleProxyLogout(w, r)
		return
	}

	// Extract box and team from Host header
	hostname := r.Host
	// Remove port if present
	if idx := strings.LastIndex(hostname, ":"); idx > 0 {
		hostname = hostname[:idx]
	}

	// Parse hostname to extract box name
	boxName, err := s.parseProxyHostname(hostname)
	if err != nil {
		http.Error(w, "Invalid hostname format", http.StatusBadRequest)
		return
	}

	// Find the box
	box, err := s.getBoxByName(r.Context(), boxName)
	if err != nil {
		http.Error(w, "Box not found", http.StatusNotFound)
		return
	}

	// Get the route for the box
	route := box.GetRoute()

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
			http.Error(w, "Forbidden: You do not have access to this box", http.StatusForbidden)
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
		if cookie, err := r.Cookie("exe-proxy-auth"); err == nil && cookie.Value != "" {
			if userID, err := s.validateAuthCookie(r.Context(), cookie.Value, r.Host); err == nil {
				fmt.Fprintf(w, "Logged in user: %s\n", userID)
			} else {
				fmt.Fprintf(w, "Invalid auth cookie: %v\n", err)
			}
		} else {
			fmt.Fprintf(w, "Not logged in\n")
		}
		return
	}

	// Proxy the request to the container
	err = s.proxyToContainer(w, r, box, route)
	if err != nil {
		slog.Debug("Failed to proxy request", "error", err, "box", boxName)
		http.Error(w, "Failed to proxy request to container", http.StatusBadGateway)
		return
	}
}

// isProxyRequest determines if a request should be handled by the proxy
func (s *Server) isProxyRequest(host string) bool {
	// Remove port if present
	hostname := host
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		hostname = host[:idx]
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

// parseProxyHostname extracts box and team names from hostname
// Supports both box.team.exe.dev and box.team.localhost formats
func (s *Server) parseProxyHostname(hostname string) (box string, err error) {
	// Remove domain suffix based on dev mode
	expectedDomain := s.getMainDomain()
	expectedSuffix := "." + expectedDomain
	if strings.HasSuffix(hostname, expectedSuffix) {
		hostname = strings.TrimSuffix(hostname, expectedSuffix)
	} else {
		// Also support the other domain for flexibility
		if s.devMode != "" && strings.HasSuffix(hostname, ".exe.dev") {
			hostname = strings.TrimSuffix(hostname, ".exe.dev")
		} else if s.devMode == "" && strings.HasSuffix(hostname, ".localhost") {
			hostname = strings.TrimSuffix(hostname, ".localhost")
		} else {
			return "", fmt.Errorf("unsupported domain")
		}
	}

	// The remaining part is just the box name
	if hostname == "" || strings.Contains(hostname, ".") {
		return "", fmt.Errorf("invalid box name")
	}

	return hostname, nil
}

// getAuthenticatedUserID checks if the user is authenticated and returns their userID
// Returns (userID, true) if authenticated, ("") if not authenticated
func (s *Server) getAuthenticatedUserID(r *http.Request) (string, bool) {
	// Check for authentication cookie
	cookie, err := r.Cookie("exe-proxy-auth")
	if err != nil || cookie.Value == "" {
		return "", false
	}

	// Validate cookie and get user ID
	userID, err := s.validateAuthCookie(r.Context(), cookie.Value, r.Host)
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

	// Set the auth cookie
	cookie := &http.Cookie{
		Name:     cookieName,
		Value:    cookieValue,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   30 * 24 * 60 * 60, // 30 days
		Secure:   r.TLS != nil,
	}
	http.SetCookie(w, cookie)

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

	// Redirect to the root path of this proxy domain
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

// getBoxForUser retrieves a box for the given user/team/name
func (s *Server) getBoxForUser(ctx context.Context, publicKey, boxName string) (*exedb.Box, error) {
	// Get user from public key
	user, err := s.getUserByPublicKey(ctx, publicKey)
	if err != nil || user == nil {
		return nil, fmt.Errorf("user not found")
	}

	// Get user's alloc
	alloc, err := s.getUserAlloc(ctx, user.UserID)
	if err != nil || alloc == nil {
		return nil, fmt.Errorf("user has no allocation")
	}

	// Get the box
	box, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.GetBoxByNameAndAllocRow, error) {
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

	// Convert to exedb.Box
	exeBox := &exedb.Box{
		ID:              box.ID,
		AllocID:         box.AllocID,
		Name:            box.Name,
		Status:          box.Status,
		Image:           box.Image,
		ContainerID:     box.ContainerID,
		CreatedByUserID: box.CreatedByUserID,
		CreatedAt:       box.CreatedAt,
		UpdatedAt:       box.UpdatedAt,
		LastStartedAt:   box.LastStartedAt,
		Routes:          box.Routes,
	}
	return exeBox, nil
}

// proxyToContainer proxies the HTTP request to a container via SSH port forwarding
func (s *Server) proxyToContainer(w http.ResponseWriter, r *http.Request, box *exedb.Box, route exedb.Route) error {
	// Validate box has SSH credentials
	if len(box.SSHClientPrivateKey) == 0 || box.SSHPort == nil {
		return fmt.Errorf("box missing SSH credentials")
	}

	// In test mode, skip actual SSH connection and just simulate a successful proxy response.
	// TODOX(philip): WTF is test mode.
	if s.testMode {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "Test proxy response from port: %d\n", route.Port)
		return nil
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
	// if hotname starst with lima, use localhost, because limactl does some
	// fancy weird port forwarding
	if s.devMode != "" && strings.HasPrefix(sshHost, "lima") {
		sshHost = "localhost"
	}

	// Try to proxy to the configured port
	err = s.proxyViaSSHPortForward(w, r, sshHost, int(*box.SSHPort), sshKey, route.Port)
	if err != nil {
		return fmt.Errorf("failed to proxy to port %d: %w", route.Port, err)
	}

	return nil
}

// proxyViaSSHPortForward establishes an SSH connection and proxies the HTTP request directly
func (s *Server) proxyViaSSHPortForward(w http.ResponseWriter, r *http.Request, sshHost string, sshPort int, sshKey ssh.Signer, targetPort int) error {
	// Create SSH client config
	sshConfig := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(sshKey),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: Use proper host key validation
		Timeout:         10 * time.Second,
	}

	// Connect to SSH server
	sshAddr := fmt.Sprintf("%s:%d", sshHost, sshPort)
	sshConn, err := ssh.Dial("tcp", sshAddr, sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to SSH server %s: %w", sshAddr, err)
	}
	defer sshConn.Close()

	// Connect directly to the target port inside the container via SSH
	remoteAddr := fmt.Sprintf("127.0.0.1:%d", targetPort)
	remoteConn, err := sshConn.Dial("tcp", remoteAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to port %d in container: %w", targetPort, err)
	}
	defer remoteConn.Close()

	// Create a custom transport that uses the SSH connection
	transport := &sshTransport{
		sshConn:    sshConn,
		remoteAddr: remoteAddr,
	}

	// Create HTTP reverse proxy with custom transport
	targetURL := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("127.0.0.1:%d", targetPort),
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = transport
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Debug("HTTP proxy error", "error", err, "target_port", targetPort)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	// Proxy the request
	proxy.ServeHTTP(w, r)
	return nil
}

// sshTransport implements http.RoundTripper to send HTTP requests through SSH connections
type sshTransport struct {
	sshConn    *ssh.Client
	remoteAddr string
}

// RoundTrip implements the http.RoundTripper interface
func (t *sshTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Connect to the target through SSH
	conn, err := t.sshConn.Dial("tcp", t.remoteAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to establish SSH connection: %w", err)
	}
	defer conn.Close()

	// Write the HTTP request to the connection
	err = req.Write(conn)
	if err != nil {
		return nil, fmt.Errorf("failed to write HTTP request: %w", err)
	}

	// Read the HTTP response from the connection
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return nil, fmt.Errorf("failed to read HTTP response: %w", err)
	}

	return resp, nil
}
