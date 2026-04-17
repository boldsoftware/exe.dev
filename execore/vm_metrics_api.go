package execore

import (
	"encoding/json"
	"fmt"
	"net/http"

	"exe.dev/exedb"
)

// vmMetricsResponse is the JSON response for GET /api/vm/{name}/compute-usage/live
type vmMetricsResponse struct {
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	CPUPercent   float64 `json:"cpu_percent"`         // 100% = 1 core
	MemBytes     uint64  `json:"mem_bytes"`           // RSS in bytes
	SwapBytes    uint64  `json:"swap_bytes"`          // Swap usage in bytes
	DiskBytes    uint64  `json:"disk_bytes"`          // Actual disk usage
	DiskCapacity uint64  `json:"disk_capacity_bytes"` // Provisioned disk size
	NetRxBytes   uint64  `json:"net_rx_bytes"`        // Cumulative received bytes
	NetTxBytes   uint64  `json:"net_tx_bytes"`        // Cumulative transmitted bytes
}

// handleAPIVMMetrics handles GET /api/vm/{name}/compute-usage/live
// Returns live metrics for a single VM owned by the authenticated user.
func (s *Server) handleAPIVMMetrics(w http.ResponseWriter, r *http.Request, userID, vmName string) {
	ctx := r.Context()

	// Verify the VM exists and is owned by this user (authorization check).
	// This prevents leaking information about VMs the user doesn't own.
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            vmName,
		CreatedByUserID: userID,
	})
	if err != nil {
		// VM not found or not owned by user
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("VM %q not found", vmName),
		})
		return
	}

	// Fetch live metrics for all user's VMs.
	// We fetch all VMs because fetchVMUsageForUser is optimized for bulk queries
	// (groups by ctrhost, queries in parallel). Fetching just one VM would require
	// a new code path that's actually less efficient.
	usageRows, err := s.sshServer.fetchVMUsageForUser(ctx, userID)
	if err != nil {
		// Log the error but still return the VM with DB-only info (status from DB, zero metrics)
		// This handles the case where the exelet is temporarily unreachable.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(vmMetricsResponse{
			Name:   vmName,
			Status: box.Status,
			// All metric fields are zero by default
		})
		return
	}

	// Find the requested VM in the usage results.
	for _, row := range usageRows {
		if row.Name == vmName {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(vmMetricsResponse{
				Name:         row.Name,
				Status:       row.Status,
				CPUPercent:   row.CPUPercent,
				MemBytes:     row.MemBytes,
				SwapBytes:    row.SwapBytes,
				DiskBytes:    row.DiskBytes,
				DiskCapacity: row.DiskCapacity,
				NetRxBytes:   row.NetRx,
				NetTxBytes:   row.NetTx,
			})
			return
		}
	}

	// VM exists in DB but wasn't in usage results (shouldn't happen after auth check,
	// but handle gracefully). Return DB status with zero metrics.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(vmMetricsResponse{
		Name:   vmName,
		Status: box.Status,
	})
}
