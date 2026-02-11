package execore

import (
	"encoding/json"
	"fmt"
	"net/http"

	"exe.dev/desiredstate"
)

// handleExeletDesired serves the desired state for an exelet host.
// This is called by exelets to discover what cgroup settings they should enforce.
// Access is restricted to localhost/Tailscale via requireLocalAccess.
//
// TODO: enable exelet reading this (flip --desired-state-sync default)
// TODO: add CLI commands to set cgroup overrides in DB and publish them here
// TODO: handle desired VM state (running/stopped)
// TODO: more cgroups besides cpu.max (cpu.weight, io.weight, memory.swap.max)
// TODO: create per-user limits as well
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
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to get boxes for exelet desired state", "host", host, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Build the desired state response.
	// Group VMs by their owner (user_id), which is used as the cgroup group ID.
	groupSet := make(map[string]bool)
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

		vms = append(vms, desiredstate.VM{
			ID:     containerID,
			Group:  groupID,
			State:  state,
			Cgroup: cgroups,
		})
	}

	// Build groups (one per unique user_id on this host).
	// For now, no group-level cgroup overrides.
	var groups []desiredstate.Group
	for groupID := range groupSet {
		groups = append(groups, desiredstate.Group{
			Name:   groupID,
			Cgroup: []desiredstate.CgroupSetting{},
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
