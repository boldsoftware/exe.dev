package exeweb

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"exe.dev/container"
	"exe.dev/domz"
	"exe.dev/sshkey"
	"exe.dev/sshpool2"
	"exe.dev/stage"

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
	SSHPool      *sshpool2.Pool
	HTTPMetrics  *HTTPMetrics
	MagicSecrets *MagicSecrets
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
		if result := ps.Data.ValidateVMToken(r.Context(), token, boxName); result != nil {
			return result
		}
	}

	// 2. Try Basic auth (password is the token, username is ignored).
	// This supports git HTTPS and other tools that use basic auth.
	if _, password, ok := r.BasicAuth(); ok {
		if result := ps.Data.ValidateVMToken(r.Context(), password, boxName); result != nil {
			return result
		}
	}

	// 3. Fall back to cookie-based auth.
	if userID, err := ps.ValidateProxyAuthCookie(r); err == nil {
		return &ProxyAuthResult{UserID: userID}
	}

	return nil
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
