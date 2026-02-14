package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	sloghttp "github.com/samber/slog-http"
	"golang.org/x/crypto/ssh"

	"exe.dev/exedb"
	"exe.dev/exeweb"
	"exe.dev/metricsbag"
	"exe.dev/stage"
)

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
	start := time.Now()

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
			s.slog().ErrorContext(r.Context(), "Host header didn't have port but we're not using default ports", "host", r.Host, "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	} else {
		hostHeaderPort, err = strconv.Atoi(hostPortStr)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to convert host port to integer", "host", r.Host, "error", err)
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

	// Reserve the /__exe.dev/ prefix — don't forward unknown paths to VMs.
	if strings.HasPrefix(r.URL.Path, "/__exe.dev/") || r.URL.Path == "/__exe.dev" {
		http.NotFound(w, r)
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
	metricsbag.SetLabel(r.Context(), exeweb.LabelBox, boxName)

	// Find the box.
	// Careful: we aren't checking the team or owner in this look-up, so we must do it below.
	box, err := withRxRes1(s, r.Context(), (*exedb.Queries).BoxNamed, boxName)
	if errors.Is(err, sql.ErrNoRows) {
		// Box doesn't exist - show 401 to avoid leaking existence
		s.renderAccessRequired(w, r)
		return
	}
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to look up box", "error", err, "box_name", boxName, "elapsed", time.Since(start).Round(time.Millisecond))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Check if box owner is locked out - their VMs should not accept proxy requests (fail closed on DB error).
	isLockedOut, lockoutErr := s.isUserLockedOut(r.Context(), box.CreatedByUserID)
	if lockoutErr != nil {
		s.slog().ErrorContext(r.Context(), "failed to check owner lockout status", "error", lockoutErr, "user_id", box.CreatedByUserID)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if isLockedOut {
		sloghttp.AddCustomAttributes(r, slog.Bool("owner_locked_out", true))
		sloghttp.AddCustomAttributes(r, slog.String("owner_user_id", box.CreatedByUserID))
		s.renderAccessRequired(w, r)
		return
	}

	// Determine final route:
	// - Shelley subdomain (box.shelley.exe.xyz) always routes to port 9999 as private
	// - If no explicit targetPort (0), or it matches server default ports, or equals box's default, use box route
	// - Otherwise create an ad-hoc private route for the requested port
	var route exedb.Route
	boxRoute := box.GetRoute()
	targetPort := hostHeaderPort
	if s.isShelleyRequest(r.Host) {
		route = exedb.Route{Port: 9999, Share: "private"}
	} else if targetPort == 0 || targetPort == boxRoute.Port || s.isDefaultServerPort(targetPort) {
		route = boxRoute
	} else {
		route = exedb.Route{Port: targetPort, Share: "private"}
	}

	if route.Port == 9999 {
		// We're going to call all proxy requests to port 9999
		// shelley requests. We could look for /api/conversation/* or
		// something, this seems fine for purposes of tracking/logging.
		sloghttp.AddCustomAttributes(r, slog.Bool("proxy_shelley", true))
	}

	// Apply authentication based on route share setting
	var authResult *exeweb.ProxyAuthResult
	if route.Share == "private" {
		// Check if user is authenticated (cookie, Bearer token, or Basic auth).
		authResult = s.getProxyAuth(r, box)
		if authResult == nil {
			// Not authenticated by any method.
			// If the request has an Authorization header, it's an API client;
			// return 401 instead of redirecting to the login page.
			if r.Header.Get("Authorization") != "" {
				w.Header().Set("WWW-Authenticate", "Bearer, Basic")
				http.Error(w, "invalid or missing authentication", http.StatusUnauthorized)
				return
			}
			// Browser client - redirect to auth flow.
			s.redirectToAuth(w, r)
			return
		}
		userID := authResult.UserID

		// Set user ID for HTTP logging
		sloghttp.AddCustomAttributes(r, slog.String("user_id", userID))

		// User is authenticated - check if they have access
		hasAccess := false

		// Check access
		accessType, err := s.hasUserAccessToBox(r.Context(), userID, &box)
		if err == nil && (accessType == BoxAccessOwner || accessType == BoxAccessEmailShare || accessType == BoxAccessTeamShare) {
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
		if !hasAccess && s.FindBoxForExeSudoer(r.Context(), userID, boxName) != nil {
			s.slog().InfoContext(r.Context(), "proxy support access granted", "box", boxName, "user_id", userID)
			hasAccess = true
		}

		if !hasAccess {
			// User is authenticated but doesn't have access
			// Show 401 to avoid leaking box existence
			s.renderAccessRequired(w, r)
			return
		}

		// Track unique users for private proxy access
		if s.hllTracker != nil {
			s.hllTracker.NoteEvent("proxy", userID)
			if route.Port == 9999 {
				s.hllTracker.NoteEvent("shelley-proxy", userID)
			}
			// Track login-with-exe: user accessing someone else's box (not owner)
			if accessType != BoxAccessOwner {
				s.hllTracker.NoteEvent("login-with-exe", userID)
			}
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

	// Resolve proxy auth once, reusing the result from the private-route check if available.
	// On public routes this is best-effort: a valid token scoped to this VM
	// will carry its ctx through, but a token for a different VM simply fails
	// namespace validation and produces no auth result (no cross-container leak).
	if authResult == nil {
		authResult = s.getProxyAuth(r, box)
	}

	// Proxy the request to the container
	err = s.proxyToContainer(w, r, &box, route, authResult)
	if err != nil {
		s.slog().DebugContext(r.Context(), "Failed to proxy request", "error", err, "box", boxName)

		// Determine if the requester is the owner of the box
		isOwner := false
		if userID, ok := s.getAuthenticatedUserID(r); ok {
			if box.CreatedByUserID == userID {
				isOwner = true
			}
		}

		if isOwner {
			// Render owner-facing help page
			data := struct {
				stage.Env
				BoxName         string
				BoxDest         func(string) string
				SSHCommand      string
				Port            int
				TerminalURL     string
				ShowWelcomeStep bool
				IsShelleyPort   bool
				ShelleyURL      string
			}{
				Env:             s.env,
				BoxName:         boxName,
				BoxDest:         s.env.BoxDest,
				SSHCommand:      s.boxSSHConnectionCommand(boxName),
				Port:            route.Port,
				TerminalURL:     s.xtermURL(boxName, r.TLS != nil),
				ShowWelcomeStep: strings.Contains(box.Image, "exeuntu") && route.Port == 8000,
				IsShelleyPort:   route.Port == 9999,
				ShelleyURL:      s.shelleyURL(boxName),
			}

			w.WriteHeader(http.StatusBadGateway)
			_ = s.renderTemplate(r.Context(), w, "proxy-unreachable.html", data)
			return
		}

		// Non-owner: render 503 page
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = s.renderTemplate(r.Context(), w, "503.html", nil)
		return
	}
}

// isProxyRequest reports whether a request to host should be handled by the proxy.
// The proxy handles requests to VMs, which are can single subdomains of the box domain,
// or third party domains pointing here.
func (s *Server) isProxyRequest(host string) bool {
	return exeweb.IsProxyRequest(&s.env, s.tsDomain, host)
}

// isShelleyRequest determines if a request is for a Shelley subdomain (vm.shelley.exe.xyz)
func (s *Server) isShelleyRequest(host string) bool {
	return exeweb.IsShelleyRequest(&s.env, host)
}

// getAuthenticatedUserID checks if the user is authenticated and returns their userID
// Returns (userID, true) if authenticated, ("", false) if not authenticated.
// It may be called multiple times while handling a single request,
// so it should not mutate r or have other side-effects.
// Note: This only checks cookie-based auth. For full auth including tokens, use getProxyAuth.
func (s *Server) getAuthenticatedUserID(r *http.Request) (string, bool) {
	if userID, err := s.validateProxyAuthCookie(r); err == nil {
		return userID, true
	}
	return "", false
}

// getProxyAuth checks if the user is authenticated for the proxy and returns the auth result.
// Supports three authentication methods, tried in this order:
//  1. Bearer token auth (Authorization: Bearer <token>)
//  2. Basic auth with token as password (for git HTTPS, etc.)
//  3. Cookie-based auth (login-with-exe-* cookies)
//
// For token-based auth, the namespace must be "v0@VMNAME.BOXHOST".
// Returns nil if not authenticated.
func (s *Server) getProxyAuth(r *http.Request, box exedb.Box) *exeweb.ProxyAuthResult {
	// 1. Try Bearer token auth.
	// RFC 7235: auth scheme is case-insensitive.
	if auth := r.Header.Get("Authorization"); len(auth) >= len("Bearer ") && strings.EqualFold(auth[:len("Bearer ")], "Bearer ") {
		token := auth[len("Bearer "):]
		if result := s.validateVMToken(r.Context(), token, box.Name); result != nil {
			return result
		}
	}

	// 2. Try Basic auth (password is the token, username is ignored).
	// This supports git HTTPS and other tools that use basic auth.
	if _, password, ok := r.BasicAuth(); ok {
		if result := s.validateVMToken(r.Context(), password, box.Name); result != nil {
			return result
		}
	}

	// 3. Fall back to cookie-based auth.
	if userID, err := s.validateProxyAuthCookie(r); err == nil {
		return &exeweb.ProxyAuthResult{UserID: userID}
	}

	return nil
}

// validateVMToken validates a token for VM access.
// The namespace is "v0@VMNAME.BOXHOST" where VMNAME is the box name.
// Returns the auth result if valid, nil otherwise.
func (s *Server) validateVMToken(ctx context.Context, token, boxName string) *exeweb.ProxyAuthResult {
	namespace := "v0@" + boxName + "." + s.env.BoxHost
	result, err := s.validateToken(ctx, token, namespace)
	if err != nil {
		s.slog().DebugContext(ctx, "VM token validation failed", "error", err, "box", boxName)
		return nil
	}
	return &exeweb.ProxyAuthResult{
		UserID: result.UserID,
		CtxRaw: result.CtxRaw,
	}
}

func (s *Server) webBaseURLNoRequest() string {
	return fmt.Sprintf("%s://%s%s", s.bestScheme(), s.env.WebHost, s.bestURLPort())
}

// getProxyPorts returns the list of ports that should be used for proxying.
// TEST_PROXY_PORTS env var overrides the stage config (used by e1e tests).
func (s *Server) getProxyPorts() []int {
	if testPorts := os.Getenv("TEST_PROXY_PORTS"); testPorts != "" {
		var ports []int
		for portStr := range strings.SplitSeq(testPorts, ",") {
			if port, err := strconv.Atoi(portStr); err == nil {
				ports = append(ports, port)
			}
		}
		return ports
	}
	return s.env.ProxyPorts
}

// isDefaultServerPort reports whether port should use the box's default route.
// This includes port 443 (HTTPS) and the server's main HTTP port.
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
	s.renderTemplate(r.Context(), w, "401.html", data)
}

// redirectToAuth redirects the user to the /__exe.dev/login URL
// which will then redirect to the main domain auth flow
func (s *Server) redirectToAuth(w http.ResponseWriter, r *http.Request) {
	// Pass only path+query as the redirect target. The scheme and host
	// are already conveyed via the Host header and return_host parameter,
	// and downstream handlers (handleProxyLogin, handleMagicAuth) validate
	// the redirect with isValidRedirectURL which only allows relative paths.
	redirect := r.URL.Path
	if r.URL.RawQuery != "" {
		redirect += "?" + r.URL.RawQuery
	}

	authURL := makeAuthURL("login", r, url.Values{
		"redirect": {redirect},
	})

	s.slog().DebugContext(r.Context(), "[REDIRECT] redirectToAuth", "from", r.URL, "to", authURL)
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
	magicSecret, err := s.magicSecrets.Validate(secret)
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

	// Redirect to the original URL or the redirect from the magic secret.
	// Validate to prevent open redirect attacks.
	finalRedirect := redirectURL
	if finalRedirect == "" {
		finalRedirect = magicSecret.RedirectURL
	}
	if !isValidRedirectURL(finalRedirect) {
		finalRedirect = "/"
	}

	s.slog().DebugContext(r.Context(), "[REDIRECT] handleMagicAuth redirecting", "to", finalRedirect)
	http.Redirect(w, r, finalRedirect, http.StatusSeeOther)
}

// handleProxyLogin handles the login URL /__exe.dev/login
// It redirects to the main domain auth flow with redirect and return_host parameters
func (s *Server) handleProxyLogin(w http.ResponseWriter, r *http.Request) {
	s.slog().DebugContext(r.Context(), "[REDIRECT] handleProxyLogin called", "host", r.Host)

	redirect := r.URL.Query().Get("redirect")
	if !isValidRedirectURL(redirect) {
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
		s.deleteAuthCookie(r.Context(), cookieValue)
	}

	// Clear the proxy auth cookie in the browser
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
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
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("VM '%s' not found or access denied", boxName)
	}
	if err != nil {
		return nil, fmt.Errorf("database error: %v", err)
	}
	return &box, nil
}

// proxyToContainer proxies the HTTP request to a container via SSH port forwarding
func (s *Server) proxyToContainer(w http.ResponseWriter, r *http.Request, box *exedb.Box, route exedb.Route, authResult *exeweb.ProxyAuthResult) error {
	// Convert to exeweb data formats.
	// This code is temporary until we move more to exeweb.
	exewebBox := dbBoxToExewebBox(box)

	exewebRoute := exeweb.BoxRoute{
		Port:  route.Port,
		Share: route.Share,
	}

	ps := exeweb.ProxyServer{
		Data:        &proxyData{s: s},
		Lg:          s.slog(),
		Env:         &s.env,
		SSHPool:     s.sshPool,
		HTTPMetrics: s.httpMetrics,
	}

	return ps.ProxyToContainer(w, r, &exewebBox, exewebRoute, authResult)
}

// createSSHTunnelTransport creates an HTTP transport that
// tunnels through SSH to a container.
func (s *Server) createSSHTunnelTransport(sshHost string, box *exedb.Box, sshKey ssh.Signer) *http.Transport {
	// Convert to exeweb data formats.
	// This code is temporary until we move more to exeweb.
	exewebBox := dbBoxToExewebBox(box)

	ps := exeweb.ProxyServer{
		Data:        &proxyData{s: s},
		Lg:          s.slog(),
		Env:         &s.env,
		SSHPool:     s.sshPool,
		HTTPMetrics: s.httpMetrics,
	}

	return ps.CreateSSHTunnelTransport(sshHost, &exewebBox, sshKey)
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

// proxyData implements exeweb.ProxyData using a Server.
type proxyData struct {
	s *Server
}

// BoxInfo implements [exeweb.ProxyData.BoxInfo].
func (pd *proxyData) BoxInfo(ctx context.Context, boxName string) (exeweb.BoxData, bool, error) {
	box, err := exedb.WithRxRes1(pd.s.db, ctx, (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return exeweb.BoxData{}, false, nil
		}
		return exeweb.BoxData{}, false, err
	}
	return dbBoxToExewebBox(&box), true, nil
}

// dbBoxToExewebBox converts a [exedb.Box] to a [exeweb.BoxData].
func dbBoxToExewebBox(box *exedb.Box) exeweb.BoxData {
	exewebBox := exeweb.BoxData{
		ID:                   box.ID,
		Name:                 box.Name,
		Ctrhost:              box.Ctrhost,
		Image:                box.Image,
		SSHServerIdentityKey: box.SSHServerIdentityKey,
		SSHClientPrivateKey:  box.SSHClientPrivateKey,
	}
	if box.SSHPort != nil {
		exewebBox.SSHPort = int(*box.SSHPort)
	}
	if box.SSHUser != nil {
		exewebBox.SSHUser = *box.SSHUser
	}
	r := box.GetRoute()
	exewebBox.BoxRoute = exeweb.BoxRoute{
		Port:  r.Port,
		Share: r.Share,
	}
	return exewebBox
}

// UserInfo implements [exeweb.ProxyData.UserInfo].
func (pd *proxyData) UserInfo(ctx context.Context, userID string) (exeweb.UserData, bool, error) {
	email, err := withRxRes1(pd.s, ctx, (*exedb.Queries).GetEmailByUserID, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return exeweb.UserData{}, false, nil
		}
		return exeweb.UserData{}, false, err
	}
	userData := exeweb.UserData{
		UserID: userID,
		Email:  email,
	}
	return userData, true, nil
}
