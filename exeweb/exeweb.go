// Package exeweb holds code that is shared between the
// exed web server and the exeprox proxy server.
//
// At least for now exed serves everything that exeprox does,
// so we can add exeprox without disturbing existing users.
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
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"exe.dev/boxname"
	"exe.dev/domz"
	"exe.dev/sshpool2"
	"exe.dev/stage"
	"exe.dev/tracing"

	"golang.org/x/crypto/ssh"
)

// SSHKnownHostsPath is for https://c2sp.org/well-known-ssh-hosts.
const SSHKnownHostsPath = "/.well-known/ssh-known-hosts"

// HSTSMiddleware adds Strict-Transport-Security headers to HTTPS responses.
// The header meets HSTS preload list requirements:
// max-age of 2 years, includeSubDomains, and preload.
func HSTSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		}
		next.ServeHTTP(w, r)
	})
}

// ExeNewAliases maps legacy exe.new paths to idea shortnames.
var ExeNewAliases = map[string]string{
	"/moltbot":  "openclaw",
	"/clawdbot": "openclaw",
}

// RequestHost returns the host for a request.
// In a normal request direct to exeprox, this is just r.Host.
// When exed redirects to exeprox, it passes a exedev_host
// parameter to preserve the original host across the redirect.
//
// Although the user can control the exedev_host parameter,
// this is not a security issue; the parameter only replaces
// the Host header, which the user can already control.
func RequestHost(r *http.Request) string {
	if header := r.URL.Query().Get("exedev_host"); header != "" {
		return header
	}
	return r.Host
}

// NonProxyRedirect is called from an HTTP handler.
// It looks for cases where we redirect the URL.
// It does not consider cases where we should proxy the URL,
// nor does it consider cases where the URL should be handled locally.
// It returns the redirect target,
// or the empty string in the normal case that no redirection is needed.
func NonProxyRedirect(env *stage.Env, r *http.Request) string {
	isKnownHostsRequest := r.URL.Path == SSHKnownHostsPath
	hostname := domz.Canonicalize(domz.StripPort(RequestHost(r)))

	// Redirect requests to BoxHost apex (exe.xyz) to WebHost (exe.dev).
	// BoxHost is only for box subdomains (vmname.exe.xyz);
	// the apex itself should redirect to WebHost to avoid
	// passkey RPID mismatch errors during auth.
	if env.BoxHost != env.WebHost {
		if hostname == env.BoxHost && !isKnownHostsRequest {
			return fmt.Sprintf("%s://%s%s", getScheme(r), env.WebHost, r.URL.RequestURI())
		}
	}

	// Redirect requests to exe.new to WebHost/new (exe.dev/new).
	// exe.new/<shortname> redirects with ?idea=<shortname> so /new can
	// look up the idea template from the database.
	// Legacy paths like /moltbot and /clawdbot are aliased to their shortname.
	if hostname == "exe.new" {
		var target strings.Builder
		target.WriteString(getScheme(r))
		target.WriteString("://")
		target.WriteString(env.WebHost)
		target.WriteString("/new")

		// Resolve the idea shortname from the path.
		shortname := ""
		if p := r.URL.Path; p != "/" && p != "" {
			if alias, ok := ExeNewAliases[p]; ok {
				shortname = alias
			} else {
				shortname = strings.TrimPrefix(p, "/")
			}
		}

		addedQuery := false
		if shortname != "" {
			target.WriteString("?idea=")
			target.WriteString(url.QueryEscape(shortname))
			addedQuery = true
		}

		if invite := r.URL.Query().Get("invite"); invite != "" {
			if addedQuery {
				target.WriteByte('&')
			} else {
				target.WriteByte('?')
			}
			target.WriteString("invite=")
			target.WriteString(url.QueryEscape(invite))
		}

		return target.String()
	}

	// Redirect requests to bold.dev to WebHost (exe.dev).
	if hostname == "bold.dev" {
		return fmt.Sprintf("https://%s%s", env.WebHost, r.URL.RequestURI())
	}

	return ""
}

// IsProxyRequest reports whether an HTTP request to host should be proxied.
// We proxy requests to VMs, which are single subdomains of the box domain.
// tsDomain is the Tailscale domain.
func IsProxyRequest(env *stage.Env, tsDomain, host string) bool {
	// DANGER ZONE: This function is load-bearing and empirically bug-prone.
	// Please take extra care when working on it.

	// Given that we cannot enumerate all proxy hosts,
	// implement by explicitly excluding known non-proxy hosts,
	// and then allowing the rest through.
	// TODO: When we have public ips,
	// we could make this decision based on the IP the request came in on,
	// or at least note when that decision varies from
	// the hostname-based one.
	host = domz.Canonicalize(domz.StripPort(host))
	switch host {
	case "":
		return false // refuse the temptation to guess
	case env.BoxHost:
		return false // box apex is not a proxy target
	case "blog." + env.WebHost:
		// Special main webserver subdomains that are actually
		// served on VMs.
		return true
	}

	if env.WebDev {
		// When doing local development,
		// it's useful to be able to reach the webserver
		// via the local machine's hostname, not just localhost.
		// This lets you do something like
		// "socat TCP-LISTEN:8081,fork TCP:localhost:8080"
		// and try out the mobile dashboard from your phone.
		oshost, err := os.Hostname()
		if err == nil && host == oshost {
			return false
		}
	}

	// Exclude pages that should be served locally:
	// our internal debug pages (on Tailscale),
	// the public web server ([*.]exe.dev),
	// and web-based xterm (foo.xterm.exe.xyz).
	// Note: shelley subdomain (foo.shelley.exe.xyz) IS a proxy request;
	// it proxies to port 9999.
	if domz.FirstMatch(host, tsDomain, env.WebHost, env.BoxSub("xterm")) != "" {
		return false
	}
	if domz.IsIPAddr(host) {
		return false // refuse IP addresses
	}
	// We've excluded known non-proxy hosts.
	// At this point, anything domain-like is fair game.
	return strings.Contains(host, ".")
}

// IsShelleyRequest determines if a request is for a Shelley subdomain
// (vm.shelley.exe.xyz).
func IsShelleyRequest(env *stage.Env, host string) bool {
	host = domz.Canonicalize(domz.StripPort(host))
	// Check if host ends with .shelley.{BoxHost}
	// (e.g., vm.shelley.exe.xyz).
	shelleyBase := env.BoxSub("shelley") // shelley.exe.xyz
	return strings.HasSuffix(host, "."+shelleyBase)
}

// getScheme returns the request scheme, respecting X-Forwarded-Proto
// for requests arriving through a reverse proxy (e.g., exe.dev TLS proxy).
func getScheme(r *http.Request) string {
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" || proto == "http" {
		return proto
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// RelativeRedirect returns the path and query from u as a relative URL string
// suitable for use as a redirect parameter. It omits the scheme and host,
// which avoids passing absolute URLs to redirect validators that only
// accept relative paths.
func RelativeRedirect(u *url.URL) string {
	if u.RawQuery != "" {
		return u.Path + "?" + u.RawQuery
	}
	return u.Path
}

// IsValidRedirectURL validates that a redirect URL is safe
// (relative path only).
// This prevents open redirect attacks where an attacker could redirect users
// to a malicious external site after authentication.
func IsValidRedirectURL(redirectURL string) bool {
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

// IsTerminalRequest reports whether a request is for a terminal subdomain.
func IsTerminalRequest(env *stage.Env, host string) bool {
	_, err := ParseTerminalHostname(env, host)
	return err == nil
}

// ParseTerminalHostname returns the box name from a terminal hostname.
// It returns an error if the argument is not a terminal hostname.
func ParseTerminalHostname(env *stage.Env, host string) (string, error) {
	host = domz.Canonicalize(domz.StripPort(host))
	if box, ok := terminalBoxForBase(env, host); ok {
		return box, nil
	}
	return "", errors.New("not a terminal hostname")
}

// terminalBoxForBase returns the box name for a terminal hostname.
// The second result reports whether this is a valid terminal hostname.
func terminalBoxForBase(env *stage.Env, host string) (string, bool) {
	if host == "" {
		return "", false
	}
	boxName, ok := domz.CutBase(host, env.BoxSub("xterm"))
	if !ok {
		return "", false
	}
	if !boxname.IsValid(boxName) {
		return "", false
	}
	return boxName, true
}

// SetAuthCookie adds an HTTP auth cookie to an HTTP response.
func SetAuthCookie(w http.ResponseWriter, r *http.Request, domain, cookieValue string) {
	cookie := &http.Cookie{
		Name:     domain,
		Value:    cookieValue,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   30 * 24 * 60 * 60, // 30 days
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(w, cookie)
}

// RenderLockedOutPage is called if a user is locked out
// to show the locked-out page on w.
func RenderLockedOutPage(ctx context.Context, lg *slog.Logger, templates *template.Template, userID string, w http.ResponseWriter) {
	traceID := tracing.TraceIDFromContext(ctx)
	lg.WarnContext(ctx, "locked out user attempted access", "userID", userID, "trace_id", traceID)

	w.WriteHeader(http.StatusForbidden)
	data := struct {
		TraceID string
	}{
		TraceID: traceID,
	}
	if err := renderTemplate(ctx, lg, templates, w, "account-locked.html", data); err != nil {
		lg.ErrorContext(ctx, "failed to render account-locked template", "error", err)
	}
}

// renderTemplate generates an HTTP response from a template.
func renderTemplate(ctx context.Context, lg *slog.Logger, templates *template.Template, w http.ResponseWriter, templateName string, data any) error {
	w.Header().Set("Content-Type", "text/html")
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		lg.ErrorContext(ctx, "Failed to execute template", "error", err, "template", templateName)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// AuthCookieValueFromRequest takes an HTTP request.
// It returns the auth cookie value and domain.
func AuthCookieValueFromRequest(r *http.Request) (cookieValue, domain string, err error) {
	return CookieValueFromRequest(r, "exe-auth")
}

// CookieValueFromRequest takes an HTTP request and the name of an auth cookie.
// It returns the cookie value and domain.
func CookieValueFromRequest(r *http.Request, cookieName string) (cookieValue, domain string, err error) {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		// NB: many callers check for errors.Is(err, http.ErrNoCookie),
		// so be sure to wrap the error returned from r.Cookie.
		return "", "", fmt.Errorf("failed to read %s cookie: %w", cookieName, err)
	}
	if cookie.Value == "" {
		return "", "", fmt.Errorf("empty %s: %w", cookieName, http.ErrNoCookie)
	}

	// Strip port from domain since cookies are per-host,
	// not per-host:port.
	// We don't use RequestHost here because the auth cookie
	// is associated with exe.dev, not the user host.
	domain = domz.StripPort(r.Host)

	return cookie.Value, domain, nil
}

// CreateSSHTunnelTransportArgs is arguments to pass to CreateSSHTunnelTransport.
type CreateSSHTunnelTransportArgs struct {
	SSHHost                 string
	SSHKey                  ssh.Signer
	BoxName                 string
	BoxSSHUser              string
	BoxSSHPort              int
	BoxSSHServerIdentityKey []byte
	SSHPool                 *sshpool2.Pool
	Metrics                 *HTTPMetrics
}

// CreateSSHTunnelTransport creates an HTTP transport that
// tunnels through SSH to a container.
func CreateSSHTunnelTransport(ctx context.Context, args *CreateSSHTunnelTransportArgs) *http.Transport {
	if args.SSHHost == "" || args.SSHKey == nil ||
		args.BoxName == "" || args.BoxSSHUser == "" ||
		args.BoxSSHPort == 0 || len(args.BoxSSHServerIdentityKey) == 0 ||
		args.SSHPool == nil || args.Metrics == nil {

		slog.ErrorContext(ctx, "CreateSSHTunnelTransport missing args", "args", *args)
	}

	// Build an HTTP transport that dials through SSH
	// to the target on the SSH host.
	// The sshDialer uses the connection pool for SSH connections.
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			cfg := &ssh.ClientConfig{
				User:            args.BoxSSHUser,
				Auth:            []ssh.AuthMethod{ssh.PublicKeys(args.SSHKey)},
				HostKeyCallback: CreateHostKeyCallback(args.BoxName, args.BoxSSHServerIdentityKey),
				Timeout:         30 * time.Second,
			}
			// Use a deadline that allows for stale
			// connection recovery:
			// - Each dial attempt is bounded by
			//   max(500ms, 4×RTT) via staleTimeoutFor;
			// - If first attempt hits a stale connection,
			//   it times out and is removed;
			// - Retries can establish a fresh connection
			//   (up to 3s for SSH dial).
			// 10s gives enough headroom for stale recovery
			// even on high-latency paths (e.g. JNB→LAX).
			// Note: "port not bound" still fails fast
			// since connection refused is immediate.
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			conn, err := args.SSHPool.DialWithRetries(ctx, network, addr, args.SSHHost, args.BoxSSHUser, args.BoxSSHPort, args.SSHKey, cfg, []time.Duration{
				50 * time.Millisecond,
				100 * time.Millisecond,
				200 * time.Millisecond,
			})
			// DialWithRetries guarantees (conn, nil) on success; check conn for defensiveness.
			if conn == nil {
				return nil, errors.Join(errors.New("SSH dial failed"), err)
			}
			return &countingConn{Conn: conn, metrics: args.Metrics}, nil
		},
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100, // all traffic goes to one host (127.0.0.1 inside VM)
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
	}
}
