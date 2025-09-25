package exe

import (
	"expvar"
	"fmt"
	"net/http"
	"net/http/pprof"
	"runtime/debug"
)

// debugHandler constructs and returns a handler with Go-standard debug endpoints
// (pprof, expvar). Creating this handler is cheap and avoids global state.
func (s *Server) debugHandler() http.Handler {
	mux := http.NewServeMux()

	// index & aux
	mux.HandleFunc("/debug/", s.handleDebugIndex)
	mux.HandleFunc("/debug/gitsha", s.handleDebugGitsha)

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
	// If requesting /debug or /debug/, redirect to the pprof index.
	switch r.URL.Path {
	case "/debug", "/debug/":
		http.Redirect(w, r, "/debug/pprof/", http.StatusTemporaryRedirect)
		return
	}

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
</ul>
<p>Git version: %s</p>
</body></html>
`, commit)
}

func (s *Server) handleDebugGitsha(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, gitCommit())
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
