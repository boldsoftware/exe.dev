package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	"exe.dev/exe-ops/server/deploy"
	"exe.dev/exe-ops/server/inventory"
	"exe.dev/exe-ops/version"
)

// Handlers holds API handler dependencies.
type Handlers struct {
	log         *slog.Logger
	environment string
	inventory   *inventory.Inventory
	deployer    *deploy.Manager
	scheduler   *deploy.Scheduler
}

// NewHandlers creates a new Handlers. environment is surfaced on
// /api/v1/version for UI display; empty means unset.
func NewHandlers(log *slog.Logger, environment string, inv *inventory.Inventory, deployer *deploy.Manager, scheduler *deploy.Scheduler) *Handlers {
	return &Handlers{log: log, environment: environment, inventory: inv, deployer: deployer, scheduler: scheduler}
}

// HandleServerVersion handles GET /api/v1/version.
func (h *Handlers) HandleServerVersion(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"version": version.Version,
		"commit":  version.Commit,
		"date":    version.Date,
	}
	if h.environment != "" {
		resp["environment"] = h.environment
	}
	if user, ok := UserFromContext(r.Context()); ok {
		resp["user"] = map[string]string{
			"loginName":   user.LoginName,
			"displayName": user.DisplayName,
			"slug":        userSlug(user),
		}
	}
	writeJSON(w, resp)
}

func userSlug(user User) string {
	candidate := strings.TrimSpace(user.LoginName)
	if before, _, ok := strings.Cut(candidate, "@"); ok {
		candidate = before
	}
	if candidate == "" {
		candidate = strings.TrimSpace(user.DisplayName)
	}

	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(candidate) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
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
