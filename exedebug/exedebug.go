// Package exedebug provides shared infrastructure for /debug endpoints
// across exe.dev services (exed, exeprox).
package exedebug

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/netip"
	"net/url"
	"slices"

	"exe.dev/domz"
	"exe.dev/stage"
	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/net/tsaddr"
)

// TailscaleWhoIs asks the local tailscaled who is on the other end of
// the connection from remoteAddr (as given by http.Request.RemoteAddr).
// It returns nil if the lookup fails or returns no node info.
func TailscaleWhoIs(ctx context.Context, remoteAddr string) *apitype.WhoIsResponse {
	who, err := new(local.Client).WhoIs(ctx, remoteAddr)
	if err != nil || who == nil || who.Node == nil {
		return nil
	}
	return who
}

// IsHumanTailscaleUser reports whether who is an untagged Tailscale
// node with a real user identity behind it (as opposed to a tagged
// service node like an exelet).
func IsHumanTailscaleUser(who *apitype.WhoIsResponse) bool {
	if who == nil || who.Node == nil || who.Node.IsTagged() {
		return false
	}
	return who.UserProfile != nil && who.UserProfile.LoginName != ""
}

// HasTailscaleTag reports whether who is a Tailscale node tagged with tag.
func HasTailscaleTag(who *apitype.WhoIsResponse, tag string) bool {
	if who == nil || who.Node == nil {
		return false
	}
	return slices.Contains(who.Node.Tags, tag)
}

// AllowLocalAccess reports whether r may reach a local-only endpoint
// (e.g. /metrics, /exelet-desired). On deny it writes an HTTP error
// response and returns false; the caller should return immediately.
//
// In debug-dev environments (env.DebugDev), all access control is
// disabled so the endpoint is open to the world. Otherwise the
// request must come from a Tailscale IP.
func AllowLocalAccess(env stage.Env, w http.ResponseWriter, r *http.Request) bool {
	if env.DebugDev {
		return true
	}
	ip, err := netip.ParseAddr(domz.StripPort(r.RemoteAddr))
	if err != nil {
		http.Error(w, "remoteaddr check: "+r.RemoteAddr+": "+err.Error(), http.StatusInternalServerError)
		return false
	}
	if !tsaddr.IsTailscaleIP(ip) {
		http.NotFound(w, r)
		return false
	}
	return true
}

// AllowDebugAccess reports whether r may reach a /debug endpoint. On
// deny it writes an HTTP error response and returns false; the caller
// should return immediately.
//
// In debug-dev environments (env.DebugDev), all access control is
// disabled so the endpoint is open to the world. Otherwise the
// request must come from a human (untagged) Tailscale user, so that
// tagged service nodes (e.g. exelets) cannot reach /debug pages.
//
// As a limited exception, Tailscale nodes tagged `tag:ops` are allowed
// through, since our current ops tooling runs under that tag.
//
// TODO: tighten the `tag:ops` exception. Ideally /debug pages would
// require a real human identity (passkey or otherwise) and ops
// automation would use a narrower mechanism.
func AllowDebugAccess(env stage.Env, w http.ResponseWriter, r *http.Request) bool {
	if !AllowLocalAccess(env, w, r) {
		return false
	}
	if env.DebugDev {
		return true
	}
	who := TailscaleWhoIs(r.Context(), r.RemoteAddr)
	if IsHumanTailscaleUser(who) || HasTailscaleTag(who, "tag:ops") {
		return true
	}
	http.NotFound(w, r)
	return false
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
