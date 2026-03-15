package exeprox

import (
	"expvar"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/pprof"

	"exe.dev/exedebug"
	"exe.dev/logging"
)

// debugHandler constructs and returns the debug endpoint mux.
// All routes are already gated by exedebug.RequireLocalAccess
// in the caller (ServeHTTP).
func (wp *WebProxy) debugHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug", wp.handleDebugIndex)
	mux.HandleFunc("/debug/", wp.handleDebugIndex)
	mux.HandleFunc("/debug/gitsha", wp.handleDebugGitsha)

	// pprof
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// expvar
	mux.Handle("/debug/vars", expvar.Handler())

	return mux
}

var debugIndexTmpl = template.Must(template.New("debug-index").Parse(`<!doctype html>
<html><head><title>exeprox debug</title>
<style>
body { font-family: ui-monospace, "Cascadia Code", "SF Mono", Menlo, Consolas, monospace; font-size: 13px; margin: 12px; line-height: 1.4; background: {{.StageBgColor}}; }
a { color: #007bff; text-decoration: none; }
a:hover { text-decoration: underline; }
h1 { font-size: 16px; margin: 0 0 4px 0; }
h3 { color: #333; border-bottom: 1px solid #ddd; padding-bottom: 2px; display: inline-block; min-width: 200px; font-size: 13px; margin: 10px 0 2px 0; }
ul { margin: 2px 0 0 0; padding-left: 18px; }
li { margin: 1px 0; }
.meta { color: #666; margin-bottom: 8px; }
.stage { display: inline-block; padding: 1px 6px; border-radius: 2px; font-weight: bold; color: white; font-size: 12px; }
</style>
</head><body>
<h1>exeprox debug</h1>
<p class="meta">
    <span class="stage" style="background:{{.StageColor}}">{{.Stage}}</span>
    {{.GitCommit}} {{.GitHubLink}}
</p>

<h3>Diagnostics</h3>
<ul>
    <li><a href="/debug/pprof/">pprof</a></li>
    <li><a href="/debug/pprof/goroutine?debug=1">pprof/goroutine</a></li>
    <li><a href="/debug/pprof/profile">pprof/profile</a></li>
    <li><a href="/debug/pprof/trace">pprof/trace</a></li>
    <li><a href="/debug/pprof/symbol">pprof/symbol</a></li>
    <li><a href="/debug/pprof/cmdline">pprof/cmdline</a></li>
    <li><a href="/debug/vars">vars</a></li>
    <li><a href="/metrics">metrics</a></li>
    <li><a href="/debug/gitsha">gitsha</a></li>
</ul>

</body></html>
`))

func (wp *WebProxy) handleDebugIndex(w http.ResponseWriter, r *http.Request) {
	commit := logging.GitCommit()
	displayCommit := exedebug.DisplayCommit(commit)

	data := struct {
		Stage        string
		StageColor   string
		StageBgColor string
		GitCommit    string
		GitHubLink   template.HTML
	}{
		Stage:        wp.env.DebugLabel,
		StageColor:   wp.env.DebugColor,
		StageBgColor: wp.env.DebugBgColor,
		GitCommit:    displayCommit,
		GitHubLink:   exedebug.GitHubLink(commit),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := debugIndexTmpl.Execute(w, data); err != nil {
		slog.ErrorContext(r.Context(), "debug index template", "error", err)
	}
}

func (wp *WebProxy) handleDebugGitsha(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, logging.GitCommit())
}
