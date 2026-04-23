package execore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"exe.dev/billing/plan"
	"exe.dev/desiredstate"
	"exe.dev/exedb"
)

// handleExeletDesired serves the desired state for an exelet host.
// This is called by exelets to discover what cgroup settings they should enforce.
// Access is restricted to localhost/Tailscale via requireLocalAccess.
//
// TODO: enable exelet reading this (flip --desired-state-sync default)
// TODO: handle desired VM state (running/stopped)
// TODO: connect with abuse buttons
func (s *Server) handleExeletDesired(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	if host == "" {
		http.Error(w, "missing host parameter", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Verify this is a known exelet host
	if s.getExeletClient(host) == nil {
		http.Error(w, "unknown exelet host", http.StatusNotFound)
		return
	}

	// Get all boxes on this host
	boxes, err := s.getBoxesByHost(ctx, host)
	switch {
	case errors.Is(err, context.Canceled):
		return
	case err != nil:
		s.slog().ErrorContext(ctx, "failed to get boxes for exelet desired state", "host", host, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Build the desired state response.
	// Group VMs by their owner (user_id), which is used as the cgroup group ID.
	// Also track max allocated CPUs per user on this host for default group limits.
	groupSet := make(map[string]bool)
	userMaxCPUs := make(map[string]int64) // user_id -> max allocated_cpus across their VMs
	var vms []desiredstate.VM
	for _, box := range boxes {
		var containerID string
		if box.ContainerID != nil {
			containerID = *box.ContainerID
		}
		if containerID == "" {
			// No container ID means the VM isn't created yet; skip it.
			continue
		}

		groupID := box.CreatedByUserID
		groupSet[groupID] = true

		// Track max allocated CPUs per user for default group limits.
		if box.AllocatedCpus != nil && *box.AllocatedCpus > userMaxCPUs[groupID] {
			userMaxCPUs[groupID] = *box.AllocatedCpus
		}

		state := "running"
		if box.Status == "stopped" {
			state = "stopped"
		}

		// Build per-VM cgroup settings.
		var cgroups []desiredstate.CgroupSetting

		// Set cpu.max based on allocated CPUs.
		// cpu.max format: "{quota} {period}" where period=100000 (100ms).
		// For N CPUs: quota = N * period, limiting to N cores of host CPU time.
		if box.AllocatedCpus != nil {
			const period = 100000
			quota := *box.AllocatedCpus * period
			cgroups = append(cgroups, desiredstate.CgroupSetting{
				Path:  "cpu.max",
				Value: fmt.Sprintf("%d %d", quota, period),
			})
		}

		// Apply per-VM cgroup overrides from the database.
		if box.CgroupOverrides != nil {
			overrides := desiredstate.ParseOverrides(*box.CgroupOverrides)
			cgroups = desiredstate.ApplyOverrides(cgroups, overrides)
		}

		vms = append(vms, desiredstate.VM{
			ID:     containerID,
			Group:  groupID,
			State:  state,
			Cgroup: cgroups,
		})
	}

	// Fetch user-level cgroup overrides. Instead of JOINing boxes→users per host,
	// fetch all users with overrides (a small set) and filter to users on this host.
	allOverrides, err := withRxRes0(s, ctx, (*exedb.Queries).GetAllUserCgroupOverrides)
	switch {
	case errors.Is(err, context.Canceled):
		return
	case err != nil:
		s.slog().ErrorContext(ctx, "failed to get user cgroup overrides for exelet desired state", "host", host, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	userOverrides := make(map[string][]desiredstate.CgroupSetting)
	for _, row := range allOverrides {
		if groupSet[row.UserID] {
			userOverrides[row.UserID] = desiredstate.ParseOverrides(*row.CgroupOverrides)
		}
	}

	// When EnforcePlanCPUMax is enabled, look up each user's plan to get
	// the tier's MaxCPUs for account-level enforcement.
	userPlanMaxCPUs := make(map[string]uint64) // user_id -> plan MaxCPUs (0 = no limit)
	if s.env.EnforcePlanCPUMax {
		for groupID := range groupSet {
			planRow, err := withRxRes1(s, ctx, (*exedb.Queries).GetActivePlanForUser, groupID)
			if err != nil {
				// User has no plan; fall through to 2x heuristic.
				continue
			}
			userPlanMaxCPUs[groupID] = plan.MaxCPUsForPlan(planRow.PlanID)
		}
	}

	// Build groups (one per unique user_id on this host).
	//
	// Priority for account-slice cpu.max:
	//   1. User cgroup overrides (abuse throttle or VIP boost) — always wins.
	//   2. Plan tier MaxCPUs (when EnforcePlanCPUMax is on and MaxCPUs > 0).
	//   3. Fallback: 2x the max allocated CPUs across the user's VMs.
	var groups []desiredstate.Group
	for groupID := range groupSet {
		var cgroups []desiredstate.CgroupSetting

		const period = 100000
		if planMax := userPlanMaxCPUs[groupID]; planMax > 0 {
			// Plan-based enforcement: cap account to tier's vCPU pool.
			quota := int64(planMax) * period
			cgroups = append(cgroups, desiredstate.CgroupSetting{
				Path:  "cpu.max",
				Value: fmt.Sprintf("%d %d", quota, period),
			})
		} else if maxCPUs := userMaxCPUs[groupID]; maxCPUs > 0 {
			// Heuristic fallback: 2x max allocated CPUs for burst headroom.
			quota := maxCPUs * 2 * period
			cgroups = append(cgroups, desiredstate.CgroupSetting{
				Path:  "cpu.max",
				Value: fmt.Sprintf("%d %d", quota, period),
			})
		}

		// User-level cgroup overrides are applied on top — they always win.
		if overrides, ok := userOverrides[groupID]; ok {
			cgroups = desiredstate.ApplyOverrides(cgroups, overrides)
		}
		groups = append(groups, desiredstate.Group{
			Name:   groupID,
			Cgroup: cgroups,
		})
	}

	resp := desiredstate.DesiredState{
		Groups: groups,
		VMs:    vms,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.slog().ErrorContext(ctx, "failed to encode exelet desired state", "error", err)
	}
}
