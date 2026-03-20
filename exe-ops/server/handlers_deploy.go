package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"exe.dev/exe-ops/server/deploy"
)

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
	writeJSON(w, map[string]any{
		"head_sha":     headSHA,
		"head_subject": headSubject,
		"head_date":    headDate,
		"processes":    h.inventory.Processes(),
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
		writeJSON(w, h.deployer.List())

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
		// Record who initiated the deploy from Tailscale identity headers.
		if login := r.Header.Get("Tailscale-User-Login"); login != "" {
			req.InitiatedBy = login
		} else if name := r.Header.Get("Tailscale-User-Name"); name != "" {
			req.InitiatedBy = name
		}
		status, err := h.deployer.Start(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, status)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleDeployStatus handles GET /api/v1/deploys/{id}.
func (h *Handlers) HandleDeployStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.deployer == nil {
		http.Error(w, "deploy not configured", http.StatusNotFound)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/v1/deploys/")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}

	status, ok := h.deployer.Get(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, status)
}
