package exeprox

import (
	"cmp"
	"encoding/json"
	"expvar"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"slices"
	"strconv"
	"time"

	"exe.dev/exedebug"
	"exe.dev/logging"
	"exe.dev/sshpool2"
)

// debugHandler constructs and returns the debug endpoint mux.
// All routes are already gated by exedebug.AllowDebugAccess
// in the caller (ServeHTTP).
func (wp *WebProxy) debugHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug", wp.handleDebugIndex)
	mux.HandleFunc("/debug/", wp.handleDebugIndex)
	mux.HandleFunc("/debug/gitsha", wp.handleDebugGitsha)
	mux.HandleFunc("/debug/sshpool", wp.handleDebugSSHPool)
	mux.HandleFunc("/debug/sshpool/drop", wp.handleDebugSSHPoolDrop)
	mux.HandleFunc("/debug/sshpool/dropall", wp.handleDebugSSHPoolDropAll)

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
    <li><a href="/debug/sshpool">sshpool</a></li>
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

var sshPoolTmpl = template.Must(template.New("debug-sshpool").Funcs(template.FuncMap{
	"dur": func(d time.Duration) string {
		if d == 0 {
			return "-"
		}
		if d < time.Millisecond {
			return d.Round(time.Microsecond).String()
		}
		if d < time.Second {
			return d.Round(time.Millisecond).String()
		}
		return d.Round(time.Second).String()
	},
}).Parse(`<!doctype html>
<html><head><title>exeprox sshpool</title>
<style>
body { font-family: ui-monospace, "Cascadia Code", "SF Mono", Menlo, Consolas, monospace; font-size: 13px; margin: 12px; line-height: 1.4; background: {{.StageBgColor}}; }
a { color: #007bff; text-decoration: none; }
a:hover { text-decoration: underline; }
h1 { font-size: 16px; margin: 0 0 4px 0; }
h3 { color: #333; border-bottom: 1px solid #ddd; padding-bottom: 2px; font-size: 13px; margin: 12px 0 4px 0; }
.meta { color: #666; margin-bottom: 8px; }
.stage { display: inline-block; padding: 1px 6px; border-radius: 2px; font-weight: bold; color: white; font-size: 12px; }
table { border-collapse: collapse; margin-top: 4px; }
th, td { text-align: left; padding: 3px 10px 3px 0; border-bottom: 1px solid #eee; vertical-align: top; }
th { color: #555; font-weight: normal; border-bottom: 1px solid #ccc; }
td.num { text-align: right; }
button { font: inherit; padding: 1px 6px; cursor: pointer; }
form.inline { display: inline; margin: 0; }
.toolbar { margin: 6px 0; }
.empty { color: #888; font-style: italic; }
</style>
</head><body>
<h1><a href="/debug">exeprox debug</a> / sshpool</h1>
<p class="meta">
    <span class="stage" style="background:{{.StageColor}}">{{.Stage}}</span>
    {{len .Conns}} connection(s) &middot;
    <a href="/debug/sshpool?format=json">json</a> &middot;
    <a href="/metrics">metrics</a>
</p>

<div class="toolbar">
<form class="inline" method="POST" action="/debug/sshpool/dropall" onsubmit="return confirm('Drop all {{len .Conns}} pooled SSH connections?');">
    <button type="submit">Flush all</button>
</form>
</div>

{{if .Conns}}
<table>
<thead><tr>
    <th>User@Host:Port</th>
    <th>Active</th>
    <th>Age</th>
    <th>RTT</th>
    <th>Fingerprint</th>
    <th></th>
</tr></thead>
<tbody>
{{range .Conns}}
<tr>
    <td>{{.User}}@{{.Host}}:{{.Port}}</td>
    <td class="num">{{.Active}}</td>
    <td class="num">{{dur .Age}}</td>
    <td class="num">{{dur .RTT}}</td>
    <td>{{.PublicKeyFingerprint}}</td>
    <td>
        <form class="inline" method="POST" action="/debug/sshpool/drop">
            <input type="hidden" name="host" value="{{.Host}}">
            <input type="hidden" name="port" value="{{.Port}}">
            <button type="submit" title="Drop all pooled connections to {{.Host}}:{{.Port}}">Drop</button>
        </form>
    </td>
</tr>
{{end}}
</tbody>
</table>
{{else}}
<p class="empty">no pooled connections</p>
{{end}}

</body></html>
`))

// snapshotPool returns a sorted snapshot of the current sshpool2 state.
// Sorted for stable UI rendering across refreshes.
func (wp *WebProxy) snapshotPool() []sshpool2.ConnInfo {
	conns := wp.proxy.sshPool.Snapshot()
	slices.SortFunc(conns, func(a, b sshpool2.ConnInfo) int {
		return cmp.Or(
			cmp.Compare(a.Host, b.Host),
			cmp.Compare(a.Port, b.Port),
			cmp.Compare(a.User, b.User),
			cmp.Compare(a.PublicKeyFingerprint, b.PublicKeyFingerprint),
		)
	})
	return conns
}

func (wp *WebProxy) handleDebugSSHPool(w http.ResponseWriter, r *http.Request) {
	conns := wp.snapshotPool()
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(conns); err != nil {
			slog.ErrorContext(r.Context(), "sshpool json", "error", err)
		}
		return
	}

	data := struct {
		Stage        string
		StageColor   string
		StageBgColor string
		Conns        []sshpool2.ConnInfo
	}{
		Stage:        wp.env.DebugLabel,
		StageColor:   wp.env.DebugColor,
		StageBgColor: wp.env.DebugBgColor,
		Conns:        conns,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := sshPoolTmpl.Execute(w, data); err != nil {
		slog.ErrorContext(r.Context(), "sshpool template", "error", err)
	}
}

func (wp *WebProxy) handleDebugSSHPoolDrop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	host := r.FormValue("host")
	port, err := strconv.Atoi(r.FormValue("port"))
	if host == "" || err != nil {
		http.Error(w, "missing or invalid host/port", http.StatusBadRequest)
		return
	}
	wp.lg().InfoContext(r.Context(), "debug: dropping sshpool connections", "host", host, "port", port)
	wp.proxy.sshPool.DropConnectionsTo(host, port)
	http.Redirect(w, r, "/debug/sshpool", http.StatusSeeOther)
}

func (wp *WebProxy) handleDebugSSHPoolDropAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	wp.lg().InfoContext(r.Context(), "debug: flushing sshpool")
	wp.proxy.sshPool.DropAll()
	http.Redirect(w, r, "/debug/sshpool", http.StatusSeeOther)
}
