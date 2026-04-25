package execore

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"exe.dev/exedb"
	"exe.dev/metricsd/types"
)

// usagePoolResponse is the JSON response for GET /api/vms/usage/pool.
type usagePoolResponse struct {
	Points []types.PoolPoint `json:"points"`
}

// HandleAPIUsagePool handles GET /api/vms/usage/pool.
// Returns aggregated pool history (avg/sum of CPU and memory) across all the user's VMs.
func (s *Server) HandleAPIUsagePool(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()

	if s.metricsdURL == "" {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return
	}

	boxes, err := withRxRes1(s, ctx, (*exedb.Queries).BoxesForUser, userID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list user boxes", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var vmNames []string
	for _, b := range boxes {
		vmNames = append(vmNames, b.Name)
	}

	if len(vmNames) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(usagePoolResponse{Points: []types.PoolPoint{}})
		return
	}

	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		if parsed, err := strconv.Atoi(h); err == nil && parsed >= 1 && parsed <= 744 {
			hours = parsed
		}
	}

	client := newMetricsClient(s.metricsdURL)
	points, err := client.queryVMsPool(ctx, vmNames, hours)
	if err != nil {
		slog.ErrorContext(ctx, "failed to query pool history", "error", err)
		http.Error(w, "failed to query metrics", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(usagePoolResponse{Points: points})
}
