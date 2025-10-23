package exe

import (
	"encoding/json"
	"expvar"
	"fmt"
	"html"
	"net/http"
	"net/http/pprof"
	"runtime/debug"
)

// debugHandler constructs and returns a handler with Go-standard debug endpoints
// (pprof, expvar). Creating this handler is cheap and avoids global state.
func (s *Server) debugHandler() http.Handler {
	mux := http.NewServeMux()

	// index & aux
	mux.HandleFunc("/debug$", s.handleDebugIndex)
	mux.HandleFunc("/debug/", s.handleDebugIndex)
	mux.HandleFunc("/debug/gitsha", s.handleDebugGitsha)
	mux.HandleFunc("/debug/boxes", s.handleDebugBoxes)

	// pprof endpoints
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// expvar at /debug/vars
	mux.Handle("/debug/vars", expvar.Handler())

	// Metrics are served at /metrics; no duplicate handler here.

	return mux
}

// handleDebug gates access to debug endpoints: allowed when the
// request originates from a Tailscale IP or loopback.
func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	s.debugHandler().ServeHTTP(w, r)
}

// handleDebugIndex renders a simple HTML index of debug endpoints.
func (s *Server) handleDebugIndex(w http.ResponseWriter, r *http.Request) {
	commit := gitCommit()
	if commit == "" {
		commit = "unknown"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>exed debug</title></head><body>
<h1>exed debug</h1>
<ul>
    <li><a href="/debug/pprof/">pprof</a></li>
    <li><a href="/debug/pprof/cmdline">pprof/cmdline</a></li>
    <li><a href="/debug/pprof/profile">pprof/profile</a></li>
    <li><a href="/debug/pprof/symbol">pprof/symbol</a></li>
    <li><a href="/debug/pprof/trace">pprof/trace</a></li>
    <li><a href="/debug/pprof/goroutine?debug=1">pprof/goroutine?debug=1</a></li>
    <li><a href="/metrics">metrics</a></li>
    <li><a href="/debug/gitsha">gitsha</a></li>
    <li><a href="/debug/boxes">boxes</a> (<a href="/debug/boxes?format=json">json</a>)</li>
</ul>
<p>Git version: %s</p>
</body></html>
`, commit)
}

func (s *Server) handleDebugGitsha(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, gitCommit())
}

// handleDebugBoxes returns the list of container hosts and their containers
func (s *Server) handleDebugBoxes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	type containerInfo struct {
		Host   string `json:"host"`
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}

	type hostInfo struct {
		Host       string
		Containers []containerInfo
		Error      string
	}

	var hosts []hostInfo
	var flatContainers []containerInfo

	if s.containerManager != nil {
		for _, host := range s.containerManager.GetHosts() {
			info := hostInfo{Host: host}
			containers, err := s.containerManager.ListContainersOnHost(ctx, host)
			if err != nil {
				info.Error = err.Error()
			} else {
				for _, c := range containers {
					cInfo := containerInfo{
						Host:   host,
						ID:     c.ID,
						Name:   c.Name,
						Status: string(c.Status),
					}
					info.Containers = append(info.Containers, cInfo)
					flatContainers = append(flatContainers, cInfo)
				}
			}
			hosts = append(hosts, info)
		}
	}

	// Check if JSON format is requested
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(flatContainers); err != nil {
			s.slog().Error("Failed to encode containers", "error", err)
		}
		return
	}

	// HTML output
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Boxes by Host</title></head><body>
<h1>Boxes by Host</h1>
<p><a href="/debug/boxes?format=json">View as JSON</a></p>
`)

	if len(hosts) == 0 {
		fmt.Fprintf(w, "<p>No container hosts configured.</p>\n")
	} else {
		for _, host := range hosts {
			fmt.Fprintf(w, "<h2>%s</h2>\n", html.EscapeString(host.Host))
			if host.Error != "" {
				fmt.Fprintf(w, "<p style='color: red;'>Error: %s</p>\n", html.EscapeString(host.Error))
			} else if len(host.Containers) == 0 {
				fmt.Fprintf(w, "<p>No containers running.</p>\n")
			} else {
				fmt.Fprintf(w, "<table border='1' cellpadding='5' cellspacing='0'>\n")
				fmt.Fprintf(w, "<tr><th>Name</th><th>ID</th><th>Status</th></tr>\n")
				for _, c := range host.Containers {
					fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%s</td></tr>\n",
						html.EscapeString(c.Name),
						html.EscapeString(c.ID),
						html.EscapeString(c.Status))
				}
				fmt.Fprintf(w, "</table>\n")
			}
		}
	}

	fmt.Fprintf(w, `<p><a href="/debug">Back to debug index</a></p>
</body></html>
`)
}

// gitCommit extracts the git SHA from build info for version identification.
func gitCommit() string {
	bi, _ := debug.ReadBuildInfo()
	if bi != nil {
		for _, setting := range bi.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	return ""
}
