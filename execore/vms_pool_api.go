package execore

import (
	"encoding/json"
	"net/http"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
)

// vmsPoolResponse is the JSON response for GET /api/vms/pool.
type vmsPoolResponse struct {
	PlanName          string `json:"plan_name"`
	TierName          string `json:"tier_name"`
	CPUMax            uint64 `json:"cpu_max"`
	MemMaxBytes       uint64 `json:"mem_max_bytes"`
	CPUAllocated      int64  `json:"cpu_allocated"`
	MemAllocatedBytes int64  `json:"mem_allocated_bytes"`
	MaxVMs            int    `json:"max_vms"`
	VMsTotal          int    `json:"vms_total"`
	VMsRunning        int    `json:"vms_running"`
}

// HandleAPIVMsPool handles GET /api/vms/pool.
// Returns the user's plan capacity and VM counts.
func (s *Server) HandleAPIVMsPool(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()

	var resp vmsPoolResponse

	// Look up plan tier.
	planRow, err := withRxRes1(s, ctx, (*exedb.Queries).GetActivePlanForUser, userID)
	if err == nil {
		version := plan.Base(planRow.PlanID)
		resp.PlanName = plan.Name(version)
		if tier, tierErr := plan.GetTierByID(planRow.PlanID); tierErr == nil {
			resp.TierName = tier.Name
			resp.CPUMax = tier.Quotas.MaxCPUs
			resp.MemMaxBytes = tier.Quotas.MaxMemory
			resp.MaxVMs = tier.Quotas.MaxUserVMs
		}
	}

	// Count VMs and sum allocations.
	boxes, err := withRxRes1(s, ctx, (*exedb.Queries).BoxesForUser, userID)
	if err == nil {
		resp.VMsTotal = len(boxes)
		for _, b := range boxes {
			if b.Status != "running" {
				continue
			}
			resp.VMsRunning++
			if b.AllocatedCpus != nil {
				resp.CPUAllocated += *b.AllocatedCpus
			}
			resp.MemAllocatedBytes += b.MemoryCapacityBytes
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
