package execore

import (
	"encoding/json"
	"net/http"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
)

// vmsLiveVM is a single VM's live metrics in the /api/vms/live response.
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

// vmsLivePool is the pre-computed pool summary in the /api/vms/live response.
type vmsLivePool struct {
	CPUUsed      float64 `json:"cpu_used"`       // sum of cpu_percent/100 across running VMs
	CPUMax       uint64  `json:"cpu_max"`        // plan MaxCPUs (0 = unlimited)
	MemUsedBytes uint64  `json:"mem_used_bytes"` // sum of mem_bytes (actual usage) across running VMs
	MemMaxBytes  uint64  `json:"mem_max_bytes"`  // plan MaxMemory (0 = unlimited)
}

// vmsLiveResponse is the JSON response for GET /api/vms/live.
type vmsLiveResponse struct {
	VMs  []vmsLiveVM `json:"vms"`
	Pool vmsLivePool `json:"pool"`
}

// HandleAPIVMsLive handles GET /api/vms/live.
// Returns live metrics for all VMs owned by the user, plus a pool summary.
func (s *Server) HandleAPIVMsLive(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()

	// Fetch live metrics for all user's VMs via exelet gRPC.
	usageRows, err := s.sshServer.fetchVMUsageForUser(ctx, userID)

	vms := make([]vmsLiveVM, 0, len(usageRows))
	var cpuUsed float64
	var memUsed uint64

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
			if row.Status == "running" {
				cpuUsed += row.CPUPercent / 100.0
				memUsed += row.MemBytes
			}
		}
	}
	// If fetchVMUsageForUser fails (exelet unreachable), return empty VMs
	// with pool limits from the plan. The frontend can show "metrics unavailable".

	// Look up plan limits.
	var cpuMax uint64
	var memMax uint64
	planRow, planErr := withRxRes1(s, ctx, (*exedb.Queries).GetActivePlanForUser, userID)
	if planErr == nil {
		cpuMax = plan.MaxCPUsForPlan(planRow.PlanID)
		memMax = plan.MaxMemoryForPlan(planRow.PlanID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(vmsLiveResponse{
		VMs: vms,
		Pool: vmsLivePool{
			CPUUsed:      cpuUsed,
			CPUMax:       cpuMax,
			MemUsedBytes: memUsed,
			MemMaxBytes:  memMax,
		},
	})
}
