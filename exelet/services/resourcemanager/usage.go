package resourcemanager

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"exe.dev/exelet/utils"
	"exe.dev/exelet/vmm/cloudhypervisor/client"
)

// usageData holds collected usage metrics for a VM
type usageData struct {
	cpuSeconds       float64
	memoryBytes      uint64
	swapBytes        uint64
	diskVolsizeBytes uint64 // ZFS volsize (provisioned size)
	diskBytes        uint64 // ZFS used (actual compressed bytes on disk)
	diskLogicalBytes uint64 // ZFS logicalused (uncompressed)
	netRxBytes       uint64
	netTxBytes       uint64
}

// collectUsage collects resource usage for a VM.
func (m *ResourceManager) collectUsage(ctx context.Context, id, name string) (*usageData, error) {
	usage := &usageData{}

	// Get VM info from cloud-hypervisor (includes PID and memory)
	pid, memoryBytes, err := m.getVMInfo(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get VM info: %w", err)
	}

	// CPU usage from /proc/<pid>/stat
	usage.cpuSeconds, err = m.readCPUUsage(pid)
	if err != nil {
		return nil, fmt.Errorf("read CPU usage: %w", err)
	}

	// Memory from cloud-hypervisor API (actual guest memory)
	usage.memoryBytes = memoryBytes

	// Swap usage from /proc/<pid>/status (VmSwap line)
	usage.swapBytes, err = m.readSwapUsage(pid)
	if err != nil {
		m.log.DebugContext(ctx, "failed to read swap usage", "id", id, "error", err)
	}

	// Disk info from ZFS (volsize, used, and logicalused)
	zfsInfo, err := m.readZFSVolumeInfo(ctx, id)
	if err != nil {
		m.log.DebugContext(ctx, "failed to read ZFS volume info", "id", id, "error", err)
	} else if zfsInfo != nil {
		usage.diskVolsizeBytes = zfsInfo.Volsize
		usage.diskBytes = zfsInfo.Used
		usage.diskLogicalBytes = zfsInfo.LogicalUsed
	}

	// Network usage from tap device
	usage.netRxBytes, usage.netTxBytes, err = m.readNetworkUsage(ctx, id)
	if err != nil {
		m.log.DebugContext(ctx, "failed to read network usage", "id", id, "error", err)
	}

	return usage, nil
}

// getVMInfo retrieves VM info from cloud-hypervisor including PID and actual memory usage.
func (m *ResourceManager) getVMInfo(ctx context.Context, id string) (pid int, memoryBytes uint64, err error) {
	runtimeURL, err := url.Parse(m.config.RuntimeAddress)
	if err != nil {
		return 0, 0, fmt.Errorf("parse runtime address: %w", err)
	}

	if runtimeURL.Scheme != "cloudhypervisor" {
		return 0, 0, fmt.Errorf("unsupported runtime scheme: %s", runtimeURL.Scheme)
	}

	socketPath := filepath.Join(runtimeURL.Path, id, "chh.sock")

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Use retry=false - fail fast for monitoring queries
	cl, err := client.NewCloudHypervisorClient(reqCtx, socketPath, false, m.log)
	if err != nil {
		return 0, 0, err
	}
	defer cl.Close()

	// Get PID from vmm.ping
	pingResp, err := cl.GetVmmPingWithResponse(reqCtx)
	if err != nil {
		return 0, 0, err
	}
	if pingResp.JSON200 == nil || pingResp.JSON200.Pid == nil {
		return 0, 0, fmt.Errorf("cloud-hypervisor did not report PID")
	}
	pid = int(*pingResp.JSON200.Pid)

	// Get actual memory from vm.info
	infoResp, err := cl.GetVmInfoWithResponse(reqCtx)
	if err != nil {
		return 0, 0, fmt.Errorf("get vm info: %w", err)
	}
	if infoResp.JSON200 != nil && infoResp.JSON200.MemoryActualSize != nil {
		memoryBytes = uint64(*infoResp.JSON200.MemoryActualSize)
	}

	return pid, memoryBytes, nil
}

// getVMPID retrieves just the PID for a VM (used by priority management).
func (m *ResourceManager) getVMPID(ctx context.Context, id string) (int, error) {
	pid, _, err := m.getVMInfo(ctx, id)
	return pid, err
}

// readSwapUsage reads swap usage from /proc/<pid>/status (VmSwap line).
func (m *ResourceManager) readSwapUsage(pid int) (uint64, error) {
	statusPath := fmt.Sprintf("/proc/%d/status", pid)
	data, err := os.ReadFile(statusPath)
	if err != nil {
		return 0, err
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmSwap:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, fmt.Errorf("malformed VmSwap line: %q", line)
			}
			// Value is in kB
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse VmSwap: %w", err)
			}
			return kb * 1024, nil // Convert to bytes
		}
	}

	// VmSwap line not found - could be a kernel thread or similar
	return 0, nil
}

// readCPUUsage reads total CPU seconds from /proc/<pid>/stat.
func (m *ResourceManager) readCPUUsage(pid int) (float64, error) {
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(statPath)
	if err != nil {
		return 0, err
	}

	// Parse stat file - fields after closing paren
	closing := bytes.LastIndexByte(data, ')')
	if closing == -1 {
		return 0, fmt.Errorf("malformed stat data: missing ')'")
	}

	fields := strings.Fields(strings.TrimSpace(string(data[closing+1:])))
	if len(fields) < 14 {
		return 0, fmt.Errorf("malformed stat data: insufficient fields")
	}

	// Fields 13 and 14 (0-indexed 11 and 12 after comm) are utime and stime
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse utime: %w", err)
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse stime: %w", err)
	}

	// Convert ticks to seconds (assuming 100 Hz)
	const clockTicks = 100.0
	return float64(utime+stime) / clockTicks, nil
}

// zfsVolumeInfo contains ZFS volume properties.
type zfsVolumeInfo struct {
	Volsize     uint64 // Provisioned size of the volume
	Used        uint64 // Actual compressed bytes on disk
	LogicalUsed uint64 // Uncompressed logical bytes
}

// readZFSVolumeInfo reads volume properties from ZFS.
// Returns volsize, used, and logicalused for the volume.
func (m *ResourceManager) readZFSVolumeInfo(ctx context.Context, id string) (*zfsVolumeInfo, error) {
	if m.config.StorageManagerAddress == "" {
		return nil, nil
	}

	storageURL, err := url.Parse(m.config.StorageManagerAddress)
	if err != nil {
		return nil, err
	}

	if storageURL.Scheme != "zfs" {
		return nil, nil
	}

	dataset := storageURL.Query().Get("dataset")
	if dataset == "" {
		return nil, nil
	}

	dsName := filepath.Join(dataset, id)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Get volsize, used, and logicalused in a single zfs command
	cmd := exec.CommandContext(ctx, "zfs", "get", "-Hp", "-o", "value", "volsize,used,logicalused", dsName)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 3 {
		return nil, fmt.Errorf("unexpected zfs output: %q", string(output))
	}

	info := &zfsVolumeInfo{}

	info.Volsize, err = strconv.ParseUint(strings.TrimSpace(lines[0]), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse volsize: %w", err)
	}

	info.Used, err = strconv.ParseUint(strings.TrimSpace(lines[1]), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse used: %w", err)
	}

	info.LogicalUsed, err = strconv.ParseUint(strings.TrimSpace(lines[2]), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse logicalused: %w", err)
	}

	return info, nil
}

// readDiskUsage reads disk usage from ZFS.
// Returns (used, logicalused, error) where used is compressed bytes on disk
// and logicalused is uncompressed logical bytes.
func (m *ResourceManager) readDiskUsage(ctx context.Context, id string) (used, logicalUsed uint64, err error) {
	info, err := m.readZFSVolumeInfo(ctx, id)
	if err != nil {
		return 0, 0, err
	}
	if info == nil {
		return 0, 0, nil
	}
	return info.Used, info.LogicalUsed, nil
}

// readNetworkUsage reads network RX/TX bytes from the tap device.
func (m *ResourceManager) readNetworkUsage(ctx context.Context, id string) (rxBytes, txBytes uint64, err error) {
	tapName := utils.GetTapName(id)

	// Note: tap TX = VM RX and tap RX = VM TX (perspective inversion)
	tapRxBytes, err := m.readNetStat(tapName, "rx_bytes")
	if err != nil {
		return 0, 0, fmt.Errorf("read tap rx_bytes: %w", err)
	}

	tapTxBytes, err := m.readNetStat(tapName, "tx_bytes")
	if err != nil {
		return 0, 0, fmt.Errorf("read tap tx_bytes: %w", err)
	}

	// Invert perspective: tap RX = VM TX, tap TX = VM RX
	return tapTxBytes, tapRxBytes, nil
}

func (m *ResourceManager) readNetStat(ifaceName, stat string) (uint64, error) {
	statPath := filepath.Join("/sys/class/net", ifaceName, "statistics", stat)
	data, err := os.ReadFile(statPath)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}
