package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"strings"

	"exe.dev/exe-ops/server/deploy"
	"exe.dev/exe-ops/server/inventory"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// New creates a configured HTTP handler with all routes. environment is
// the label for the environment this exe-ops serves (e.g. "prod",
// "staging"); it is returned from /api/v1/version for UI display. Empty
// means unset.
//
// If requireHumanAuth is true, every route except /health and /metrics
// is wrapped with RequireHumanTailscaleUser, so only human Tailscale
// users (not tagged devices or non-Tailscale peers) can reach them.
// Production deployments must set this to true; it should be disabled
// only for local development where tailscaled is not available.
func New(uiFS fs.FS, log *slog.Logger, environment string, inv *inventory.Inventory, deployer *deploy.Manager, scheduler *deploy.Scheduler, requireHumanAuth bool) http.Handler {
	h := NewHandlers(log, environment, inv, deployer, scheduler)

	// Authenticated routes: the UI and every API endpoint. These are
	// gated behind Tailscale peer identity in production.
	authed := http.NewServeMux()

	// Deploy inventory and deploy management.
	authed.HandleFunc("/api/v1/deploy/inventory", h.HandleDeployInventory)
	authed.HandleFunc("/api/v1/deploy/commits", h.HandleDeployCommits)
	authed.HandleFunc("/api/v1/deploys", h.HandleDeploys)
	authed.HandleFunc("/api/v1/deploys/", h.HandleDeployStatus)
	authed.HandleFunc("/api/v1/rollouts", h.HandleRollouts)
	authed.HandleFunc("/api/v1/rollouts/", h.HandleRolloutByID)

	// Continuous Deployment scheduler.
	authed.HandleFunc("/api/v1/cd/status", h.HandleCDStatus)
	authed.HandleFunc("/api/v1/cd/enable", h.HandleCDEnable)
	authed.HandleFunc("/api/v1/cd/disable", h.HandleCDDisable)

	// Host metrics from Prometheus.
	authed.HandleFunc("/api/v1/hosts", h.HandleHosts)
	authed.HandleFunc("/api/v1/hosts/sparklines", h.HandleHostSparklines)

	// Daemon health metrics (sparklines + floor evaluation).
	authed.HandleFunc("/api/v1/daemons/health", h.HandleDaemonHealth)
	authed.HandleFunc("/api/v1/daemons/health/instances", h.HandleDaemonHealthInstances)
	authed.HandleFunc("/api/v1/daemons/summary", h.HandleDaemonHealthSummary)

	// Server version.
	authed.HandleFunc("/api/v1/version", h.HandleServerVersion)

	authed.HandleFunc("/debug/gitsha", h.HandleDebugGitSHA)

	// pprof endpoints. Gated behind the same human-Tailscale auth as the
	// rest of /debug. Importing net/http/pprof would also register on
	// http.DefaultServeMux, which we don't serve — register explicitly.
	authed.HandleFunc("/debug/pprof/", pprof.Index)
	authed.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	authed.HandleFunc("/debug/pprof/profile", pprof.Profile)
	authed.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	authed.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// SPA fallback: serve static files, fall back to index.html.
	if uiFS != nil {
		fileServer := http.FileServer(http.FS(uiFS))
		authed.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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

	// Outer mux: /health and /metrics stay open for liveness probes and
	// Prometheus scrapers (tagged devices); everything else goes through
	// the auth gate.
	mux := http.NewServeMux()
	mux.HandleFunc("/health", h.HandleHealth)

	metricsRegistry := prometheus.NewRegistry()
	metricsRegistry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	mux.Handle("/metrics", promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{}))

	var gated http.Handler = authed
	if requireHumanAuth {
		gated = RequireHumanTailscaleUser(log)(authed)
	}
	mux.Handle("/", gated)

	return RequestLogger(log)(mux)
}
