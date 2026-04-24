package execore

import (
	"encoding/json"
	"net/http"
)

// vmsLiveVM is a single VM's live metrics in the /api/vms/usage/live response.
type vmsLiveVM struct {
	Name              string  `json:"name"`
	Status            string  `json:"status"`
	CPUPercent        float64 `json:"cpu_percent"`
	CPUs              uint64  `json:"cpus"`
	MemBytes          uint64  `json:"mem_bytes"`
	MemCapacityBytes  uint64  `json:"mem_capacity_bytes"`
	DiskBytes         uint64  `json:"disk_bytes"`
	DiskLogicalBytes  uint64  `json:"disk_logical_bytes"`
	DiskCapacityBytes uint64  `json:"disk_capacity_bytes"`
	NetRxBytes        uint64  `json:"net_rx_bytes"`
	NetTxBytes        uint64  `json:"net_tx_bytes"`
}

// vmsLiveResponse is the JSON response for GET /api/vms/usage/live.
type vmsLiveResponse struct {
	VMs []vmsLiveVM `json:"vms"`
}

// HandleAPIVMsLive handles GET /api/vms/usage/live.
// Returns live metrics for all VMs owned by the user.
// Returns empty vms when EnforcePlanCPUMax is off (metrics not yet validated).
func (s *Server) HandleAPIVMsLive(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()

	if !s.env.EnforcePlanCPUMax {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(vmsLiveResponse{VMs: []vmsLiveVM{}})
		return
	}

	// Fetch live metrics for all user's VMs via exelet gRPC.
	usageRows, err := s.sshServer.fetchVMUsageForUser(ctx, userID)

	vms := make([]vmsLiveVM, 0, len(usageRows))
	if err == nil {
		for _, row := range usageRows {
			vms = append(vms, vmsLiveVM{
				Name:              row.Name,
				Status:            row.Status,
				CPUPercent:        row.CPUPercent,
				CPUs:              row.CPUs,
				MemBytes:          row.MemBytes,
				MemCapacityBytes:  row.MemCapacity,
				DiskBytes:         row.DiskBytes,
				DiskLogicalBytes:  row.DiskLogicalBytes,
				DiskCapacityBytes: row.DiskCapacity,
				NetRxBytes:        row.NetRx,
				NetTxBytes:        row.NetTx,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(vmsLiveResponse{VMs: vms})
}
