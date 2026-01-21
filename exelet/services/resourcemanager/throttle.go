package resourcemanager

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/vmm/cloudhypervisor/client"
	api "exe.dev/pkg/api/exe/resource/v1"
)

// ThrottleVM applies or clears resource throttling for a VM.
func (m *ResourceManager) ThrottleVM(ctx context.Context, req *api.ThrottleVMRequest) (*api.ThrottleVMResponse, error) {
	// Validate request
	if req.VmID == "" {
		return nil, status.Error(codes.InvalidArgument, "vm_id is required")
	}

	// Validate that clear is not combined with other fields
	if req.Clear && (req.CpuPercent != nil || req.MemoryPercent != nil) {
		return nil, status.Error(codes.InvalidArgument, "clear cannot be combined with cpu or memory options")
	}

	// Validate cpu_percent > 0
	if req.CpuPercent != nil && *req.CpuPercent == 0 {
		return nil, status.Error(codes.InvalidArgument, "cpu_percent must be greater than 0")
	}

	// Validate 1 <= memory_percent <= 100
	if req.MemoryPercent != nil {
		if *req.MemoryPercent == 0 || *req.MemoryPercent > 100 {
			return nil, status.Error(codes.InvalidArgument, "memory_percent must be between 1 and 100")
		}
	}

	// Validate that at least one option is specified
	if !req.Clear && req.CpuPercent == nil && req.MemoryPercent == nil {
		return nil, status.Error(codes.InvalidArgument, "at least one of clear, cpu_percent, or memory_percent is required")
	}

	// Get VM PID for cgroup operations
	pid, err := m.getVMPID(ctx, req.VmID)
	if err != nil {
		return nil, classifyVMPIDError(err)
	}

	// Get group ID for cgroup path
	var groupID string
	m.usageMu.Lock()
	if state, ok := m.usageState[req.VmID]; ok {
		groupID = state.groupID
	}
	m.usageMu.Unlock()

	// Ensure cgroup exists and get path
	cgroupPath, err := m.ensureCgroup(ctx, req.VmID, groupID, pid)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to ensure cgroup: %v", err)
	}

	if req.Clear {
		return m.clearThrottling(ctx, req.VmID, cgroupPath)
	}

	return m.applyThrottling(ctx, req, cgroupPath)
}

// applyThrottling applies the requested throttling settings.
func (m *ResourceManager) applyThrottling(ctx context.Context, req *api.ThrottleVMRequest, cgroupPath string) (*api.ThrottleVMResponse, error) {
	// CPU throttling
	if req.CpuPercent != nil {
		if err := m.setCPUMax(cgroupPath, *req.CpuPercent); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to set CPU throttle: %v", err)
		}
		m.log.InfoContext(ctx, "applied CPU throttle", "vm_id", req.VmID, "cpu_percent", *req.CpuPercent)
	}

	// Memory throttling
	if req.MemoryPercent != nil {
		// Get allocated memory for this VM
		var allocatedMemoryBytes uint64
		m.usageMu.Lock()
		if state, ok := m.usageState[req.VmID]; ok {
			allocatedMemoryBytes = state.allocatedMemoryBytes
		}
		m.usageMu.Unlock()

		if allocatedMemoryBytes == 0 {
			return nil, status.Errorf(codes.FailedPrecondition, "cannot set memory throttle: allocated memory unknown for VM %s", req.VmID)
		}

		if err := m.setMemoryHighPercent(cgroupPath, *req.MemoryPercent, allocatedMemoryBytes); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to set memory throttle: %v", err)
		}
		m.log.InfoContext(ctx, "applied memory throttle", "vm_id", req.VmID, "memory_percent", *req.MemoryPercent)
	}

	return &api.ThrottleVMResponse{}, nil
}

// clearThrottling removes all throttling from a VM.
func (m *ResourceManager) clearThrottling(ctx context.Context, vmID, cgroupPath string) (*api.ThrottleVMResponse, error) {
	var errs []string

	// Clear CPU throttle by setting max (unlimited)
	if err := m.clearCPUMax(cgroupPath); err != nil {
		m.log.WarnContext(ctx, "failed to clear CPU throttle", "vm_id", vmID, "error", err)
		errs = append(errs, fmt.Sprintf("cpu: %v", err))
	}

	// Clear memory throttle by setting max (unlimited)
	if err := m.setMemoryHigh(cgroupPath, "max"); err != nil {
		m.log.WarnContext(ctx, "failed to clear memory throttle", "vm_id", vmID, "error", err)
		errs = append(errs, fmt.Sprintf("memory: %v", err))
	}

	if len(errs) > 0 {
		return nil, status.Errorf(codes.Internal, "failed to clear throttles: %v", errs)
	}

	m.log.InfoContext(ctx, "cleared all throttles", "vm_id", vmID)
	return &api.ThrottleVMResponse{}, nil
}

// setCPUMax sets the cpu.max value for a cgroup.
// percent is the CPU limit as a percentage of one core's time.
// Values above 100 allow multiple cores (e.g., 200 = 2 cores).
// Formula: quota = percent * period / 100, period = 100000 (100ms)
func (m *ResourceManager) setCPUMax(cgroupPath string, percent uint32) error {
	const period = 100000 // 100ms in microseconds
	quota := (uint64(percent) * period) / 100
	value := fmt.Sprintf("%d %d", quota, period)
	return os.WriteFile(filepath.Join(cgroupPath, "cpu.max"), []byte(value), 0o644)
}

// clearCPUMax sets cpu.max to unlimited.
func (m *ResourceManager) clearCPUMax(cgroupPath string) error {
	return os.WriteFile(filepath.Join(cgroupPath, "cpu.max"), []byte("max 100000"), 0o644)
}

// setMemoryHighPercent sets memory.high to a percentage of allocated memory.
// This triggers aggressive memory reclaim, pushing pages to swap.
func (m *ResourceManager) setMemoryHighPercent(cgroupPath string, percent uint32, allocatedBytes uint64) error {
	highBytes := (uint64(percent) * allocatedBytes) / 100
	return os.WriteFile(filepath.Join(cgroupPath, "memory.high"), []byte(fmt.Sprintf("%d", highBytes)), 0o644)
}

// classifyVMPIDError maps getVMPID errors to appropriate gRPC status codes.
//
// Error semantics:
//   - NotFound: VM doesn't exist (socket file missing)
//   - Unavailable: VM runtime is unhealthy (connection refused, API errors)
//   - Internal: Server misconfiguration or unexpected errors
func classifyVMPIDError(err error) error {
	// Socket doesn't exist - VM was never started or has been cleaned up
	// Check both fs.ErrNotExist and syscall.ENOENT because:
	// - fs.ErrNotExist: standard Go file operations
	// - syscall.ENOENT: net.Dial("unix", path) for missing sockets
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		return status.Errorf(codes.NotFound, "VM not found: %v", err)
	}

	// Connection refused - runtime process crashed or is restarting
	if errors.Is(err, syscall.ECONNREFUSED) {
		return status.Errorf(codes.Unavailable, "VM runtime unavailable (connection refused): %v", err)
	}

	// Client couldn't connect - check if it wraps a more specific error
	if errors.Is(err, client.ErrNotConnected) {
		// ErrNotConnected is returned for both "socket not found" and "connection failed"
		// Since we already checked for ErrNotExist above, this is likely a connection issue
		return status.Errorf(codes.Unavailable, "VM runtime unavailable: %v", err)
	}

	// Default to internal error for config issues or unexpected cases
	return status.Errorf(codes.Internal, "failed to get VM info: %v", err)
}
