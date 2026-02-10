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
	"strings"

	"exe.dev/boxname"
	"exe.dev/domz"
	"exe.dev/stage"
)

// SSHKnownHostsPath is for https://c2sp.org/well-known-ssh-hosts.
const SSHKnownHostsPath = "/.well-known/ssh-known-hosts"

// openclawPrompt is a prefilled prompt for exe.new/openclaw.
var openclawPrompt = `ANTHROPIC_API_KEY=<fill-this-in>

Set up Openclaw (https://openclaw.ai/) on this VM. Openclaw used to be called
Moltbot and before that Clawdbot, so be aware if the executable or other docs
still refer to those names. Use the non-interactive and accept-risk flags for
openclaw onboarding. Add the supplied auth or token as needed. Configure nginx
to forward from the default port 18789 to the root location on the default
enabled site config, making sure to enable Websocket support. Pairing is done
by "openclaw devices list" and "openclaw device approve <request id>". Make
sure the dashboard shows that Openclaw's health is OK. exe.dev handles forwarding
from port 8000 to port 80/443 and HTTPS for us, so the final "reachable"
should be https://<vm-name>.exe.xyz without port specification.`

// ExeNewPathPrompts maps paths on exe.new to pre-filled prompts
// for the /new page.
var ExeNewPathPrompts = map[string]string{
	"/moltbot":  openclawPrompt,
	"/clawdbot": openclawPrompt,
	"/openclaw": openclawPrompt,
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
	// This is a vanity domain that lets users start a new box from a memorable URL.
	// Special paths like /openclaw, /moltbot, and /clawdbot redirect with a pre-filled prompt.
	if hostname == "exe.new" {
		var target strings.Builder
		target.WriteString(getScheme(r))
		target.WriteString("://")
		target.WriteString(env.WebHost)
		target.WriteString("/new")

		addedQuery := false
		if prompt := ExeNewPathPrompts[r.URL.Path]; prompt != "" {
			target.WriteString("?prompt=")
			target.WriteString(url.QueryEscape(prompt))
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

// getScheme returns the request scheme
func getScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
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

// Label names for HTTP metrics
const (
	LabelProxy = "proxy"
	LabelPath  = "path"
	LabelBox   = "box"
)
