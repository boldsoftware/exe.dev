package exeweb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"exe.dev/container"
	"exe.dev/domz"
	"exe.dev/email"
	"exe.dev/metricsbag"
	"exe.dev/publicips"
	"exe.dev/sshkey"
	"exe.dev/sshpool2"
	"exe.dev/stage"
	"exe.dev/tracing"
	"exe.dev/webstatic"

	sloghttp "github.com/samber/slog-http"
	"golang.org/x/crypto/ssh"
)

// ProxyServer handles proxy requests for both exed and exeprox.
// Data is handled via the ProxyData interface,
// which for exed talks to the database and for exeprox talks to exed.
type ProxyServer struct {
	Data         ProxyData
	Lg           *slog.Logger
	Env          *stage.Env
	HTTPPort     int // zero if not serving HTTP
	HTTPSPort    int // zero if not serving HTTPS
	PiperdPort   int
	SSHPool      *sshpool2.Pool
	HTTPMetrics  *HTTPMetrics
	Templates    *template.Template
	LobbyIP      netip.Addr
	PublicIPs    map[netip.Addr]publicips.PublicIP
	MagicSecrets *MagicSecrets

	// For testing:
	LookupCNAMEFunc func(context.Context, string) (string, error)
	LookupAFunc     func(context.Context, string, string) ([]netip.Addr, error)
}

// HandleProxyRequest handles requests that should be proxied to containers
// This handler is called when the Host header matches box.exe.dev or box.exe.local
func (ps *ProxyServer) HandleProxyRequest(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Ensure the port in the Host header matches the listener's local port
	conn, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		ps.Lg.ErrorContext(r.Context(), "Failed to get local address from request context")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	_, localPortStr, err := net.SplitHostPort(conn.String())
	if err != nil {
		ps.Lg.ErrorContext(r.Context(), "Failed to parse local address", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	localPort, err := strconv.Atoi(localPortStr)
	if err != nil {
		ps.Lg.ErrorContext(r.Context(), "Failed to convert local port to integer", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	hostHeaderPort := 0
	hostHeaderHost, hostPortStr, err := net.SplitHostPort(r.Host)
	if err != nil {
		// No port in Host header, that's fine if it's the default port which only
		// happens in HTTPS land...
		hostHeaderHost = r.Host
		if ps.HTTPSPort != 0 {
			hostHeaderPort = ps.HTTPSPort
		} else {
			ps.Lg.WarnContext(r.Context(), "Host header didn't have port but we're not using default ports", "host", r.Host, "error", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	} else {
		hostHeaderPort, err = strconv.Atoi(hostPortStr)
		if err != nil {
			ps.Lg.WarnContext(r.Context(), "Failed to convert host port to integer", "host", r.Host, "error", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}
	if hostHeaderPort != localPort {
		ps.Lg.WarnContext(r.Context(), "Host header port mismatch", "host_port", hostHeaderPort, "local_port", localPort)
		http.Error(w, "internal server error", http.StatusBadRequest)
		return
	}

	// Handle magic URL for authentication
	if r.URL.Path == "/__exe.dev/auth" {
		ps.Lg.InfoContext(r.Context(), "[REDIRECT] Magic auth URL accessed", "host", r.Host, "path", r.URL.Path)
		ps.HandleMagicAuth(w, r)
		return
	}

	// Handle login URL
	if r.URL.Path == "/__exe.dev/login" {
		ps.HandleProxyLogin(w, r)
		return
	}

	// Handle logout URL
	if r.URL.Path == "/__exe.dev/logout" {
		ps.Lg.InfoContext(r.Context(), "[REDIRECT] Logout URL accessed", "host", r.Host, "path", r.URL.Path)
		ps.HandleProxyLogout(w, r)
		return
	}

	// Handle request-access URL
	if r.URL.Path == "/__exe.dev/request-access" {
		ps.HandleRequestAccess(w, r)
		return
	}

	// Reserve the /__exe.dev/ prefix — don't forward unknown paths to VMs.
	if strings.HasPrefix(r.URL.Path, "/__exe.dev/") || r.URL.Path == "/__exe.dev" {
		http.NotFound(w, r)
		return
	}

	// Parse hostname to extract box name and optional explicit target port
	boxName, err := ps.domainResolver().ResolveBoxName(r.Context(), hostHeaderHost)
	if err != nil {
		ps.Lg.WarnContext(r.Context(), "Failed to resolve box name", "host", r.Host, "error", err)
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
	// Careful: we aren't checking the team or owner in this look-up,
	// so we must do it below.
	box, exists, err := ps.Data.BoxInfo(r.Context(), boxName)
	if err != nil {
		ps.Lg.ErrorContext(r.Context(), "Failed to look up box", "error", err, "box_name", boxName, "elapsed", time.Since(start).Round(time.Millisecond))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !exists {
		// Box doesn't exist - show 401 to avoid leaking existence
		ps.RenderAccessRequired(w, r, nil)
		return
	}

	// Check if box owner is locked out -
	// their VMs should not accept proxy requests (fail closed on DB error).
	isLockedOut, lockoutErr := ps.Data.IsUserLockedOut(r.Context(), box.CreatedByUserID)
	if lockoutErr != nil {
		ps.Lg.ErrorContext(r.Context(), "failed to check owner lockout status", "error", lockoutErr, "user_id", box.CreatedByUserID)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if isLockedOut {
		sloghttp.AddCustomAttributes(r, slog.Bool("owner_locked_out", true))
		sloghttp.AddCustomAttributes(r, slog.String("owner_user_id", box.CreatedByUserID))
		ps.RenderAccessRequired(w, r, nil)
		return
	}

	// Determine final route:
	// - Shelley subdomain (box.shelley.exe.xyz)
	//   always routes to port 9999 as private;
	// - If no explicit targetPort (0),
	//   or it matches server default ports,
	//   or equals box's default, use box route;
	// - Otherwise create an ad-hoc private route for the requested port.
	var route BoxRoute
	boxRoute := box.BoxRoute
	targetPort := hostHeaderPort
	if IsShelleyRequest(ps.Env, r.Host) {
		route = BoxRoute{Port: 9999, Share: "private"}
	} else if targetPort == 0 || targetPort == boxRoute.Port || ps.isDefaultServerPort(targetPort) {
		route = boxRoute
	} else {
		route = BoxRoute{Port: targetPort, Share: "private"}
	}

	if route.Port == 9999 {
		// We're going to call all proxy requests to port 9999
		// shelley requests. We could look for /api/conversation/* or
		// something, this seems fine for purposes of tracking/logging.
		sloghttp.AddCustomAttributes(r, slog.Bool("proxy_shelley", true))
	}

	// Apply authentication based on route share setting
	var authResult *ProxyAuthResult
	if route.Share == "private" {
		// Check if user is authenticated
		// (cookie, Bearer token, or Basic auth).
		authResult = ps.GetProxyAuth(r, box.Name)
		if authResult == nil {
			// Not authenticated by any method.
			// If the request has an Authorization header,
			// it's an API client;
			// return 401 instead of redirecting to the login page.
			if r.Header.Get("Authorization") != "" {
				w.Header().Set("WWW-Authenticate", "Bearer, Basic")
				http.Error(w, "invalid or missing authentication", http.StatusUnauthorized)
				return
			}
			// Browser client - redirect to auth flow.
			ps.RedirectToAuth(w, r)
			return
		}
		userID := authResult.UserID

		// Set user ID for HTTP logging
		sloghttp.AddCustomAttributes(r, slog.String("user_id", userID))

		// User is authenticated - check if they have access
		hasAccess := false

		// Check access
		accessType, err := ps.HasUserAccessToBox(r.Context(), userID, &box)
		if err == nil {
			switch accessType {
			case BoxAccessOwner, BoxAccessEmailShare, BoxAccessTeamShare:
				hasAccess = true
			}
		}

		// Check share link access
		if !hasAccess && ps.CheckShareLinkAccess(r, box.ID, box.Name, userID) {
			hasAccess = true
		}

		// Check support access: user is root support
		// and box has support_access_allowed
		if !hasAccess && box.SupportAccessAllowed == 1 && ps.UserHasExeSudo(r.Context(), userID) {
			ps.Lg.InfoContext(r.Context(), "proxy support access granted", "box", boxName, "user_id", userID)
			hasAccess = true
		}

		if !hasAccess {
			// User is authenticated but doesn't have access
			// Show 401 to avoid leaking box existence
			ps.RenderAccessRequired(w, r, &box)
			return
		}

		// Track unique users for private proxy access
		events := []string{"proxy"}
		if route.Port == 9999 {
			events = append(events, "shelley-proxy")
		}
		// Track login-with-exe:
		// user accessing someone else's box (not owner).
		if accessType != BoxAccessOwner {
			events = append(events, "login-with-exe")
		}
		ps.Data.HLLNoteEvents(r.Context(), userID, events)
	}

	// Handle debug path in dev/test environments
	if r.URL.Path == "/__exe.dev/debug" && ps.Env.WebDev {
		// Show debug info for /__exe.dev/debug in dev mode
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "Proxy handler - Route matched!\n")
		fmt.Fprintf(w, "Box: %s\n", boxName)
		fmt.Fprintf(w, "Route port: %d\n", route.Port)
		fmt.Fprintf(w, "Route share: %s\n", route.Share)
		fmt.Fprintf(w, "Request method: %s\n", r.Method)
		fmt.Fprintf(w, "Request path: %s\n", r.URL.Path)

		// Show current user info
		if userID, err := ps.ValidateProxyAuthCookie(r); err == nil {
			// Ignore error.
			userData, exists, _ := ps.Data.UserInfo(r.Context(), userID)
			userEmail := ""
			if exists {
				userEmail = userData.Email
			}
			fmt.Fprintf(w, "Logged in user: %q (%q)\n", userEmail, userID)
		} else if errors.Is(err, http.ErrNoCookie) {
			fmt.Fprintf(w, "Not logged in\n")
		} else {
			fmt.Fprintf(w, "Invalid auth cookie: %v\n", err)
		}
		return
	}

	// Resolve proxy auth once,
	// reusing the result from the
	// private-route check if available.
	// On public routes this is best-effort:
	// a valid token scoped to this VM
	// will carry its ctx through,
	// but a token for a different VM simply fails
	// namespace validation and produces no auth result
	// (no cross-container leak).
	if authResult == nil {
		authResult = ps.GetProxyAuth(r, box.Name)
	}

	// Proxy the request to the container
	err = ps.ProxyToContainer(w, r, &box, route, authResult)
	if err != nil {
		ps.Lg.DebugContext(r.Context(), "Failed to proxy request", "error", err, "box", boxName)

		// Determine if the requester is the owner of the box.
		isOwner := false
		if userID, err := ps.ValidateProxyAuthCookie(r); err == nil {
			if box.CreatedByUserID == userID {
				isOwner = true
			}
		}

		if isOwner {
			// Render owner-facing help page
			data := struct {
				*stage.Env
				BoxName         string
				BoxDest         func(string) string
				SSHCommand      string
				Port            int
				TerminalURL     string
				ShowWelcomeStep bool
				IsShelleyPort   bool
				ShelleyURL      string
			}{
				Env:             ps.Env,
				BoxName:         boxName,
				BoxDest:         ps.Env.BoxDest,
				SSHCommand:      ps.boxSSHConnectionCommand(boxName),
				Port:            route.Port,
				TerminalURL:     ps.xtermURL(boxName, r.TLS != nil),
				ShowWelcomeStep: strings.Contains(box.Image, "exeuntu") && route.Port == 8000,
				IsShelleyPort:   route.Port == 9999,
				ShelleyURL:      ps.shelleyURL(boxName),
			}

			w.WriteHeader(http.StatusBadGateway)
			_ = ps.renderTemplate(r.Context(), w, "proxy-unreachable.html", data)
			return
		}

		// Non-owner: render 503 page
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = ps.renderTemplate(r.Context(), w, "503.html", nil)
		return
	}
}

// domainResolver returns a [DomainResolver] for ps.
func (ps *ProxyServer) domainResolver() *DomainResolver {
	return &DomainResolver{
		Lg:              ps.Lg,
		Env:             ps.Env,
		LobbyIP:         ps.LobbyIP,
		PublicIPs:       ps.PublicIPs,
		LookupCNAMEFunc: ps.LookupCNAMEFunc,
		LookupAFunc:     ps.LookupAFunc,
	}
}

// ProxyAuthResult contains the result of proxy authentication.
type ProxyAuthResult struct {
	UserID string // authenticated user ID

	// CtxRaw is non-nil if we authenticated using a token.
	// The raw bytes are passed to the VM's HTTP server.
	CtxRaw []byte
}

// HandleMagicAuth handles the magic authentication URL /__exe.dev/auth.
func (ps *ProxyServer) HandleMagicAuth(w http.ResponseWriter, r *http.Request) {
	secret := r.URL.Query().Get("secret")
	redirectURL := r.URL.Query().Get("redirect")

	ps.Lg.DebugContext(r.Context(), "[REDIRECT] handleMagicAuth called", "host", r.Host, "secret", secret[:min(10, len(secret))]+"...", "redirect", redirectURL)

	if secret == "" {
		http.Error(w, "Missing secret parameter", http.StatusBadRequest)
		return
	}

	// Validate and consume the magic secret
	magicSecret, err := ps.MagicSecrets.Validate(secret)
	if err != nil {
		ps.Lg.DebugContext(r.Context(), "[REDIRECT] Magic secret validation failed", "error", err)
		http.Error(w, "Invalid or expired secret", http.StatusUnauthorized)
		return
	}

	// Create authentication cookie for this subdomain
	cookieValue, err := ps.Data.CreateAuthCookie(r.Context(), magicSecret.UserID, r.Host)
	if err != nil {
		http.Error(w, "Failed to create authentication cookie", http.StatusInternalServerError)
		return
	}

	// Determine cookie name based on request type (terminal vs proxy)
	var cookieName string
	if IsTerminalRequest(ps.Env, r.Host) {
		cookieName = "exe-auth"
	} else {
		port, err := GetRequestPort(r)
		if err != nil {
			ps.Lg.ErrorContext(r.Context(), "Failed to get port from request", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		cookieName = ProxyAuthCookieName(port)
	}

	SetAuthCookie(w, r, cookieName, cookieValue)

	// Redirect to the original URL or the redirect from the magic secret.
	// Validate to prevent open redirect attacks.
	finalRedirect := redirectURL
	if finalRedirect == "" {
		finalRedirect = magicSecret.RedirectURL
	}
	if !IsValidRedirectURL(finalRedirect) {
		finalRedirect = "/"
	}

	ps.Lg.DebugContext(r.Context(), "[REDIRECT] handleMagicAuth redirecting", "to", finalRedirect)
	http.Redirect(w, r, finalRedirect, http.StatusSeeOther)
}

// HandleProxyLogin handles the login URL /__exe.dev/login.
// It redirects to the main domain auth flow with redirect and
// return_host parameters.
func (ps *ProxyServer) HandleProxyLogin(w http.ResponseWriter, r *http.Request) {
	ps.Lg.DebugContext(r.Context(), "[REDIRECT] handleProxyLogin called", "host", r.Host)

	redirect := r.URL.Query().Get("redirect")
	if !IsValidRedirectURL(redirect) {
		redirect = "/"
	}

	// Use webBaseURLNoRequest to get the main domain URL
	// without copying the request's port.
	// The main domain (exe.dev) always runs on the
	// default HTTPS port (443),
	// even when the proxy request came in on a non-standard port like 9999.
	authURL := fmt.Sprintf("%s/auth?redirect=%s&return_host=%s", ps.webBaseURLNoRequest(), url.QueryEscape(redirect), url.QueryEscape(r.Host))

	ps.Lg.DebugContext(r.Context(), "[REDIRECT] handleProxyLogin redirecting to main domain", "to", authURL)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// HandleProxyLogout handles the logout URL /__exe.dev/logout.
// GET: renders a simple confirmation form.
// POST: performs the logout and redirects to logged-out page.
func (ps *ProxyServer) HandleProxyLogout(w http.ResponseWriter, r *http.Request) {
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
	port, err := GetRequestPort(r)
	if err != nil {
		ps.Lg.ErrorContext(r.Context(), "Failed to get port from request for logout", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	cookieName := ProxyAuthCookieName(port)

	// Get the specific cookie value to delete.
	var cookieValue string
	cookie, err := r.Cookie(cookieName)
	if err == nil && cookie.Value != "" {
		cookieValue = cookie.Value
	}

	// Delete only this specific cookie from the database.
	if cookieValue != "" {
		if err := ps.Data.DeleteAuthCookie(r.Context(), cookieValue); err != nil {
			ps.Lg.ErrorContext(r.Context(), "deleting auth cookie failed", "cookieValue", cookieValue, "error", err)
		}
	}

	// Clear the proxy auth cookie in the browser.
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	// Redirect to logged out page on main domain.
	logoutURL := fmt.Sprintf("%s/logged-out", ps.webBaseURLNoRequest())
	http.Redirect(w, r, logoutURL, http.StatusTemporaryRedirect)
}

// GetProxyAuth checks if the user is authenticated for the proxy
// and returns the auth result.
// Supports three authentication methods, tried in this order:
//  1. Bearer token auth (Authorization: Bearer <token>)
//  2. Basic auth with token as password (for git HTTPS, etc.)
//  3. Cookie-based auth (login-with-exe-* cookies)
//
// For token-based auth, the namespace must be "v0@VMNAME.BOXHOST".
// Returns nil if not authenticated.
func (ps *ProxyServer) GetProxyAuth(r *http.Request, boxName string) *ProxyAuthResult {
	// 1. Try Bearer token auth.
	// RFC 7235: auth scheme is case-insensitive.
	if auth := r.Header.Get("Authorization"); len(auth) >= len("Bearer ") && strings.EqualFold(auth[:len("Bearer ")], "Bearer ") {
		token := auth[len("Bearer "):]
		if result := ps.ValidateVMToken(r.Context(), token, boxName); result != nil {
			return result
		}
	}

	// 2. Try Basic auth (password is the token, username is ignored).
	// This supports git HTTPS and other tools that use basic auth.
	if _, password, ok := r.BasicAuth(); ok {
		if result := ps.ValidateVMToken(r.Context(), password, boxName); result != nil {
			return result
		}
	}

	// 3. Fall back to cookie-based auth.
	if userID, err := ps.ValidateProxyAuthCookie(r); err == nil {
		return &ProxyAuthResult{UserID: userID}
	}

	return nil
}

// ValidateVMToken validates a token for VM access.
// The namespace is "v0@VMNAME.BOXHOST" where VMNAME is the box name.
// Returns the auth result if valid, nil otherwise.
func (ps *ProxyServer) ValidateVMToken(ctx context.Context, token, boxName string) *ProxyAuthResult {
	namespace := "v0@" + boxName + "." + ps.Env.BoxHost
	result, err := ps.validateToken(ctx, token, namespace)
	if err != nil {
		ps.Lg.DebugContext(ctx, "VM token validation failed", "error", err, "box", boxName)
		return nil
	}
	return &ProxyAuthResult{
		UserID: result.UserID,
		CtxRaw: result.CtxRaw,
	}
}

// validateToken validates an SSH-signed token and
// returns the user ID and payload.
func (ps *ProxyServer) validateToken(ctx context.Context, token, namespace string) (*sshkey.TokenResult, error) {
	tr, err := sshkey.ValidateToken(ctx, ps.Lg, token, namespace, ps.Data.GetSSHKeyByFingerprint)
	if err != nil {
		return nil, err
	}

	isLockedOut, err := ps.Data.IsUserLockedOut(ctx, tr.UserID)
	if err != nil {
		ps.Lg.ErrorContext(ctx, "failed to check lockout status", "error", err, "user_id", tr.UserID)
		return nil, errors.New("invalid token")
	}
	if isLockedOut {
		ps.Lg.WarnContext(ctx, "locked out user attempted token auth", "user_id", tr.UserID, "fingerprint", tr.Fingerprint)
		return nil, errors.New("invalid token")
	}

	return tr, nil
}

// ValidateAuthCookie validates the primary authentication cookie
// and returns the user ID.
func (ps *ProxyServer) ValidateAuthCookie(r *http.Request) (string, error) {
	return ps.validateNamedAuthCookie(r, "exe-auth")
}

// ValidateProxyAuthCookie validates the proxy authentication cookie
// and returns the user_id.
// The cookie name is port-specific: "login-with-exe-<port>".
func (ps *ProxyServer) ValidateProxyAuthCookie(r *http.Request) (string, error) {
	port, err := GetRequestPort(r)
	if err != nil {
		return "", fmt.Errorf("failed to get port from request: %w", err)
	}
	return ps.validateNamedAuthCookie(r, ProxyAuthCookieName(port))
}

func (ps *ProxyServer) validateNamedAuthCookie(r *http.Request, cookieName string) (string, error) {
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
	// Strip port from domain since cookies are per-host, not per-host:port
	domain := domz.StripPort(r.Host)

	// Get auth cookie info
	cookieData, exists, err := ps.Data.CookieInfo(ctx, cookieValue, domain)
	if err != nil {
		return "", fmt.Errorf("database error: %w", err)
	}
	if !exists {
		return "", fmt.Errorf("invalid cookie")
	}

	// Check if cookie has expired.
	if time.Now().After(cookieData.ExpiresAt) {
		// We don't call DeleteAuthCookie here;
		// we expect the CookieInfo method to handle that.
		return "", fmt.Errorf("cookie expired")
	}

	// Update last used time.
	ps.Data.UsedCookie(ctx, cookieValue)

	return cookieData.UserID, nil
}

// CheckShareLinkAccess reports whether the request has a share token
// that permits access to the box. If it does, we record that the
// share token was used, and we create an email-based share.
func (ps *ProxyServer) CheckShareLinkAccess(r *http.Request, boxID int, boxName, userID string) bool {
	shareToken := r.URL.Query().Get("share")
	if shareToken == "" {
		return false
	}

	valid, err := ps.Data.CheckShareLink(r.Context(), boxID, boxName, userID, shareToken)
	// Report but don't return an error.
	if err != nil {
		ps.Lg.ErrorContext(r.Context(), "check share link failed", "boxID", boxID, "boxName", boxName, "userID", userID, "shareToken", shareToken, "error", err)
	}

	return valid
}

// UserHasExeSudo reports whether a user has root support privileges.
func (ps *ProxyServer) UserHasExeSudo(ctx context.Context, userID string) bool {
	valid, err := ps.Data.UserHasExeSudo(ctx, userID)
	if err != nil {
		// Report but don't return an error.
		ps.Lg.ErrorContext(ctx, "UserHasExeSudo error", "userID", userID, "error", err)
	}
	return valid
}

// ProxyToContainer proxies an HTTP request to a container
// via SSH port forwarding.
func (ps *ProxyServer) ProxyToContainer(w http.ResponseWriter, r *http.Request, box *BoxData, route BoxRoute, authResult *ProxyAuthResult) error {
	// Validate box has SSH credentials
	if len(box.SSHClientPrivateKey) == 0 || box.SSHPort == 0 {
		return fmt.Errorf("VM missing SSH credentials")
	}

	// Parse the SSH private key
	sshKey, err := container.CreateSSHSigner(string(box.SSHClientPrivateKey))
	if err != nil {
		return fmt.Errorf("failed to parse SSH private key: %w", err)
	}

	// Determine SSH host address from the box's ctrhost
	sshHost := BoxSSHHost(ps.Lg, box.Ctrhost)

	// Try to proxy to the configured port
	err = ps.proxyViaSSHPortForward(w, r, sshHost, box, sshKey, route.Port, authResult)
	if err != nil {
		return fmt.Errorf("failed to proxy to port %d: %w", route.Port, err)
	}

	return nil
}

// proxyViaSSHPortForward establishes an SSH connection and proxies the HTTP request directly
func (ps *ProxyServer) proxyViaSSHPortForward(w http.ResponseWriter, r *http.Request, sshHost string, box *BoxData, sshKey ssh.Signer, targetPort int, authResult *ProxyAuthResult) error {
	transport := ps.CreateSSHTunnelTransport(sshHost, box, sshKey)

	// Configure the reverse proxy using NewSingleHostReverseProxy
	targetURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", targetPort))
	rp := httputil.NewSingleHostReverseProxy(targetURL)
	rp.Transport = transport

	// Customize the director to add user headers and remove auth cookie
	defaultDirector := rp.Director
	rp.Director = func(req *http.Request) {
		defaultDirector(req)
		clearExeDevHeaders(req)
		stripExeDevAuth(req)
		setForwardedHeaders(req, r)

		// Add user info headers if authenticated
		if authResult != nil {
			req.Header.Set("X-ExeDev-UserID", authResult.UserID)

			userData, exists, err := ps.Data.UserInfo(req.Context(), authResult.UserID)
			if err != nil {
				ps.Lg.ErrorContext(r.Context(), "failed to get user email for proxy headers", "error", err, "user_id", authResult.UserID)
			} else if exists {
				req.Header.Set("X-ExeDev-Email", userData.Email)
			}

			// If authenticated via token,
			// pass the ctx field as a header.
			// This allows the VM's HTTP server to
			// impose its own auth requirements.
			// We pass the raw bytes verbatim to
			// preserve exact formatting.
			if authResult.CtxRaw != nil {
				req.Header.Set("X-ExeDev-Token-Ctx", string(authResult.CtxRaw))
			}
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
		ps.Lg.DebugContext(r.Context(), "HTTP proxy error", "error", err, "target_port", targetPort)
		proxyErr = err
	}

	// Proxy the request
	rp.ServeHTTP(w, r)
	return proxyErr
}

// CreateSSHTunnelTransport creates an HTTP transport that
// tunnels through SSH to a container.
func (ps *ProxyServer) CreateSSHTunnelTransport(sshHost string, box *BoxData, sshKey ssh.Signer) *http.Transport {
	// Build an HTTP transport that dials through SSH
	// to the target on the SSH host.
	// The sshDialer uses the connection pool for SSH connections.
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			cfg := &ssh.ClientConfig{
				User:            box.SSHUser,
				Auth:            []ssh.AuthMethod{ssh.PublicKeys(sshKey)},
				HostKeyCallback: CreateHostKeyCallback(box.Name, box.SSHServerIdentityKey),
				Timeout:         30 * time.Second,
			}
			// Use a deadline that allows for stale
			// connection recovery:
			// - Each dial attempt is bounded to 500ms
			//   (in dialThroughClient);
			// - If first attempt hits a stale connection,
			//   it times out and is removed;
			// - Retries can establish a fresh connection
			//   (up to 3s for SSH dial).
			// Note: "port not bound" still fails fast
			// since connection refused is immediate.
			ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			conn, err := ps.SSHPool.DialWithRetries(ctx, network, addr, sshHost, box.SSHUser, box.SSHPort, sshKey, cfg, []time.Duration{
				50 * time.Millisecond,
				100 * time.Millisecond,
				200 * time.Millisecond,
			})
			if err != nil {
				return nil, fmt.Errorf("SSH dial failed: %w", err)
			}
			return &countingConn{Conn: conn, metrics: ps.HTTPMetrics}, nil
		},
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// isDefaultServerPort reports whether port should use the box's default route.
// This includes port 443 (HTTPS) and the server's main HTTP port.
func (ps *ProxyServer) isDefaultServerPort(port int) bool {
	// Port 443 always uses default route
	if port == 443 {
		return true
	}

	// Check if it matches the server's main HTTP port
	if ps.HTTPPort != 0 && ps.HTTPPort == port {
		return true
	}

	return false
}

// webBaseURLNoRequest returns a URL for the main web host (exe.dev).
func (ps *ProxyServer) webBaseURLNoRequest() string {
	var scheme, port string
	if ps.HTTPSPort == 0 {
		scheme = "http"
		if ps.HTTPPort != 80 {
			port = fmt.Sprintf(":%d", ps.HTTPPort)
		}
	} else {
		scheme = "https"
		if ps.HTTPSPort != 443 {
			port = fmt.Sprintf(":%d", ps.HTTPSPort)
		}
	}
	return fmt.Sprintf("%s://%s%s", scheme, ps.Env.WebHost, port)
}

// boxsSSHPort returns the port to use on the local host for ssh.
func (ps *ProxyServer) boxSSHPort() int {
	if ps.PiperdPort != 22 {
		return ps.PiperdPort
	}
	return 22
}

// urlPort returns :PORT for use in URLs, according to useTLS.
func (ps *ProxyServer) urlPort(useTLS bool) string {
	if useTLS {
		if ps.HTTPSPort == 0 || ps.HTTPSPort == 443 {
			return ""
		}
		return fmt.Sprintf(":%d", ps.HTTPSPort)
	}
	if ps.HTTPPort == 0 || ps.HTTPPort == 80 {
		return ""
	}
	return fmt.Sprintf(":%d", ps.HTTPPort)
}

// xtermURL returns the terminal URL for a box.
func (ps *ProxyServer) xtermURL(boxName string, useTLS bool) string {
	var scheme, port string
	if useTLS {
		scheme = "https"
		port = ps.urlPort(true)
	} else {
		scheme = "http"
		port = ps.urlPort(false)
	}
	return fmt.Sprintf("%s://%s%s", scheme, ps.Env.BoxXtermSub(boxName), port)
}

// shelleyURL returns the Shelley agent URL for a box (vm.shelley.exe.xyz).
func (ps *ProxyServer) shelleyURL(boxName string) string {
	var scheme, port string
	if ps.HTTPSPort != 0 {
		scheme = "https"
		port = ps.urlPort(true)
	} else {
		scheme = "http"
		port = ps.urlPort(false)
	}
	return fmt.Sprintf("%s://%s%s", scheme, ps.Env.BoxShelleySub(boxName), port)
}

// boxSSHConnectionCommand returns the SSH command to connect to box boxName.
func (ps *ProxyServer) boxSSHConnectionCommand(boxName string) string {
	dashP := ""
	if port := ps.boxSSHPort(); port != 22 {
		dashP = fmt.Sprintf("-p %d ", port)
	}
	return "ssh " + dashP + ps.Env.BoxDest(boxName)
}

// UnauthorizedData holds the template data for the 401.html page.
type UnauthorizedData struct {
	Email          string
	AuthURL        string
	RedirectURL    string
	ReturnHost     string
	LoginWithExe   bool
	InvalidSecret  bool
	InvalidToken   bool
	PasskeyEnabled bool
}

// RenderAccessRequired renders the access required page
// for unauthorized access.
// If the user is authenticated and the box exists,
// it shows the request-access page so they can
// request access from the owner.
// Otherwise it shows the 401 login page
// to avoid leaking box existence information.
func (ps *ProxyServer) RenderAccessRequired(w http.ResponseWriter, r *http.Request, box *BoxData) {
	var userEmail string
	if userID, err := ps.ValidateProxyAuthCookie(r); err == nil {
		userData, exists, err := ps.Data.UserInfo(r.Context(), userID)
		if err != nil {
			ps.Lg.ErrorContext(r.Context(), "fetching user info failed", "userID", userID, "error", err)
		}
		if exists {
			userEmail = userData.Email
		}
	}

	// If the user is authenticated and the box exists,
	// show the request-access page.
	if userEmail != "" && box != nil {
		w.WriteHeader(http.StatusUnauthorized)
		ps.renderTemplate(r.Context(), w, "request-access.html", struct {
			Email string
		}{
			Email: userEmail,
		})
		return
	}

	u := &url.URL{
		Scheme: getScheme(r),
		Host:   r.Host,
		Path:   r.URL.Path,
	}

	data := UnauthorizedData{
		Email:        userEmail,
		AuthURL:      ps.webBaseURLNoRequest() + "/auth",
		RedirectURL:  u.String(),
		ReturnHost:   r.Host,
		LoginWithExe: true,
		// PasskeyEnabled is false:
		// box subdomains can't use passkeys (RPID mismatch).
	}

	w.WriteHeader(http.StatusUnauthorized)
	ps.renderTemplate(r.Context(), w, "401.html", data)
}

// RenderLockedOutPage renders the account-locked page and
// reports whether userID is locked out.
// If there's an error checking lockout status,
// it logs the error and returns false (allows access).
func (ps *ProxyServer) RenderLockedOutPage(w http.ResponseWriter, r *http.Request, userID string) bool {
	ctx := r.Context()
	isLockedOut, err := ps.Data.IsUserLockedOut(ctx, userID)
	if err != nil {
		ps.Lg.WarnContext(ctx, "failed to check user lockout status", "userID", userID, "error", err)
		return false
	}
	if !isLockedOut {
		return false
	}

	traceID := tracing.TraceIDFromContext(ctx)
	ps.Lg.WarnContext(ctx, "locked out user attempted access", "userID", userID, "trace_id", traceID)

	w.WriteHeader(http.StatusForbidden)
	data := struct {
		TraceID string
	}{
		TraceID: traceID,
	}
	if err := ps.renderTemplate(ctx, w, "account-locked.html", data); err != nil {
		ps.Lg.ErrorContext(ctx, "failed to render account-locked template", "error", err)
	}
	return true
}

// HandleRequestAccess handles GET and POST for /__exe.dev/request-access.
// GET renders the request-access form. POST sends an access request email to the box owner.
func (ps *ProxyServer) HandleRequestAccess(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Authenticate the user.
	userID, err := ps.ValidateProxyAuthCookie(r)
	if err != nil {
		ps.RedirectToAuth(w, r)
		return
	}

	userData, exists, err := ps.Data.UserInfo(ctx, userID)
	if err == nil && !exists {
		err = errors.New("no such user")
	}
	if err != nil {
		ps.Lg.ErrorContext(ctx, "request-access: failed to get requester email", "error", err, "user_id", userID)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	requesterEmail := userData.Email

	// Resolve the box from the hostname.
	hostHeaderHost, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		hostHeaderHost = r.Host
	}
	boxName, err := ps.domainResolver().ResolveBoxName(ctx, hostHeaderHost)
	if err != nil || boxName == "" {
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}

	box, exists, err := ps.Data.BoxInfo(ctx, boxName)
	if err != nil {
		ps.Lg.ErrorContext(ctx, "request-access: failed to look up box", "error", err, "box_name", boxName)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.NotFound(w, r)
		return
	}

	if r.Method == http.MethodGet {
		ps.renderTemplate(ctx, w, "request-access.html", struct {
			Email string
		}{
			Email: requesterEmail,
		})
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Rate limit.
	if err := ps.Data.CheckAndIncrementEmailQuota(ctx, userID); err != nil {
		ps.Lg.WarnContext(ctx, "request-access: email quota exceeded", "user_id", userID, "error", err)
		http.Error(w, "Too many requests. Try again later.", http.StatusTooManyRequests)
		return
	}

	// Look up the owner's email.
	ownerUserData, exists, err := ps.Data.UserInfo(ctx, box.CreatedByUserID)
	if err == nil && !exists {
		err = errors.New("no such user")
	}
	if err != nil {
		ps.Lg.ErrorContext(ctx, "request-access: failed to get owner email", "error", err, "owner_user_id", box.CreatedByUserID)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	ownerEmail := ownerUserData.Email

	message := strings.TrimSpace(r.FormValue("message"))

	// Build the share URL for the dashboard.
	shareURL := fmt.Sprintf("%s/?share_vm=%s&share_email=%s",
		ps.webBaseURLNoRequest(),
		url.QueryEscape(boxName),
		url.QueryEscape(requesterEmail),
	)

	subject := fmt.Sprintf("%s is requesting access to %s", requesterEmail, boxName)

	var body strings.Builder
	fmt.Fprintf(&body, "%s is requesting access to your VM %q.\n\n", requesterEmail, boxName)
	if message != "" {
		fmt.Fprintf(&body, "Message: %s\n\n", message)
	}
	fmt.Fprintf(&body, "To grant access, visit:\n%s\n", shareURL)

	if err := ps.Data.SendEmail(ctx, email.TypeAccessRequest, ownerEmail, subject, body.String()); err != nil {
		ps.Lg.ErrorContext(ctx, "request-access: failed to send email", "error", err, "to", ownerEmail)
		http.Error(w, "Failed to send request. Try again later.", http.StatusInternalServerError)
		return
	}

	ps.Lg.InfoContext(ctx, "access request sent", "requester", requesterEmail, "owner", ownerEmail, "box", boxName)
	ps.renderTemplate(ctx, w, "request-sent.html", nil)
}

// RedirectToAuth redirects the user to the /__exe.dev/login URL
// which will then redirect to the main domain auth flow.
func (ps *ProxyServer) RedirectToAuth(w http.ResponseWriter, r *http.Request) {
	// Pass only path+query as the redirect target. The scheme and host
	// are already conveyed via the Host header and return_host parameter,
	// and downstream handlers (handleProxyLogin, handleMagicAuth) validate
	// the redirect with exeweb.IsValidRedirectURL which only allows
	// relative paths.
	redirect := r.URL.Path
	if r.URL.RawQuery != "" {
		redirect += "?" + r.URL.RawQuery
	}

	authURL := makeAuthURL("login", r, url.Values{
		"redirect": {redirect},
	})

	ps.Lg.DebugContext(r.Context(), "[REDIRECT] redirectToAuth", "from", r.URL, "to", authURL)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// renderTemplate generates an HTTP response from a template.
func (ps *ProxyServer) renderTemplate(ctx context.Context, w http.ResponseWriter, templateName string, data any) error {
	w.Header().Set("Content-Type", "text/html")
	var buf bytes.Buffer
	if err := ps.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		ps.Lg.ErrorContext(ctx, "Failed to execute template", "error", err, "template", templateName)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// serveStaticFile serves a file from the embedded static directory.
func (ps *ProxyServer) serveStaticFile(w http.ResponseWriter, r *http.Request, filename string) {
	webstatic.Serve(w, r, ps.Lg, filename)
}

// makeAuthURL returns a specially recognized authentication URL.
func makeAuthURL(typ string, r *http.Request, q url.Values) string {
	return fmt.Sprintf("%s://%s/__exe.dev/%s?%s",
		getScheme(r),
		r.Host,
		typ,
		q.Encode(),
	)
}

// clearExeDevHeaders removes any X-ExeDev-* headers from
// the outbound proxy request.
// This prevents clients from spoofing authentication state via
// custom headers and reserves the entire X-ExeDev- namespace for our use.
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

func stripExeDevAuth(req *http.Request) {
	auth := req.Header.Get("Authorization")
	if auth == "" {
		return
	}
	const bearer = "Bearer "
	if len(auth) >= len(bearer) && strings.EqualFold(auth[:len(bearer)], bearer) {
		if strings.HasPrefix(strings.TrimSpace(auth[len(bearer):]), sshkey.TokenPrefix) {
			req.Header.Del("Authorization")
		}
		return
	}
	if _, password, ok := req.BasicAuth(); ok && strings.HasPrefix(password, sshkey.TokenPrefix) {
		req.Header.Del("Authorization")
	}
}

// setForwardedHeaders ensures downstream services are aware
// of the original request context.
// It sets X-Forwarded-Proto, X-Forwarded-Host, and
// X-Forwarded-For so apps can reconstruct the public URL.
func setForwardedHeaders(outgoing, incoming *http.Request) {
	if outgoing == nil || incoming == nil {
		return
	}

	outgoing.Header.Set("X-Forwarded-Proto", getScheme(incoming))

	if host := incoming.Host; host != "" {
		outgoing.Header.Set("X-Forwarded-Host", host)
	}

	existingXFF := strings.TrimSpace(incoming.Header.Get("X-Forwarded-For"))
	clientIP := ClientIPFromRemoteAddr(incoming.RemoteAddr)
	switch {
	case existingXFF != "" && clientIP != "":
		outgoing.Header.Set("X-Forwarded-For", existingXFF+", "+clientIP)
	case existingXFF != "":
		outgoing.Header.Set("X-Forwarded-For", existingXFF)
	case clientIP != "":
		outgoing.Header.Set("X-Forwarded-For", clientIP)
	}
}

// ClientIPFromRemoteAddr returns the host or IP address of a net address.
func ClientIPFromRemoteAddr(addr string) string {
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

// BoxSSHHost returns the SSH host to use to connect to a box on ctrHost.
func BoxSSHHost(lg *slog.Logger, ctrHost string) string {
	// TODO: maybe this is a url and needs to be stripped.
	// ctrHost is usually tcp://host:port/ of the exelet.
	// The VMs SSH server is mapped to a port (box.sshPort) on the same
	// host as the exelet, so we parse out the host part only.
	if u, err := url.Parse(ctrHost); err == nil && u.Host != "" {
		if host, _, err := net.SplitHostPort(u.Host); err == nil {
			return host
		} else {
			return u.Host
		}
	}
	// This should never happen, but since we're dealing with data
	// from the database, let's avoid crashing for now.
	lg.Error("Box Ctrhost is not a valid URL", "ctrhost", ctrHost)
	return ctrHost
}

// CreateHostKeyCallback creates a proper SSH host key
// validation callback that verifies the presented host key
// against a box's SSH server identity key.
func CreateHostKeyCallback(boxName string, sshServerIdentityKey []byte) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		// Ensure we have an SSH server identity key.
		if len(sshServerIdentityKey) == 0 {
			return fmt.Errorf("no SSH server identity key available for box %s", boxName)
		}

		// Parse the server identity private key to
		// extract the public key.
		privKey, err := ssh.ParsePrivateKey(sshServerIdentityKey)
		if err != nil {
			return fmt.Errorf("failed to parse server identity key for box %s: %w", boxName, err)
		}

		// Compare the keys by comparing their marshaled bytes.
		if !bytes.Equal(key.Marshal(), privKey.PublicKey().Marshal()) {
			return fmt.Errorf("host key mismatch for %s: presented key does not match expected key for box %s", hostname, boxName)
		}

		return nil
	}
}

// ProxyAuthCookieName returns the cookie name for
// proxy authentication on a specific port.
// Each port gets its own cookie to prevent cross-port authentication.
func ProxyAuthCookieName(port int) string {
	return fmt.Sprintf("login-with-exe-%d", port)
}

// GetRequestPort extracts the port number from
// an HTTP request's Host header.
// For requests without an explicit port,
// it returns the default port for the scheme
// (443 for HTTPS, 80 for HTTP).
func GetRequestPort(r *http.Request) (int, error) {
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
