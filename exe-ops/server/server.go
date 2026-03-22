package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"exe.dev/exe-ops/server/deploy"
	"exe.dev/exe-ops/server/inventory"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// New creates a configured HTTP handler with all routes.
func New(uiFS fs.FS, log *slog.Logger, inv *inventory.Inventory, deployer *deploy.Manager) http.Handler {
	h := NewHandlers(log, inv, deployer)
	mux := http.NewServeMux()

	// Deploy inventory and deploy management.
	mux.HandleFunc("/api/v1/deploy/inventory", h.HandleDeployInventory)
	mux.HandleFunc("/api/v1/deploy/commits", h.HandleDeployCommits)
	mux.HandleFunc("/api/v1/deploys", h.HandleDeploys)
	mux.HandleFunc("/api/v1/deploys/", h.HandleDeployStatus)

	// Server version.
	mux.HandleFunc("/api/v1/version", h.HandleServerVersion)

	mux.HandleFunc("/health", h.HandleHealth)
	mux.HandleFunc("/debug/gitsha", h.HandleDebugGitSHA)

	// Prometheus metrics for process uptime discovery.
	metricsRegistry := prometheus.NewRegistry()
	metricsRegistry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	mux.Handle("/metrics", promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{}))

	// SPA fallback: serve static files, fall back to index.html.
	if uiFS != nil {
		fileServer := http.FileServer(http.FS(uiFS))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// Try to serve the file directly.
			path := r.URL.Path
			if path == "/" {
				path = "/index.html"
			}
			// Check if file exists.
			f, err := uiFS.Open(strings.TrimPrefix(path, "/"))
			if err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
			// SPA fallback: serve index.html for client-side routing.
			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
		})
	}

	return RequestLogger(log)(mux)
}
