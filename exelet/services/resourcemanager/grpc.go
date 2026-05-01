package resourcemanager

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/guestmetrics"
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

	// Snapshot the proto AND groupID under the lock, so we don't read
	// the mutable vmUsageState concurrently with the poller.
	m.usageMu.Lock()
	state, exists := m.usageState[req.VmID]
	if !exists {
		m.usageMu.Unlock()
		return nil, status.Errorf(codes.NotFound, "no usage data for VM %s", req.VmID)
	}
	groupID := state.groupID
	u := vmUsageProto(req.VmID, state, m.guestMemoryProto(req.VmID))
	m.usageMu.Unlock()

	m.maybeFillExt4Usage(ctx, req.VmID, groupID, req.GetCollectFilesystemUsage(), u)
	return &api.GetVMUsageResponse{Usage: u}, nil
}

// vmUsageProto builds an api.VMUsage from a vmUsageState. Caller passes a
// guest-memory snapshot if any; nil renders the field as omitted. It does
// NOT populate the fs_*_bytes fields: those are gated and filled by
// maybeFillExt4Usage on the request paths that opt in.
func vmUsageProto(id string, state *vmUsageState, guest *api.GuestMemoryStats) *api.VMUsage {
	return &api.VMUsage{
		GuestMemory:             guest,
		ID:                      id,
		Name:                    state.name,
		CpuSeconds:              state.cpuSeconds,
		CpuPercent:              state.cpuPercent,
		MemoryBytes:             state.memoryBytes,
		SwapBytes:               state.swapBytes,
		DiskBytes:               state.diskBytes,
		DiskLogicalBytes:        state.diskLogicalBytes,
		DiskCapacityBytes:       state.diskVolsizeBytes,
		MemCapacityBytes:        state.allocatedMemoryBytes,
		CPUs:                    state.allocatedCPUs,
		NetRxBytes:              state.netRxBytes,
		NetTxBytes:              state.netTxBytes,
		Priority:                state.priority,
		MemoryAnonBytes:         state.memoryAnonBytes,
		MemoryFileBytes:         state.memoryFileBytes,
		MemoryKernelBytes:       state.memoryKernelBytes,
		MemoryShmemBytes:        state.memoryShmemBytes,
		MemorySlabBytes:         state.memorySlabBytes,
		MemoryInactiveFileBytes: state.memoryInactiveFileBytes,
	}
}

// guestMemoryProto returns a freshly-populated GuestMemoryStats for the
// given VM, or nil when no fresh sample is available.
func (m *ResourceManager) guestMemoryProto(id string) *api.GuestMemoryStats {
	if m.guestPool == nil {
		return nil
	}
	if s, ok := m.guestPool.LatestFresh(id, time.Now()); ok {
		return sampleToGuestMemoryProto(s, m.guestPool.RefaultRate(id, 60*time.Second))
	}
	// No fresh sample. If the VM is Frozen, wake it so the next tick
	// fires a scrape — the caller is actively looking.
	if t, ok := m.guestPool.VMTier(id); ok && t == guestmetrics.VMTierFrozen {
		m.guestPool.WakeForRPC(id)
	}
	return nil
}

func sampleToGuestMemoryProto(s guestmetrics.Sample, refaultRate float64) *api.GuestMemoryStats {
	return &api.GuestMemoryStats{
		CapturedAtUnixNano:    s.CapturedAt.UnixNano(),
		FetchedAtUnixNano:     s.FetchedAt.UnixNano(),
		UptimeSec:             s.UptimeSec,
		MemTotalBytes:         s.MemTotalBytes,
		MemAvailableBytes:     s.MemAvailableBytes,
		CachedBytes:           s.CachedBytes,
		ActiveFileBytes:       s.ActiveFileBytes,
		InactiveFileBytes:     s.InactiveFileBytes,
		MlockedBytes:          s.MlockedBytes,
		DirtyBytes:            s.DirtyBytes,
		SwapTotalBytes:        s.SwapTotalBytes,
		SwapFreeBytes:         s.SwapFreeBytes,
		SreclaimableBytes:     s.SReclaimableBytes,
		ReclaimableBytes:      s.ReclaimableBytes(),
		WorkingsetRefaultFile: s.WorkingsetRefaultFile,
		WorkingsetRefaultAnon: s.WorkingsetRefaultAnon,
		Pgmajfault:            s.Pgmajfault,
		PsiAvailable:          s.PSIAvailable,
		PsiSomeAvg10:          s.PSISome.Avg10,
		PsiSomeAvg60:          s.PSISome.Avg60,
		PsiSomeAvg300:         s.PSISome.Avg300,
		PsiFullAvg10:          s.PSIFull.Avg10,
		PsiFullAvg60:          s.PSIFull.Avg60,
		PsiFullAvg300:         s.PSIFull.Avg300,
		RefaultRate:           refaultRate,
	}
}

// maybeFillExt4Usage performs an on-demand ext4 superblock probe and
// fills u.Fs*Bytes when:
//
//   - The caller asked for it (collectRequested), AND
//   - The exelet's gate (env-wide flag or per-group allow-list) permits
//     it for this VM's groupID.
//
// Otherwise the fs_*_bytes fields are left at zero.
func (m *ResourceManager) maybeFillExt4Usage(ctx context.Context, id, groupID string, collectRequested bool, u *api.VMUsage) {
	if !collectRequested || u == nil {
		return
	}
	if !m.ext4UsageAllowed(groupID) {
		return
	}
	readFn := m.readFilesystemUsageFn
	if readFn == nil {
		readFn = m.readFilesystemUsage
	}
	fs, ok := readFn(ctx, id)
	if !ok {
		return
	}
	u.FsTotalBytes = fs.TotalBytes()
	u.FsFreeBytes = fs.FreeBytes()
	u.FsAvailableBytes = fs.AvailableBytes()
	u.FsUsedBytes = fs.UsedBytes()
}

// ListVMUsage streams usage information for all VMs.
//
// We snapshot the usage map (and per-id guest-memory protos) under
// usageMu, then release the lock before issuing any stream.Send.
// Holding usageMu across an RPC write would block the poll loop (which
// also takes usageMu) for as long as the gRPC client takes to drain.
func (m *ResourceManager) ListVMUsage(req *api.ListVMUsageRequest, stream api.ResourceManagerService_ListVMUsageServer) error {
	// Snapshot under the lock; do the I/O for ext4 outside of it so a
	// stalled zvol can't block other usage state mutations.
	type snapshot struct {
		id      string
		groupID string
		usage   *api.VMUsage
	}
	m.usageMu.Lock()
	snapshots := make([]snapshot, 0, len(m.usageState))
	for id, state := range m.usageState {
		// guestMemoryProto reads from m.guestPool (its own lock); it
		// is safe to call here, and we want the guest snapshot to line
		// up with the usage snapshot we are emitting.
		snapshots = append(snapshots, snapshot{
			id:      id,
			groupID: state.groupID,
			usage:   vmUsageProto(id, state, m.guestMemoryProto(id)),
		})
	}
	m.usageMu.Unlock()

	collect := req.GetCollectFilesystemUsage()
	for _, s := range snapshots {
		m.maybeFillExt4Usage(stream.Context(), s.id, s.groupID, collect, s.usage)
		if err := stream.Send(&api.ListVMUsageResponse{Usage: s.usage}); err != nil {
			return err
		}
	}
	return nil
}

// SetVMPriority manually sets the priority for a VM.
// Use PRIORITY_AUTO to clear the override and return to normal priority.
func (m *ResourceManager) SetVMPriority(ctx context.Context, req *api.SetVMPriorityRequest) (*api.SetVMPriorityResponse, error) {
	if req.VmID == "" {
		return nil, status.Error(codes.InvalidArgument, "vm_id is required")
	}

	// Handle auto mode - clear override and apply normal priority
	if req.Priority == api.VMPriority_PRIORITY_AUTO {
		m.priorityMu.Lock()
		delete(m.priorityOverride, req.VmID)
		m.priorityMu.Unlock()

		// Apply NORMAL priority immediately if the VM is tracked
		var allocatedMemoryBytes uint64
		var groupID string
		var hasState bool
		m.usageMu.Lock()
		if state, ok := m.usageState[req.VmID]; ok {
			hasState = true
			allocatedMemoryBytes = state.allocatedMemoryBytes
			groupID = state.groupID
			state.priority = api.VMPriority_PRIORITY_NORMAL
		}
		m.usageMu.Unlock()

		if hasState {
			if err := m.applyPriority(ctx, req.VmID, groupID, api.VMPriority_PRIORITY_NORMAL, allocatedMemoryBytes); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to apply priority: %v", err)
			}
		}

		m.log.InfoContext(ctx, "priority set to auto (normal)", "vm_id", req.VmID)
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

// GetMachineUsage returns current usage stats for the host machine.
func (m *ResourceManager) GetMachineUsage(ctx context.Context, req *api.GetMachineUsageRequest) (*api.GetMachineUsageResponse, error) {
	return m.machineUsage(ctx)
}

// SetMachineUsage sets availability and usage states for the host machine.
// This is for testing, and perhaps decommissioning.
func (m *ResourceManager) SetMachineUsage(ctx context.Context, req *api.SetMachineUsageRequest) (*api.SetMachineUsageResponse, error) {
	if err := m.setMachineUsage(ctx, req); err != nil {
		return nil, err
	}
	return &api.SetMachineUsageResponse{}, nil
}
