package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"exe.dev/exe-ops/server/aiagent"
	"exe.dev/exe-ops/server/deploy"
	"exe.dev/exe-ops/server/exed"
	"exe.dev/exe-ops/server/inventory"
)

// New creates a configured HTTP handler with all routes.
// ai and aiCfg may be nil if the AI agent is not configured.
func New(store *Store, hub *Hub, token string, uiFS fs.FS, log *slog.Logger, ai aiagent.Provider, aiCfg *aiagent.Config, exedClient *exed.Client, inv *inventory.Inventory, deployer *deploy.Manager) http.Handler {
	h := NewHandlers(store, hub, log, ai, aiCfg, exedClient, inv, deployer)
	mux := http.NewServeMux()

	// Auth-protected endpoints.
	authMw := TokenAuth(token)
	mux.Handle("/api/v1/report", authMw(http.HandlerFunc(h.HandleReport)))
	mux.Handle("/api/v1/stream", authMw(http.HandlerFunc(h.HandleAgentStream)))

	// Dashboard SSE events (public, like other API endpoints).
	mux.HandleFunc("/api/v1/events", h.HandleDashboardEvents)

	// Fleet endpoint for fleet-wide views.
	mux.HandleFunc("/api/v1/fleet", h.HandleListFleet)

	// Public API endpoints.
	mux.HandleFunc("/api/v1/servers", func(w http.ResponseWriter, r *http.Request) {
		// Route to list or detail based on path.
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/servers")
		if path == "" || path == "/" {
			h.HandleListServers(w, r)
		} else {
			h.HandleGetServer(w, r)
		}
	})
	mux.HandleFunc("/api/v1/servers/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/upgrade") {
			h.HandleUpgrade(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/reset-net-counters") {
			h.HandleResetNetCounters(w, r)
			return
		}
		if r.Method == http.MethodDelete {
			h.HandleDeleteServer(w, r)
			return
		}
		h.HandleGetServer(w, r)
	})

	// Agent binary download (auth required).
	mux.Handle("/api/v1/agent/binary", authMw(http.HandlerFunc(h.HandleAgentBinary)))

	// Chat / AI agent endpoints.
	mux.HandleFunc("/api/v1/chat/config", h.HandleChatConfig)
	mux.HandleFunc("/api/v1/chat/conversations", h.HandleListConversations)
	mux.HandleFunc("/api/v1/chat/conversations/", h.HandleConversation)
	mux.HandleFunc("/api/v1/chat/messages", h.HandleListChatMessages)
	mux.HandleFunc("/api/v1/chat/send", h.HandleChatSend)

	// Custom alert rules.
	mux.HandleFunc("/api/v1/custom-alerts", h.HandleCustomAlerts)
	mux.HandleFunc("/api/v1/custom-alerts/", h.HandleCustomAlert)

	// Exelet data from exed hosts.
	mux.HandleFunc("/api/v1/exelets", h.HandleExelets)

	// Exelet capacity summary (aggregated).
	mux.HandleFunc("/api/v1/exelet-capacity-summary", h.HandleExeletCapacitySummary)

	// Deploy inventory and deploy management.
	mux.HandleFunc("/api/v1/deploy/inventory", h.HandleDeployInventory)
	mux.HandleFunc("/api/v1/deploys", h.HandleDeploys)
	mux.HandleFunc("/api/v1/deploys/", h.HandleDeployStatus)

	// Server version.
	mux.HandleFunc("/api/v1/version", h.HandleServerVersion)

	mux.HandleFunc("/health", h.HandleHealth)
	mux.HandleFunc("/debug/gitsha", h.HandleDebugGitSHA)

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
