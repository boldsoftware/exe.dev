package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"exe.dev/exe-ops/server/deploy"
	"exe.dev/exe-ops/server/inventory"
	"exe.dev/exe-ops/version"
)

// Handlers holds API handler dependencies.
type Handlers struct {
	log       *slog.Logger
	inventory *inventory.Inventory
	deployer  *deploy.Manager
}

// NewHandlers creates a new Handlers.
func NewHandlers(log *slog.Logger, inv *inventory.Inventory, deployer *deploy.Manager) *Handlers {
	return &Handlers{log: log, inventory: inv, deployer: deployer}
}

// HandleServerVersion handles GET /api/v1/version.
func (h *Handlers) HandleServerVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
		"date":    version.Date,
	})
}

// HandleHealth handles GET /health.
func (h *Handlers) HandleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"status": "ok"})
}

// HandleDebugGitSHA handles GET /debug/gitsha — returns the raw commit SHA.
func (h *Handlers) HandleDebugGitSHA(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	sha := version.Commit
	if sha == "unknown" || sha == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" {
					sha = s.Value
					break
				}
			}
		}
	}
	fmt.Fprint(w, sha)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
	}
}
