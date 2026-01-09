package resourcemanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	api "exe.dev/pkg/api/exe/resource/v1"
)

const (
	// cgroupSlice is the parent slice for all VM cgroups
	cgroupSlice = "exelet.slice"
	// defaultGroupID is used for VMs without a group ID (backwards compatibility)
	defaultGroupID = "default"
	// cpuWeightNormal is the default CPU weight for active VMs
	cpuWeightNormal = 100
	// cpuWeightLow is the reduced CPU weight for idle VMs
	cpuWeightLow = 50
	// ioWeightNormal is the default IO weight for active VMs
	ioWeightNormal = 100
	// ioWeightLow is the reduced IO weight for idle VMs
	ioWeightLow = 50
	// memoryHighRatio is the fraction of allocated memory for memory.high on low priority VMs
	// When a VM exceeds this ratio, the kernel aggressively reclaims its memory
	memoryHighRatio = 0.8
)

// requiredControllers lists the cgroup controllers needed for priority management
var requiredControllers = []string{"cpu", "io", "memory"}

// initControllers attempts to enable required cgroup v2 controllers at the root
// and slice level. This must be called before any VMs are placed into cgroups.
func (m *ResourceManager) initControllers(ctx context.Context) {
	// First, try to enable controllers at the root cgroup level
	for _, ctrl := range requiredControllers {
		if err := m.enableController(m.cgroupRoot, ctrl); err != nil {
			m.log.WarnContext(ctx, "failed to enable cgroup controller at root - VM priority management may not work correctly",
				"controller", ctrl,
				"error", err,
				"hint", "ensure the process has write access to /sys/fs/cgroup/cgroup.subtree_control")
		} else {
			m.log.InfoContext(ctx, "enabled cgroup controller at root", "controller", ctrl)
		}
	}

	// Create and configure the slice
	slicePath := filepath.Join(m.cgroupRoot, cgroupSlice)
	if err := os.MkdirAll(slicePath, 0o755); err != nil {
		m.log.WarnContext(ctx, "failed to create cgroup slice", "path", slicePath, "error", err)
		return
	}

	// Enable controllers on the slice so child scopes can use them
	for _, ctrl := range requiredControllers {
		if err := m.enableController(slicePath, ctrl); err != nil {
			m.log.WarnContext(ctx, "failed to enable cgroup controller on slice",
				"controller", ctrl,
				"slice", cgroupSlice,
				"error", err)
		} else {
			m.log.DebugContext(ctx, "enabled cgroup controller on slice", "controller", ctrl, "slice", cgroupSlice)
		}
	}
}

// applyPriority applies the given priority to a VM's cgroup.
// groupID is used for per-account cgroup grouping (empty string uses default group).
// allocatedMemoryBytes is the VM's allocated memory size for calculating memory.high.
// If 0, memory.high is not set for LOW priority VMs.
func (m *ResourceManager) applyPriority(ctx context.Context, id string, groupID string, priority api.VMPriority, allocatedMemoryBytes uint64) error {
	// Get VM PID
	pid, err := m.getVMPID(ctx, id)
	if err != nil {
		return fmt.Errorf("get VM PID: %w", err)
	}

	// Ensure cgroup exists and VM process is in it
	cgroupPath, err := m.ensureCgroup(ctx, id, groupID, pid)
	if err != nil {
		return fmt.Errorf("ensure cgroup: %w", err)
	}

	// Set CPU and IO weights based on priority
	cpuWeight := cpuWeightNormal
	ioWeight := ioWeightNormal
	if priority == api.VMPriority_PRIORITY_LOW {
		cpuWeight = cpuWeightLow
		ioWeight = ioWeightLow
	}

	if err := m.setCPUWeight(cgroupPath, cpuWeight); err != nil {
		return fmt.Errorf("set CPU weight: %w", err)
	}

	if err := m.setIOWeight(cgroupPath, ioWeight); err != nil {
		// IO controller may not be available, log but don't fail
		m.log.DebugContext(ctx, "failed to set IO weight", "id", id, "error", err)
	}

	// Set memory controls based on priority
	// All VMs can swap (memory.swap.max = "max"), but priority determines reclaim order
	if err := m.setMemorySwapMax(cgroupPath, "max"); err != nil {
		// Memory controller may not be available, log but don't fail
		m.log.DebugContext(ctx, "failed to set memory swap max", "id", id, "error", err)
	}

	// Set memory.high for throttling
	// NORMAL: no throttling (max) - swapped last
	// LOW: throttled at memoryHighRatio of allocated - swapped first
	memoryHigh := "max"
	if priority == api.VMPriority_PRIORITY_LOW && allocatedMemoryBytes > 0 {
		memoryHigh = strconv.FormatUint(uint64(float64(allocatedMemoryBytes)*memoryHighRatio), 10)
	}
	if err := m.setMemoryHigh(cgroupPath, memoryHigh); err != nil {
		m.log.DebugContext(ctx, "failed to set memory high", "id", id, "error", err)
	}

	m.log.DebugContext(ctx, "applied cgroup weights",
		"id", id,
		"priority", priority.String(),
		"cpu_weight", cpuWeight,
		"io_weight", ioWeight,
		"memory_high", memoryHigh,
		"cgroup_path", cgroupPath)

	return nil
}

// ensureAccountSlice creates the account slice cgroup if it doesn't exist.
// Returns the path to the account slice.
func (m *ResourceManager) ensureAccountSlice(ctx context.Context, groupID string) (string, error) {
	if groupID == "" {
		groupID = defaultGroupID
	}

	sliceName := fmt.Sprintf("%s.slice", sanitizeCgroupName(groupID))
	slicePath := filepath.Join(m.cgroupRoot, cgroupSlice, sliceName)

	// Create the group slice if it doesn't exist.
	// Use MkdirAll to handle concurrent creation (EEXIST is not an error).
	created := false
	if _, err := os.Stat(slicePath); os.IsNotExist(err) {
		if err := os.MkdirAll(slicePath, 0o755); err != nil {
			return "", fmt.Errorf("create group slice: %w", err)
		}
		created = true
	}

	// Always try to enable controllers - they may have failed on initial creation
	// or may not be enabled yet if another goroutine just created the slice.
	for _, ctrl := range requiredControllers {
		if err := m.enableController(slicePath, ctrl); err != nil {
			m.log.WarnContext(ctx, "failed to enable cgroup controller on group slice",
				"controller", ctrl,
				"group_id", groupID,
				"error", err)
		}
	}

	if created {
		m.log.InfoContext(ctx, "created group cgroup slice", "group_id", groupID, "path", slicePath)
	}

	return slicePath, nil
}

// ensureCgroup ensures the cgroup exists for the VM and the process is in it.
// groupID is used for per-account cgroup grouping (empty string uses default group).
// Controllers should already be enabled via initControllers() at startup.
func (m *ResourceManager) ensureCgroup(ctx context.Context, id string, groupID string, pid int) (string, error) {
	// Ensure account slice exists first
	accountSlicePath, err := m.ensureAccountSlice(ctx, groupID)
	if err != nil {
		return "", fmt.Errorf("ensure account slice: %w", err)
	}

	scopeName := fmt.Sprintf("vm-%s.scope", sanitizeCgroupName(id))
	cgroupPath := filepath.Join(accountSlicePath, scopeName)

	// Create scope if it doesn't exist.
	// Use MkdirAll to handle concurrent creation (EEXIST is not an error).
	if err := os.MkdirAll(cgroupPath, 0o755); err != nil {
		return "", fmt.Errorf("create scope: %w", err)
	}

	// Move process to cgroup if not already there
	if err := m.moveProcessToCgroup(cgroupPath, pid); err != nil {
		return "", fmt.Errorf("move process: %w", err)
	}

	// Clean up old cgroups for this VM in other locations
	m.cleanupOldVMCgroups(ctx, id, scopeName, cgroupPath)

	return cgroupPath, nil
}

// cleanupOldVMCgroups removes any cgroups for this VM that aren't in the expected location.
// This handles migration from flat hierarchy and between account slices.
func (m *ResourceManager) cleanupOldVMCgroups(ctx context.Context, id, scopeName, currentPath string) {
	slicePath := filepath.Join(m.cgroupRoot, cgroupSlice)

	// Clean up old flat hierarchy cgroup (exelet.slice/vm-{id}.scope)
	oldFlatPath := filepath.Join(slicePath, scopeName)
	if oldFlatPath != currentPath {
		if _, err := os.Stat(oldFlatPath); err == nil {
			if err := os.Remove(oldFlatPath); err == nil {
				m.log.InfoContext(ctx, "removed old flat cgroup", "id", id, "path", oldFlatPath)
			}
		}
	}

	// Scan group slices and clean up this VM's scope from any other slice
	entries, err := os.ReadDir(slicePath)
	if err != nil {
		return
	}

	for _, entry := range entries {
		// Look for directories ending in .slice (group slices)
		// Skip non-directories and non-.slice entries
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".slice") {
			continue
		}

		oldGroupPath := filepath.Join(slicePath, entry.Name(), scopeName)
		if oldGroupPath == currentPath {
			continue // This is the current location, skip
		}

		if _, err := os.Stat(oldGroupPath); err == nil {
			// Found VM scope in a different group slice, remove it
			if err := os.Remove(oldGroupPath); err == nil {
				m.log.InfoContext(ctx, "removed old group cgroup", "id", id, "path", oldGroupPath)
				// Try to remove empty group slice
				groupSlicePath := filepath.Join(slicePath, entry.Name())
				if err := os.Remove(groupSlicePath); err == nil {
					m.log.InfoContext(ctx, "removed empty group slice", "path", groupSlicePath)
				}
			}
		}
	}
}

// moveProcessToCgroup moves a process to a cgroup.
func (m *ResourceManager) moveProcessToCgroup(cgroupPath string, pid int) error {
	procsFile := filepath.Join(cgroupPath, "cgroup.procs")

	// Check if process is already in this cgroup
	data, err := os.ReadFile(procsFile)
	if err == nil {
		pidStr := strconv.Itoa(pid)
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == pidStr {
				return nil // Already in cgroup
			}
		}
	}

	// Move process to cgroup
	return os.WriteFile(procsFile, []byte(strconv.Itoa(pid)), 0o644)
}

// setCPUWeight sets the cpu.weight for a cgroup.
func (m *ResourceManager) setCPUWeight(cgroupPath string, weight int) error {
	weightFile := filepath.Join(cgroupPath, "cpu.weight")
	return os.WriteFile(weightFile, []byte(strconv.Itoa(weight)), 0o644)
}

// setIOWeight sets the io.weight for a cgroup.
// The io.weight file accepts "default <weight>" format.
func (m *ResourceManager) setIOWeight(cgroupPath string, weight int) error {
	weightFile := filepath.Join(cgroupPath, "io.weight")
	return os.WriteFile(weightFile, []byte("default "+strconv.Itoa(weight)), 0o644)
}

// setMemorySwapMax sets memory.swap.max for a cgroup.
// Use "max" for unlimited swap or "0" to disable swap.
func (m *ResourceManager) setMemorySwapMax(cgroupPath, value string) error {
	swapFile := filepath.Join(cgroupPath, "memory.swap.max")
	return os.WriteFile(swapFile, []byte(value), 0o644)
}

// setMemoryHigh sets memory.high for a cgroup.
// Use "max" for no limit, or a byte value as string.
// When usage exceeds this value, the kernel aggressively reclaims memory.
func (m *ResourceManager) setMemoryHigh(cgroupPath, value string) error {
	highFile := filepath.Join(cgroupPath, "memory.high")
	return os.WriteFile(highFile, []byte(value), 0o644)
}

// enableController enables a controller on a cgroup.
func (m *ResourceManager) enableController(cgroupPath, controller string) error {
	subtreeControl := filepath.Join(cgroupPath, "cgroup.subtree_control")

	// Read current controllers (space-delimited list)
	data, err := os.ReadFile(subtreeControl)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Check if already enabled by tokenizing the space-delimited list.
	// This avoids false positives like "cpuset" matching "cpu".
	for _, ctrl := range strings.Fields(string(data)) {
		if ctrl == controller {
			return nil
		}
	}

	// Enable controller
	return os.WriteFile(subtreeControl, []byte("+"+controller), 0o644)
}

// removeCgroup removes the cgroup for a VM and cleans up empty account slices.
func (m *ResourceManager) removeCgroup(ctx context.Context, id string, groupID string) error {
	if groupID == "" {
		groupID = defaultGroupID
	}

	sliceName := fmt.Sprintf("%s.slice", sanitizeCgroupName(groupID))
	accountSlicePath := filepath.Join(m.cgroupRoot, cgroupSlice, sliceName)

	scopeName := fmt.Sprintf("vm-%s.scope", sanitizeCgroupName(id))
	cgroupPath := filepath.Join(accountSlicePath, scopeName)

	// Remove the VM's cgroup
	if _, err := os.Stat(cgroupPath); os.IsNotExist(err) {
		// Already gone, but still try to clean up account slice
	} else if err := os.Remove(cgroupPath); err != nil {
		m.log.WarnContext(ctx, "failed to remove VM cgroup", "id", id, "error", err)
	}

	// Try to remove the account slice if it's now empty
	// os.Remove will fail if directory is not empty, which is expected
	if err := os.Remove(accountSlicePath); err == nil {
		m.log.InfoContext(ctx, "removed empty account cgroup slice", "group_id", groupID)
	}

	// Also clean up old-style cgroup if it exists (migration cleanup)
	oldCgroupPath := filepath.Join(m.cgroupRoot, cgroupSlice, scopeName)
	if _, err := os.Stat(oldCgroupPath); err == nil {
		os.Remove(oldCgroupPath) // Best effort
	}

	return nil
}

// sanitizeCgroupName converts an ID to a valid cgroup name component.
func sanitizeCgroupName(id string) string {
	// Replace any characters that might be problematic in paths
	return strings.ReplaceAll(id, "/", "_")
}
