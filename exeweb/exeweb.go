// Package exeweb holds code that is shared between the
// exed web server and the exeprox proxy server.
//
// At least for now exed serves everything that exeprox does,
// so we can add exeprox without disturbing existing users.
package exeweb

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"exe.dev/boxname"
	"exe.dev/domz"
	"exe.dev/stage"
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

// NonProxyRedirect is called from an HTTP handler.
// It looks for cases where we redirect the URL.
// It does not consider cases where we should proxy the URL,
// nor does it consider cases where the URL should be handled locally.
// It returns the redirect target,
// or the empty string in the normal case that no redirection is needed.
func NonProxyRedirect(env *stage.Env, r *http.Request) string {
	isKnownHostsRequest := r.URL.Path == SSHKnownHostsPath
	hostname := domz.Canonicalize(domz.StripPort(r.Host))

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
