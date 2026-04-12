package exelet

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"exe.dev/logging"
	"exe.dev/version"
)

// StartHTTPServer starts the HTTP server with debug endpoints, version, and metrics.
// It is a package-level function so it can be called before the Exelet is fully
// initialized, ensuring metrics are available as early as possible during startup.
// It returns the actual address the server is listening on (useful when addr uses port 0).
// HTTPServer holds the HTTP server state so callers can register
// additional handlers after startup.
type HTTPServer struct {
	Addr string // actual bound address
	Mux  *http.ServeMux
}

func StartHTTPServer(addr string, registry *prometheus.Registry, log *slog.Logger) (*HTTPServer, error) {
	log.Info("starting HTTP server", "addr", addr)

	mux := http.NewServeMux()

	// root redirects to debug index
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/debug", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	// debug index
	mux.HandleFunc("/debug", handleDebugIndex)

	// pprof endpoints
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// version endpoint
	mux.HandleFunc("/debug/version", handleVersion)
	mux.HandleFunc("/debug/gitsha", handleGitSHA)

	// prometheus metrics
	if registry != nil {
		mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	actualAddr := ln.Addr().String()
	log.Info("http server listening", "addr", actualAddr)

	server := &http.Server{
		Handler: mux,
	}

	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server error", "err", err)
		}
	}()

	return &HTTPServer{Addr: actualAddr, Mux: mux}, nil
}

func handleDebugIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>exelet debug</title></head><body>
<h1>exelet debug</h1>
<ul>
    <li><a href="/debug/pprof/">pprof</a></li>
    <li><a href="/debug/version">version</a></li>
    <li><a href="/debug/gitsha">gitsha</a></li>
    <li><a href="/metrics">metrics</a></li>
</ul>
</body></html>
`)
}

func handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "%s\n", version.FullVersion())
	fmt.Fprintf(w, "Git commit: %s\n", logging.GitCommit())
}

func handleGitSHA(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, logging.GitCommit())
}
