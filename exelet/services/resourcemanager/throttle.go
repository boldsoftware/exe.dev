package resourcemanager

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"exe.dev/exelet/storage"
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
	if req.Clear && (req.CpuPercent != nil || req.MemoryPercent != nil || req.IoReadBps != nil || req.IoWriteBps != nil) {
		return nil, status.Error(codes.InvalidArgument, "clear cannot be combined with throttle options")
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
	if !req.Clear && req.CpuPercent == nil && req.MemoryPercent == nil && req.IoReadBps == nil && req.IoWriteBps == nil {
		return nil, status.Error(codes.InvalidArgument, "at least one of clear, cpu_percent, memory_percent, io_read_bps, or io_write_bps is required")
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

	// IO throttling
	if req.IoReadBps != nil || req.IoWriteBps != nil {
		if m.context == nil || m.context.StorageManager == nil {
			return nil, status.Error(codes.FailedPrecondition, "storage manager not available for IO throttling")
		}

		sm := storage.ResolveForID(ctx, m.context.StorageManager, req.VmID)
		fs, err := sm.Get(ctx, req.VmID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get filesystem for IO throttle: %v", err)
		}

		majMin, err := getDeviceMajorMinor(fs.Path)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get device info: %v", err)
		}

		readBPS := uint64(0)
		if req.IoReadBps != nil {
			readBPS = *req.IoReadBps
		}
		writeBPS := uint64(0)
		if req.IoWriteBps != nil {
			writeBPS = *req.IoWriteBps
		}

		if err := setIOMax(cgroupPath, majMin, readBPS, writeBPS); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to set IO throttle: %v", err)
		}
		m.log.InfoContext(ctx, "applied IO throttle", "vm_id", req.VmID, "read_bps", readBPS, "write_bps", writeBPS)
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

	// Clear IO throttle if storage manager is available
	if m.context != nil && m.context.StorageManager != nil {
		sm := storage.ResolveForID(ctx, m.context.StorageManager, vmID)
		fs, err := sm.Get(ctx, vmID)
		if err != nil {
			m.log.WarnContext(ctx, "failed to get filesystem for IO throttle clear", "vm_id", vmID, "error", err)
			errs = append(errs, fmt.Sprintf("io (get filesystem): %v", err))
		} else if majMin, err := getDeviceMajorMinor(fs.Path); err != nil {
			m.log.WarnContext(ctx, "failed to get device info for IO throttle clear", "vm_id", vmID, "path", fs.Path, "error", err)
			errs = append(errs, fmt.Sprintf("io (get device): %v", err))
		} else if err := clearIOMax(cgroupPath, majMin); err != nil {
			m.log.WarnContext(ctx, "failed to clear IO throttle", "vm_id", vmID, "error", err)
			errs = append(errs, fmt.Sprintf("io: %v", err))
		}
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

// getDeviceMajorMinor returns "MAJ:MIN" for a device path.
// If the path is a symlink (like /dev/zvol/...), it resolves to the real device.
func getDeviceMajorMinor(devicePath string) (string, error) {
	// Resolve symlinks to get the real device path
	// /dev/zvol/tank/vm-id -> /dev/zd0 (or similar)
	realPath, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		return "", fmt.Errorf("resolve symlink %s: %w", devicePath, err)
	}

	var stat unix.Stat_t
	if err := unix.Stat(realPath, &stat); err != nil {
		return "", fmt.Errorf("stat device %s: %w", realPath, err)
	}
	major := unix.Major(uint64(stat.Rdev))
	minor := unix.Minor(uint64(stat.Rdev))
	return strconv.FormatUint(uint64(major), 10) + ":" + strconv.FormatUint(uint64(minor), 10), nil
}

// setIOMax sets io.max bandwidth limits for a device.
// readBPS/writeBPS of 0 means unlimited (use "max").
// Preserves existing limits for other devices and other keys (riops/wiops) for this device.
func setIOMax(cgroupPath, deviceMajMin string, readBPS, writeBPS uint64) error {
	ioMaxFile := filepath.Join(cgroupPath, "io.max")

	readLimit := "max"
	if readBPS > 0 {
		readLimit = strconv.FormatUint(readBPS, 10)
	}
	writeLimit := "max"
	if writeBPS > 0 {
		writeLimit = strconv.FormatUint(writeBPS, 10)
	}

	updates := map[string]string{
		"rbps": readLimit,
		"wbps": writeLimit,
	}
	return updateIOMaxLine(ioMaxFile, deviceMajMin, updates)
}

// clearIOMax removes io.max bandwidth limits for a device by setting them to max.
// Preserves existing limits for other devices and other keys (riops/wiops) for this device.
func clearIOMax(cgroupPath, deviceMajMin string) error {
	ioMaxFile := filepath.Join(cgroupPath, "io.max")
	updates := map[string]string{
		"rbps": "max",
		"wbps": "max",
	}
	return updateIOMaxLine(ioMaxFile, deviceMajMin, updates)
}

// updateIOMaxLine updates specific keys for a device in io.max,
// preserving lines for other devices and other keys for this device.
func updateIOMaxLine(ioMaxFile, deviceMajMin string, updates map[string]string) error {
	// Read existing content
	existing, err := os.ReadFile(ioMaxFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read io.max: %w", err)
	}

	// Parse existing lines
	var lines []string
	found := false
	prefix := deviceMajMin + " "

	for _, line := range strings.Split(string(existing), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, prefix) {
			// Parse existing key=value pairs and merge with updates
			merged := mergeIOMaxKeys(line, deviceMajMin, updates)
			lines = append(lines, merged)
			found = true
		} else {
			// Preserve lines for other devices
			lines = append(lines, line)
		}
	}

	if !found {
		// No existing line for this device, create new one
		lines = append(lines, buildIOMaxLine(deviceMajMin, updates))
	}

	content := strings.Join(lines, "\n")
	return os.WriteFile(ioMaxFile, []byte(content), 0o644)
}

// mergeIOMaxKeys parses an existing io.max line and merges in the updates,
// preserving any keys not being updated.
func mergeIOMaxKeys(existingLine, deviceMajMin string, updates map[string]string) string {
	// Parse existing line: "MAJ:MIN key1=val1 key2=val2 ..."
	parts := strings.Fields(existingLine)
	if len(parts) < 1 {
		return buildIOMaxLine(deviceMajMin, updates)
	}

	// Extract existing key=value pairs (skip the MAJ:MIN prefix)
	existing := make(map[string]string)
	for _, part := range parts[1:] {
		if kv := strings.SplitN(part, "=", 2); len(kv) == 2 {
			existing[kv[0]] = kv[1]
		}
	}

	// Merge updates into existing
	for k, v := range updates {
		existing[k] = v
	}

	return buildIOMaxLine(deviceMajMin, existing)
}

// buildIOMaxLine constructs an io.max line from device and key=value pairs.
func buildIOMaxLine(deviceMajMin string, kvs map[string]string) string {
	// Build line with consistent key ordering for predictable output
	var parts []string
	parts = append(parts, deviceMajMin)

	// Standard key order
	for _, key := range []string{"rbps", "wbps", "riops", "wiops"} {
		if val, ok := kvs[key]; ok {
			parts = append(parts, key+"="+val)
		}
	}
	// Any other keys (future-proofing)
	for key, val := range kvs {
		if key != "rbps" && key != "wbps" && key != "riops" && key != "wiops" {
			parts = append(parts, key+"="+val)
		}
	}

	return strings.Join(parts, " ")
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
