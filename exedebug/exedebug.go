// Package exedebug provides shared infrastructure for /debug endpoints
// across exe.dev services (exed, exeprox).
package exedebug

import (
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/netip"
	"net/url"

	"tailscale.com/net/tsaddr"
)

// RequireLocalAccess wraps an HTTP handler to only allow access from
// localhost or Tailscale IPs. Returns 404 for all other sources.
func RequireLocalAccess(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		remoteIP, err := netip.ParseAddr(host)
		if err != nil {
			http.Error(w, "remoteaddr check: "+r.RemoteAddr+": "+err.Error(), http.StatusInternalServerError)
			return
		}
		if !remoteIP.IsLoopback() && !tsaddr.IsTailscaleIP(remoteIP) {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// DisplayCommit returns a human-friendly commit string,
// substituting "(dev build)" for empty or unknown values.
func DisplayCommit(commit string) string {
	if commit == "" || commit == "unknown" {
		return "(dev build)"
	}
	return commit
}

// GitHubLink returns an HTML link to the GitHub commit history starting at the given SHA.
func GitHubLink(commit string) template.HTML {
	if commit == "" || commit == "unknown" {
		return ""
	}
	return template.HTML(fmt.Sprintf(`(<a href="https://github.com/boldsoftware/exe/commits/%s">gh</a>)`, url.PathEscape(commit)))
}
