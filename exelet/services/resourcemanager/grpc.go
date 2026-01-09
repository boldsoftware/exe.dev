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

// ListVMUsage streams usage information for all VMs.
func (m *ResourceManager) ListVMUsage(req *api.ListVMUsageRequest, stream api.ResourceManagerService_ListVMUsageServer) error {
	m.usageMu.Lock()
	defer m.usageMu.Unlock()

	for id, state := range m.usageState {
		if err := stream.Send(&api.ListVMUsageResponse{
			Usage: &api.VMUsage{
				ID:           id,
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
		}); err != nil {
			return err
		}
	}

	return nil
}

// SetVMPriority manually sets the priority for a VM.
// Use PRIORITY_AUTO to clear the override and return to automatic detection.
func (m *ResourceManager) SetVMPriority(ctx context.Context, req *api.SetVMPriorityRequest) (*api.SetVMPriorityResponse, error) {
	if req.VmID == "" {
		return nil, status.Error(codes.InvalidArgument, "vm_id is required")
	}

	// Handle auto mode - clear override and let automatic detection take over
	if req.Priority == api.VMPriority_PRIORITY_AUTO {
		m.priorityMu.Lock()
		delete(m.priorityOverride, req.VmID)
		m.priorityMu.Unlock()

		m.log.InfoContext(ctx, "priority set to auto", "vm_id", req.VmID)
		return &api.SetVMPriorityResponse{}, nil
	}

	// Store the override for manual priority
	m.priorityMu.Lock()
	m.priorityOverride[req.VmID] = req.Priority
	m.priorityMu.Unlock()

	// Get allocated memory, groupID and update state priority
	var allocatedMemoryBytes uint64
	var groupID string
	m.usageMu.Lock()
	if state, ok := m.usageState[req.VmID]; ok {
		allocatedMemoryBytes = state.allocatedMemoryBytes
		groupID = state.groupID
		state.priority = req.Priority // Update state so GetVMUsage reflects the change immediately
	}
	m.usageMu.Unlock()

	// Apply immediately
	if err := m.applyPriority(ctx, req.VmID, groupID, req.Priority, allocatedMemoryBytes); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to apply priority: %v", err)
	}

	m.log.InfoContext(ctx, "priority manually set", "vm_id", req.VmID, "priority", req.Priority)

	return &api.SetVMPriorityResponse{}, nil
}
