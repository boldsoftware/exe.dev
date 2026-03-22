package execore

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"exe.dev/exedb"
	"exe.dev/stage"
)

// handleUsagePage renders the user-facing /usage page.
func (s *Server) handleUsagePage(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()

	// Get user's VMs
	boxes, err := withRxRes1(s, ctx, (*exedb.Queries).BoxesForUser, userID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list user boxes", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type vmInfo struct {
		Name   string
		Status string
	}

	var vms []vmInfo
	for _, b := range boxes {
		vms = append(vms, vmInfo{Name: b.Name, Status: b.Status})
	}

	data := struct {
		stage.Env
		VMs              []vmInfo
		HasMetrics       bool
		ActivePage       string
		IsLoggedIn       bool
		BasicUser        bool
		ShowIntegrations bool
	}{
		Env:        s.env,
		VMs:        vms,
		HasMetrics: s.metricsdURL != "",
		ActivePage: "usage",
		IsLoggedIn: true,
	}

	if err := s.renderTemplate(ctx, w, "usage.html", data); err != nil {
		return
	}
}

// handleUsageAPI returns metrics data for the authenticated user's VMs only.
func (s *Server) handleUsageAPI(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()

	if s.metricsdURL == "" {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return
	}

	// Get the user's VMs
	boxes, err := withRxRes1(s, ctx, (*exedb.Queries).BoxesForUser, userID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list user boxes", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Build set of user's VM names for validation
	userVMs := make(map[string]bool)
	for _, b := range boxes {
		userVMs[b.Name] = true
	}

	// Parse requested VM names (or use all)
	var vmNames []string
	if param := r.URL.Query().Get("vm_names"); param != "" {
		for _, name := range strings.Split(param, ",") {
			name = strings.TrimSpace(name)
			if userVMs[name] {
				vmNames = append(vmNames, name)
			}
		}
	} else {
		for _, b := range boxes {
			vmNames = append(vmNames, b.Name)
		}
	}

	if len(vmNames) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}"))
		return
	}

	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		if parsed, err := strconv.Atoi(h); err == nil && parsed >= 1 && parsed <= 744 {
			hours = parsed
		}
	}

	client := newMetricsClient(s.metricsdURL)
	metrics, err := client.queryVMs(ctx, vmNames, hours)
	if err != nil {
		slog.ErrorContext(ctx, "failed to query metricsd", "error", err)
		http.Error(w, "failed to query metrics", http.StatusBadGateway)
		return
	}

	// Compute derived metrics
	result := make(map[string][]usageDataPoint)
	for vmName, vmMetrics := range metrics {
		result[vmName] = computeUsageData(vmMetrics)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
