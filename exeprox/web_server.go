package exeprox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	"exe.dev/domz"
	"exe.dev/email"
	"exe.dev/exeweb"
	"exe.dev/metricsbag"

	sloghttp "github.com/samber/slog-http"
)

// ServeHTTP implements http.Handler for the web proxy.
func (wp *WebProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if wp.stopping.Load() {
		http.Error(w, "Server is shutting down", http.StatusServiceUnavailable)
		return
	}

	if target := exeweb.NonProxyRedirect(wp.env, r); target != "" {
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		return
	}

	isProxy := wp.isProxyRequest(r.Host)
	isTerminal := exeweb.IsTerminalRequest(wp.env, r.Host)

	// Add request classication to logs.
	if isProxy {
		sloghttp.AddCustomAttributes(r, slog.Bool("proxy", true))
	}
	if isTerminal {
		sloghttp.AddCustomAttributes(r, slog.Bool("terminal", true))
	}

	ps := &exeweb.ProxyServer{
		Data:          &exewebProxyData{wp: wp},
		Lg:            wp.lg(),
		Env:           wp.env,
		ExedHTTPPort:  wp.exedHTTPPort,
		ExedHTTPSPort: wp.exedHTTPSPort,
		PiperdPort:    0,
		SSHPool:       wp.proxy.sshPool,
		Transports:    wp.transportCache,
		HTTPMetrics:   wp.httpMetrics,
		Templates:     wp.templates,
		LobbyIP:       wp.lobbyIP,
		PublicIPs:     wp.publicIPs,
	}
	if wp.httpServer != nil {
		ps.ProxyHTTPPort = wp.httpLn.port()
	}
	if wp.httpsServer != nil {
		ps.ProxyHTTPSPort = wp.httpsLn.port()
	}

	userID, err := ps.ValidateAuthCookie(r)
	if err == nil {
		sloghttp.AddCustomAttributes(r, slog.String("user_id", userID))
	}

	if isProxy {
		metricsbag.SetLabel(r.Context(), exeweb.LabelProxy, "true")
		ps.HandleProxyRequest(w, r)
		return
	}

	// Non-proxy content (main site, terminal) should only be served
	// on the main port.
	if !wp.isRequestOnMainPort(w, r) {
		return
	}

	if isTerminal {
		metricsbag.SetLabel(r.Context(), exeweb.LabelProxy, "false")
		metricsbag.SetLabel(r.Context(), exeweb.LabelPath, "/terminal")
		ps.HandleTerminalRequest(w, r)
		return
	}

	// Set labels for non-proxy HTTP metrics
	metricsbag.SetLabel(r.Context(), exeweb.LabelProxy, "false")
	metricsbag.SetLabel(r.Context(), exeweb.LabelPath, exeweb.SanitizePath(r.URL.Path))

	// Handle some paths locally.
	switch r.URL.Path {
	case "/health":
		wp.handleHealth(w, r)
		return
	case "/metrics":
		wp.handleMetrics(w, r)
		return
	}

	// This is a web request that we aren't going to handle
	// or proxy to an exelet. Forward to the single exed.
	var scheme, port string
	if r.TLS != nil {
		scheme = "https"
		if wp.exedHTTPSPort != 0 && wp.exedHTTPSPort != 443 {
			port = fmt.Sprintf(":%d", wp.exedHTTPSPort)
		}
	} else {
		scheme = "http"
		if wp.exedHTTPPort != 0 && wp.exedHTTPPort != 80 {
			port = fmt.Sprintf(":%d", wp.exedHTTPPort)
		}
	}

	target := fmt.Sprintf("%s://%s%s%s", scheme, wp.env.WebHost, port, r.URL.RequestURI())
	http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	return
}

// getScheme returns the request scheme
func getScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// isProxyRequest reports whether a request to host
// should be handled by the proxy.
func (wp *WebProxy) isProxyRequest(host string) bool {
	return exeweb.IsProxyRequest(wp.env, wp.tsDomain, host)
}

// boxFromHost returns the box name given a host we are proxying.
func (wp *WebProxy) boxFromHost(host string) string {
	hostname, _, _ := net.SplitHostPort(host)
	if hostname == "" {
		hostname = host
	}
	return domz.Label(hostname, wp.env.BoxHost)
}

// isRequestOnMainPort reports whether the request came in on
// the main HTTP/HTTPS port.
// Returns true if the request should continue processing,
// false if an error response was sent.
// Non-proxy content (main website, xterm, etc) should only
// be served on the main port.
// Checks both the actual connection port and the Host header port.
func (wp *WebProxy) isRequestOnMainPort(w http.ResponseWriter, r *http.Request) bool {
	// Check the actual local address the request came in on from the context.
	conn, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if ok && conn != nil {
		_, localPortStr, err := net.SplitHostPort(conn.String())
		if err != nil {
			wp.lg().ErrorContext(r.Context(), "failed to parse local address", "error", err, "addr", conn.String())
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return false
		}
		localPort, err := strconv.Atoi(localPortStr)
		if err != nil {
			wp.lg().ErrorContext(r.Context(), "failed to convert local port", "error", err, "port", localPortStr)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return false
		}
		if !wp.isMainListenerPort(localPort) {
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
		if !wp.isMainListenerPort(hostPort) {
			http.Error(w, "404 page not found", http.StatusNotFound)
			return false
		}
	}
	// No port in Host header is fine - browsers don't send port for 80/443.

	return true
}

// isMainListenerPort reports whether port is the server's
// main HTTP or HTTPS port.
func (wp *WebProxy) isMainListenerPort(port int) bool {
	if wp.httpLn != nil && wp.httpLn.port() == port {
		return true
	}
	if wp.httpsLn != nil && wp.httpsLn.port() == port {
		return true
	}
	return false
}

// getRequestPort extracts the port number from
// an HTTP request's Host header.
// For requests without an explicit port,
// it returns the default port for the scheme
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

// exewebProxyData is the local implementation of [exeweb.ProxyData].
type exewebProxyData struct {
	wp *WebProxy
}

// BoxInfo implements [exeweb.ProxyData.BoxInfo].
func (epd *exewebProxyData) BoxInfo(ctx context.Context, boxName string) (exeweb.BoxData, bool, error) {
	return epd.wp.proxy.boxes.lookup(ctx, epd.exeproxData(), boxName)
}

// CookieInfo implements [exeweb.ProxyData.CookieInfo].
func (epd *exewebProxyData) CookieInfo(ctx context.Context, cookieValue, domain string) (exeweb.CookieData, bool, error) {
	return epd.wp.proxy.cookies.lookup(ctx, epd.exeproxData(), cookieValue, domain)
}

// UserInfo implements [exeweb.ProxyData.UserInfo].
func (epd *exewebProxyData) UserInfo(ctx context.Context, userID string) (exeweb.UserData, bool, error) {
	ud, exists, err := epd.wp.proxy.users.lookup(ctx, epd.exeproxData(), userID)
	if !exists || err != nil {
		return exeweb.UserData{}, exists, err
	}
	eud := exeweb.UserData{
		UserID: ud.userID,
		Email:  ud.email,
	}
	return eud, true, nil
}

// IsUserLockedOut implements [exeweb.ProxyData.IsUserLockedOut].
func (epd *exewebProxyData) IsUserLockedOut(ctx context.Context, userID string) (bool, error) {
	ud, exists, err := epd.wp.proxy.users.lookup(ctx, epd.exeproxData(), userID)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, errors.New("user not found")
	}
	return ud.isLockedOut, nil
}

// UserHasExeSudo implements [exeweb.ProxyData.UserHasExeSudo].
func (epd *exewebProxyData) UserHasExeSudo(ctx context.Context, userID string) (bool, error) {
	ud, exists, err := epd.wp.proxy.users.lookup(ctx, epd.exeproxData(), userID)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, errors.New("user not found")
	}
	return ud.rootSupport == 1, nil
}

// CreateAuthCookie implements [exeweb.ProxyData.CreateAuthCookie].
func (epd *exewebProxyData) CreateAuthCookie(ctx context.Context, userID, domain string) (string, error) {
	return epd.exeproxData().CreateAuthCookie(ctx, userID, domain)
}

// DeleteAuthCookie implements [exeweb.ProxyData.DeleteAuthCookie].
func (epd *exewebProxyData) DeleteAuthCookie(ctx context.Context, cookieValue string) error {
	return epd.exeproxData().DeleteAuthCookie(ctx, cookieValue)
}

// UsedCookie implements [exeweb.ProxyData.UsedCookie].
func (epd *exewebProxyData) UsedCookie(ctx context.Context, cookieValue string) {
	// We don't need to wait for this, and we don't care about errors,
	// so run it in a separate goroutine.
	go func() {
		ctx = context.WithoutCancel(ctx)
		epd.exeproxData().UsedCookie(ctx, cookieValue)
	}()
}

// HasUserAccessToBox implements [exeweb.ProxyData.HasUserAccessToBox].
func (epd *exewebProxyData) HasUserAccessToBox(ctx context.Context, boxID int, boxName, userID string) (bool, error) {
	return epd.wp.proxy.boxes.isSharedWithUser(ctx, epd.exeproxData(), boxID, boxName, userID)
}

// IsBoxSharedWithUserTeam implements [exeweb.ProxyData.IsBoxSharedWithUserTeam].
func (epd *exewebProxyData) IsBoxSharedWithUserTeam(ctx context.Context, boxID int, boxName, userID string) (bool, error) {
	return epd.wp.proxy.boxes.isSharedWithUserTeam(ctx, epd.exeproxData(), boxID, boxName, userID)
}

// CheckShareLink implements [exeweb.ProxyData.CheckShareLink].
func (epd *exewebProxyData) CheckShareLink(ctx context.Context, boxID int, boxName, userID, shareToken string) (bool, error) {
	return epd.wp.proxy.boxes.isShareLinkValid(ctx, epd.exeproxData(), boxID, boxName, userID, shareToken)
}

// ValidateMagicSecret consumes and validates a magic secret.
func (epd *exewebProxyData) ValidateMagicSecret(ctx context.Context, secret string) (userID, boxName, redirectURL string, err error) {
	return epd.exeproxData().ValidateMagicSecret(ctx, secret)
}

// GetSSHKeyByFingerprint implements [exeweb.ProxyData.GetSSHKeyByFingerprint].
func (epd *exewebProxyData) GetSSHKeyByFingerprint(ctx context.Context, fingerprint string) (userID, key string, err error) {
	skd, exists, err := epd.wp.proxy.sshKeys.lookup(ctx, epd.exeproxData(), fingerprint)
	if err != nil {
		return "", "", err
	}
	if !exists {
		return "", "", errors.New("invalid token")
	}
	return skd.userID, skd.publicKey, nil
}

// HLLNoteEvents implements [exeweb.ProxyData.HLLNoteEvents].
func (epd *exewebProxyData) HLLNoteEvents(ctx context.Context, userID string, events []string) {
	// We don't need to wait for this, and we don't care about errors,
	// so run it in a separate goroutine.
	go func() {
		ctx = context.WithoutCancel(ctx)
		epd.exeproxData().HLLNoteEvents(ctx, userID, events)
	}()
}

// CheckAndIncrementEmailQuota implements [exeweb.ProxyData.CheckAndIncrementEmailQuota].
func (epd *exewebProxyData) CheckAndIncrementEmailQuota(ctx context.Context, userID string) error {
	return epd.exeproxData().CheckAndIncrementEmailQuota(ctx, userID)
}

// SendEmail implements [exeweb.ProxyData.SendEmail].
func (epd *exewebProxyData) SendEmail(ctx context.Context, emailType email.Type, to, subject, body string) error {
	return epd.exeproxData().SendEmail(ctx, emailType, to, subject, body)
}

// exeproxData is a helper method to return the exexproxData to use.
func (epd *exewebProxyData) exeproxData() ExeproxData {
	return epd.wp.proxy.exeproxData
}
