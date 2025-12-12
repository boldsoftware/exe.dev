package resourcemanager

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	computeapi "exe.dev/pkg/api/exe/compute/v1"
	api "exe.dev/pkg/api/exe/resource/v1"
)

// GetNodeStatus returns the current node capacity and allocation summary.
func (m *ResourceManager) GetNodeStatus(ctx context.Context, req *api.GetNodeStatusRequest) (*api.GetNodeStatusResponse, error) {
	// Get capacity
	cpus, memoryBytes, diskBytes, err := m.capacity.Get(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get capacity: %v", err)
	}

	capacity := &api.NodeCapacity{
		CPUs:        cpus,
		MemoryBytes: memoryBytes,
		DiskBytes:   diskBytes,
	}

	// Get instances for allocation calculation
	instances, err := m.context.ComputeService.Instances(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list instances: %v", err)
	}

	allocation := &api.NodeAllocation{}
	for _, inst := range instances {
		// Only count running instances for allocation
		if inst.GetState() == computeapi.VMState_RUNNING || inst.GetState() == computeapi.VMState_STARTING {
			if cfg := inst.GetVMConfig(); cfg != nil {
				allocation.CPUs += cfg.GetCPUs()
				allocation.MemoryBytes += cfg.GetMemory()
				allocation.DiskBytes += cfg.GetDisk()
			}
		}
	}

	return &api.GetNodeStatusResponse{
		Capacity:   capacity,
		Allocation: allocation,
	}, nil
}

// GetVMUsage returns usage information for a specific VM.
func (m *ResourceManager) GetVMUsage(ctx context.Context, req *api.GetVMUsageRequest) (*api.GetVMUsageResponse, error) {
	if req.VmID == "" {
		return nil, status.Error(codes.InvalidArgument, "vm_id is required")
	}

	m.usageMu.Lock()
	state, exists := m.usageState[req.VmID]
	m.usageMu.Unlock()

	if !exists {
		return nil, status.Errorf(codes.NotFound, "no usage data for VM %s", req.VmID)
	}

	return &api.GetVMUsageResponse{
		Usage: &api.VMUsage{
			ID:           req.VmID,
			Name:         state.name,
			CpuSeconds:   state.cpuSeconds,
			CpuPercent:   state.cpuPercent,
			MemoryBytes:  state.memoryBytes,
			DiskBytes:    state.diskBytes,
			NetRxBytes:   state.netRxBytes,
			NetTxBytes:   state.netTxBytes,
			LastActivity: state.lastActivity.UnixNano(),
			Priority:     state.priority,
		},
	}, nil
}

// SetVMPriority manually sets the priority for a VM.
func (m *ResourceManager) SetVMPriority(ctx context.Context, req *api.SetVMPriorityRequest) (*api.SetVMPriorityResponse, error) {
	if req.VmID == "" {
		return nil, status.Error(codes.InvalidArgument, "vm_id is required")
	}

	// Store the override
	m.priorityMu.Lock()
	m.priorityOverride[req.VmID] = req.Priority
	m.priorityMu.Unlock()

	// Apply immediately
	if err := m.applyPriority(ctx, req.VmID, req.Priority); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to apply priority: %v", err)
	}

	m.log.InfoContext(ctx, "priority manually set", "vm_id", req.VmID, "priority", req.Priority)

	return &api.SetVMPriorityResponse{}, nil
}
