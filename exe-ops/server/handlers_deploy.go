package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"exe.dev/exe-ops/server/deploy"
)

// whoisTimeout bounds how long the handler will wait for a Tailscale whois
// lookup. This is best-effort — if it fails, we just record an empty
// InitiatedBy and proceed rather than hanging the request.
const whoisTimeout = 3 * time.Second

// tailscaleWhoIsBounded wraps TailscaleWhoIs with whoisTimeout so a stuck
// tailscale daemon can't block a deploy/rollout request.
func tailscaleWhoIsBounded(parent context.Context, remoteAddr string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, whoisTimeout)
	defer cancel()
	return TailscaleWhoIs(ctx, remoteAddr)
}

// HandleDeployInventory handles GET /api/v1/deploy/inventory.
func (h *Handlers) HandleDeployInventory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.inventory == nil {
		http.Error(w, "inventory not configured", http.StatusNotFound)
		return
	}
	headSHA, headSubject, headDate := h.inventory.HeadCommit()
	procs := h.inventory.Processes()
	if h.environment != "" {
		env := normalizeStage(h.environment)
		filtered := procs[:0]
		for _, p := range procs {
			if normalizeStage(p.Stage) == env {
				filtered = append(filtered, p)
			}
		}
		procs = filtered
	}
	writeJSON(w, map[string]any{
		"head_sha":     headSHA,
		"head_subject": headSubject,
		"head_date":    headDate,
		"processes":    procs,
	})
}

// HandleDeployCommits handles GET /api/v1/deploy/commits?from=SHA&to=SHA&limit=N.
func (h *Handlers) HandleDeployCommits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.inventory == nil {
		http.Error(w, "inventory not configured", http.StatusNotFound)
		return
	}
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if to == "" {
		http.Error(w, "to parameter required", http.StatusBadRequest)
		return
	}
	limit := 50
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	commits, err := h.inventory.CommitLog(from, to, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, commits)
}

// HandleDeploys handles GET /api/v1/deploys (list) and POST /api/v1/deploys (start).
func (h *Handlers) HandleDeploys(w http.ResponseWriter, r *http.Request) {
	if h.deployer == nil {
		http.Error(w, "deploy not configured", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		since := time.Now().Add(-30 * time.Minute)
		if s := r.URL.Query().Get("since"); s != "" {
			if d, err := time.ParseDuration(s); err == nil {
				since = time.Now().Add(-d)
			} else if t, err := time.Parse(time.RFC3339, s); err == nil {
				since = t
			}
		} else if r.URL.Query().Get("all") != "" {
			since = time.Time{} // zero time = no filter
		}
		writeJSON(w, h.deployer.List(since))

	case http.MethodPost:
		var req deploy.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Process == "" || req.Host == "" || req.SHA == "" {
			http.Error(w, "process, host, and sha are required", http.StatusBadRequest)
			return
		}
		// Identify who initiated the deploy via Tailscale peer identity.
		if identity, err := tailscaleWhoIsBounded(r.Context(), r.RemoteAddr); err != nil {
			h.log.Warn("tailscale whois failed", "remote_addr", r.RemoteAddr, "error", err)
		} else {
			req.InitiatedBy = identity
		}
		status, err := h.deployer.Start(req)
		if err != nil {
			var plErr *deploy.ProdLockError
			if errors.As(err, &plErr) {
				http.Error(w, err.Error(), http.StatusLocked)
				return
			}
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, status)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleRollouts handles GET /api/v1/rollouts (list) and POST /api/v1/rollouts (start).
func (h *Handlers) HandleRollouts(w http.ResponseWriter, r *http.Request) {
	if h.deployer == nil {
		http.Error(w, "deploy not configured", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		since := time.Now().Add(-30 * time.Minute)
		if s := r.URL.Query().Get("since"); s != "" {
			if d, err := time.ParseDuration(s); err == nil {
				since = time.Now().Add(-d)
			} else if t, err := time.Parse(time.RFC3339, s); err == nil {
				since = t
			}
		} else if r.URL.Query().Get("all") != "" {
			since = time.Time{}
		}
		writeJSON(w, h.deployer.ListRollouts(since))

	case http.MethodPost:
		var req deploy.RolloutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if identity, err := tailscaleWhoIsBounded(r.Context(), r.RemoteAddr); err != nil {
			h.log.Warn("tailscale whois failed", "remote_addr", r.RemoteAddr, "error", err)
		} else {
			req.InitiatedBy = identity
		}
		status, err := h.deployer.StartRollout(req)
		if err != nil {
			var plErr *deploy.ProdLockError
			switch {
			case errors.As(err, &plErr):
				http.Error(w, err.Error(), http.StatusLocked)
			case strings.Contains(err.Error(), "deployment in progress"):
				http.Error(w, err.Error(), http.StatusConflict)
			default:
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, status)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleRolloutByID handles GET /api/v1/rollouts/{id} and the
// POST /api/v1/rollouts/{id}/{cancel,pause,resume} control endpoints.
func (h *Handlers) HandleRolloutByID(w http.ResponseWriter, r *http.Request) {
	if h.deployer == nil {
		http.Error(w, "deploy not configured", http.StatusNotFound)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/rollouts/")
	if path == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}

	id := path
	var action string
	for _, suffix := range []string{"/cancel", "/pause", "/resume"} {
		if strings.HasSuffix(path, suffix) {
			id = strings.TrimSuffix(path, suffix)
			action = strings.TrimPrefix(suffix, "/")
			break
		}
	}

	if action != "" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var err error
		switch action {
		case "cancel":
			err = h.deployer.CancelRollout(id)
		case "pause":
			err = h.deployer.PauseRollout(id)
		case "resume":
			err = h.deployer.ResumeRollout(id)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		status, ok := h.deployer.GetRollout(id)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, status)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status, ok := h.deployer.GetRollout(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, status)
}

// HandleDeployStatus handles GET /api/v1/deploys/{id} and
// POST /api/v1/deploys/{id}/cancel.
func (h *Handlers) HandleDeployStatus(w http.ResponseWriter, r *http.Request) {
	if h.deployer == nil {
		http.Error(w, "deploy not configured", http.StatusNotFound)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/deploys/")
	if path == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}

	id := path
	cancel := false
	if strings.HasSuffix(path, "/cancel") {
		id = strings.TrimSuffix(path, "/cancel")
		cancel = true
	}

	if cancel {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := h.deployer.Cancel(id); err != nil {
			if errors.Is(err, deploy.ErrDeployRolloutOwned) {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		status, ok := h.deployer.Get(id)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, status)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status, ok := h.deployer.Get(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, status)
}
