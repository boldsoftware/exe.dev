package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"exe.dev/exe-ops/apitype"
	"exe.dev/exe-ops/server/agentbin"
	"exe.dev/exe-ops/server/aiagent"
	"exe.dev/exe-ops/server/deploy"
	"exe.dev/exe-ops/server/exed"
	"exe.dev/exe-ops/server/inventory"
	"exe.dev/exe-ops/version"
)

// Handlers holds API handler dependencies.
type Handlers struct {
	store      *Store
	hub        *Hub
	log        *slog.Logger
	aiAgent    aiagent.Provider     // nil if AI not configured
	aiConfig   *aiagent.Config      // nil if AI not configured
	exedClient *exed.Client         // nil if exed not configured
	inventory  *inventory.Inventory // nil if inventory not configured
	deployer   *deploy.Manager      // nil if deploy not configured
}

// NewHandlers creates a new Handlers.
func NewHandlers(store *Store, hub *Hub, log *slog.Logger, ai aiagent.Provider, aiCfg *aiagent.Config, exedClient *exed.Client, inv *inventory.Inventory, deployer *deploy.Manager) *Handlers {
	return &Handlers{store: store, hub: hub, log: log, aiAgent: ai, aiConfig: aiCfg, exedClient: exedClient, inventory: inv, deployer: deployer}
}

// HandleReport handles POST /api/v1/report.
func (h *Handlers) HandleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var report apitype.Report
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		h.log.Error("decode report", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if report.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if report.Tags == nil {
		report.Tags = []string{}
	}

	parts, err := apitype.ParseHostname(report.Name)
	if err != nil {
		// Non-standard hostname; use name as hostname, leave parts empty.
		parts = apitype.HostnameParts{}
	}

	serverID, err := h.store.UpsertServer(r.Context(), &report, parts)
	if err != nil {
		h.log.Error("upsert server", "error", err, "name", report.Name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.store.InsertReport(r.Context(), serverID, &report); err != nil {
		h.log.Error("insert report", "error", err, "name", report.Name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Check if this agent has a pending upgrade.
	pending, err := h.store.IsUpgradePending(r.Context(), report.Name)
	if err != nil {
		h.log.Warn("check upgrade pending", "error", err, "name", report.Name)
	}
	if pending {
		w.Header().Set("X-Upgrade-Available", "true")
	}

	h.log.Info("report received", "server", report.Name, "agent_version", report.AgentVersion)

	// Broadcast report event to dashboard subscribers.
	reportData, err := json.Marshal(map[string]any{
		"name":        report.Name,
		"cpu_percent": report.CPU,
		"mem_total":   report.MemTotal,
		"mem_used":    report.MemUsed,
		"disk_total":  report.DiskTotal,
		"disk_used":   report.DiskUsed,
		"net_send":    report.NetSend,
		"net_recv":    report.NetRecv,
	})
	if err == nil {
		h.hub.Broadcast(Event{Type: "report", Data: reportData})
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleListServers handles GET /api/v1/servers.
func (h *Handlers) HandleListServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	servers, err := h.store.ListServers(r.Context())
	if err != nil {
		h.log.Error("list servers", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, servers)
}

// HandleListFleet handles GET /api/v1/fleet.
func (h *Handlers) HandleListFleet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	servers, err := h.store.ListFleet(r.Context())
	if err != nil {
		h.log.Error("list fleet", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, servers)
}

// HandleGetServer handles GET /api/v1/servers/{name}.
func (h *Handlers) HandleGetServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract server name from path: /api/v1/servers/{name}
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/servers/")
	if name == "" {
		http.Error(w, "server name required", http.StatusBadRequest)
		return
	}

	server, err := h.store.GetServer(r.Context(), name)
	if err != nil {
		h.log.Error("get server", "error", err, "name", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if server == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	writeJSON(w, server)
}

// HandleDeleteServer handles DELETE /api/v1/servers/{name}.
func (h *Handlers) HandleDeleteServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/api/v1/servers/")
	if name == "" {
		http.Error(w, "server name required", http.StatusBadRequest)
		return
	}

	if err := h.store.DeleteServer(r.Context(), name); err != nil {
		h.log.Error("delete server", "error", err, "name", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.log.Info("server deleted", "server", name)
	w.WriteHeader(http.StatusNoContent)
}

// HandleUpgrade handles POST /api/v1/servers/{name}/upgrade.
func (h *Handlers) HandleUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract server name: /api/v1/servers/{name}/upgrade
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/servers/")
	name := strings.TrimSuffix(path, "/upgrade")
	if name == "" || name == path {
		http.Error(w, "server name required", http.StatusBadRequest)
		return
	}

	if err := h.store.SetUpgradePending(r.Context(), name, true); err != nil {
		h.log.Error("set upgrade pending", "error", err, "name", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.log.Info("upgrade scheduled", "server", name)
	writeJSON(w, map[string]string{"status": "upgrade_scheduled"})
}

// HandleResetNetCounters handles POST /api/v1/servers/{name}/reset-net-counters.
func (h *Handlers) HandleResetNetCounters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract server name: /api/v1/servers/{name}/reset-net-counters
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/servers/")
	name := strings.TrimSuffix(path, "/reset-net-counters")
	if name == "" || name == path {
		http.Error(w, "server name required", http.StatusBadRequest)
		return
	}

	if err := h.store.ResetNetCounters(r.Context(), name); err != nil {
		h.log.Error("reset net counters", "error", err, "name", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.log.Info("net counters reset", "server", name)
	w.WriteHeader(http.StatusNoContent)
}

// HandleAgentBinary handles GET /api/v1/agent/binary.
func (h *Handlers) HandleAgentBinary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	goos := r.URL.Query().Get("os")
	if goos == "" {
		goos = "linux"
	}
	goarch := r.URL.Query().Get("arch")
	if goarch == "" {
		goarch = "amd64"
	}

	data, err := agentbin.Get(goos, goarch)
	if err != nil {
		h.log.Error("agent binary not found", "os", goos, "arch", goarch, "error", err)
		http.Error(w, "binary not available for "+goos+"/"+goarch, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=exe-ops-agent")
	w.Header().Set("X-Agent-Version", version.Version)
	w.Write(data)

	// Clear upgrade_pending after the binary has been served.
	name := r.URL.Query().Get("name")
	if name != "" {
		if err := h.store.SetUpgradePending(r.Context(), name, false); err != nil {
			h.log.Warn("failed to clear upgrade_pending", "error", err, "name", name)
		}
	}
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
	fmt.Fprint(w, version.Commit)
}

// HandleAgentStream handles GET /api/v1/stream — SSE connection for agent presence.
func (h *Handlers) HandleAgentStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "use GET for SSE stream", http.StatusMethodNotAllowed)
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name query parameter is required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	h.hub.AgentConnected(name)
	defer h.hub.AgentDisconnected(name)

	// Check if this agent has a pending upgrade and notify immediately.
	pending, err := h.store.IsUpgradePending(r.Context(), name)
	if err == nil && pending {
		fmt.Fprintf(w, "event: upgrade-available\ndata: {}\n\n")
		flusher.Flush()
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			// SSE keepalive comment.
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// HandleDashboardEvents handles GET /api/v1/events — SSE connection for dashboard push.
func (h *Handlers) HandleDashboardEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Send initial connected agents snapshot.
	agents := h.hub.ConnectedAgents()
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	initData, err := json.Marshal(map[string]any{"agents": names})
	if err == nil {
		fmt.Fprintf(w, "event: connected\ndata: %s\n\n", initData)
		flusher.Flush()
	}

	sub := h.hub.Subscribe()
	defer h.hub.Unsubscribe(sub)

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-sub.ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, ev.Data)
			flusher.Flush()
		}
	}
}

// HandleCustomAlerts handles GET/POST /api/v1/custom-alerts.
func (h *Handlers) HandleCustomAlerts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rules, err := h.store.ListCustomAlerts(r.Context())
		if err != nil {
			h.log.Error("list custom alerts", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if rules == nil {
			rules = []apitype.CustomAlertRule{}
		}
		writeJSON(w, rules)

	case http.MethodPost:
		var rule apitype.CustomAlertRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if rule.Name == "" || rule.Metric == "" || rule.Operator == "" {
			http.Error(w, "name, metric, and operator are required", http.StatusBadRequest)
			return
		}
		id, err := h.store.CreateCustomAlert(r.Context(), &rule)
		if err != nil {
			h.log.Error("create custom alert", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		rule.ID = id
		h.log.Info("custom alert created", "id", id, "name", rule.Name)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, rule)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleCustomAlert handles PUT/DELETE /api/v1/custom-alerts/{id}.
func (h *Handlers) HandleCustomAlert(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/custom-alerts/")
	if idStr == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var rule apitype.CustomAlertRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		rule.ID = id
		if err := h.store.UpdateCustomAlert(r.Context(), &rule); err != nil {
			h.log.Error("update custom alert", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		h.log.Info("custom alert updated", "id", id)
		writeJSON(w, rule)

	case http.MethodDelete:
		if err := h.store.DeleteCustomAlert(r.Context(), id); err != nil {
			h.log.Error("delete custom alert", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		h.log.Info("custom alert deleted", "id", id)
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleExeletCapacitySummary handles GET /api/v1/exelet-capacity-summary[?env=&region=].
func (h *Handlers) HandleExeletCapacitySummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	env := r.URL.Query().Get("env")
	region := r.URL.Query().Get("region")

	summary, err := h.store.GetExeletCapacitySummary(r.Context(), env, region)
	if err != nil {
		h.log.Error("get exelet capacity summary", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, summary)
}

// HandleExelets handles GET /api/v1/exelets[?env=<name>].
func (h *Handlers) HandleExelets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.exedClient == nil {
		http.Error(w, "exed not configured", http.StatusNotFound)
		return
	}

	env := r.URL.Query().Get("env")
	if env != "" {
		result, err := h.exedClient.Fetch(r.Context(), env)
		if err != nil {
			h.log.Error("fetch exelets", "env", env, "error", err)
		}
		writeJSON(w, result)
		return
	}

	results := h.exedClient.FetchAll(r.Context())
	writeJSON(w, results)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
	}
}
