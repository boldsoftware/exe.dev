package exelet

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"exe.dev/logging"
	"exe.dev/version"
)

// StartHTTPServer starts the HTTP server with debug endpoints, version, and metrics.
func (s *Exelet) StartHTTPServer(addr string, registry *prometheus.Registry) error {
	s.log.Info("starting HTTP server", "addr", addr)

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
	mux.HandleFunc("/debug", s.handleDebugIndex)

	// pprof endpoints
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// version endpoint
	mux.HandleFunc("/debug/version", s.handleVersion)

	// prometheus metrics
	if registry != nil {
		mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	actualAddr := ln.Addr().String()
	s.log.Info("http server listening", "addr", actualAddr)

	server := &http.Server{
		Handler: mux,
	}

	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("HTTP server error", "err", err)
		}
	}()

	return nil
}

func (s *Exelet) handleDebugIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>exelet debug</title></head><body>
<h1>exelet debug</h1>
<ul>
    <li><a href="/debug/pprof/">pprof</a></li>
    <li><a href="/debug/version">version</a></li>
    <li><a href="/metrics">metrics</a></li>
</ul>
</body></html>
`)
}

func (s *Exelet) handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "%s\n", version.FullVersion())
	fmt.Fprintf(w, "Git commit: %s\n", logging.GitCommit())
}
