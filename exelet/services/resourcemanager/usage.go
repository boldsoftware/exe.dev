package resourcemanager

import (
	"bufio"
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
	cpuSeconds  float64
	memoryBytes uint64
	diskBytes   uint64
	netRxBytes  uint64
	netTxBytes  uint64
}

// collectUsage collects resource usage for a VM.
func (m *ResourceManager) collectUsage(ctx context.Context, id, name string) (*usageData, error) {
	usage := &usageData{}

	// Get VM PID from cloud-hypervisor
	pid, err := m.getVMPID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get VM PID: %w", err)
	}

	// CPU usage from /proc/<pid>/stat
	usage.cpuSeconds, err = m.readCPUUsage(pid)
	if err != nil {
		return nil, fmt.Errorf("read CPU usage: %w", err)
	}

	// Memory usage from /proc/<pid>/status
	usage.memoryBytes, err = m.readMemoryUsage(pid)
	if err != nil {
		// Non-fatal, continue with other metrics
		m.log.DebugContext(ctx, "failed to read memory usage", "id", id, "error", err)
	}

	// Disk usage from ZFS
	usage.diskBytes, err = m.readDiskUsage(ctx, id)
	if err != nil {
		m.log.DebugContext(ctx, "failed to read disk usage", "id", id, "error", err)
	}

	// Network usage from tap device
	usage.netRxBytes, usage.netTxBytes, err = m.readNetworkUsage(ctx, id)
	if err != nil {
		m.log.DebugContext(ctx, "failed to read network usage", "id", id, "error", err)
	}

	return usage, nil
}

// getVMPID retrieves the cloud-hypervisor process PID for a VM.
func (m *ResourceManager) getVMPID(ctx context.Context, id string) (int, error) {
	runtimeURL, err := url.Parse(m.config.RuntimeAddress)
	if err != nil {
		return 0, fmt.Errorf("parse runtime address: %w", err)
	}

	if runtimeURL.Scheme != "cloudhypervisor" {
		return 0, fmt.Errorf("unsupported runtime scheme: %s", runtimeURL.Scheme)
	}

	socketPath := filepath.Join(runtimeURL.Path, id, "chh.sock")
	cl, err := client.NewCloudHypervisorClient(socketPath, m.log)
	if err != nil {
		return 0, err
	}
	defer cl.Close()

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := cl.GetVmmPingWithResponse(reqCtx)
	if err != nil {
		return 0, err
	}
	if resp.JSON200 == nil || resp.JSON200.Pid == nil {
		return 0, fmt.Errorf("cloud-hypervisor did not report PID")
	}

	return int(*resp.JSON200.Pid), nil
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

// readMemoryUsage reads memory usage from /proc/<pid>/status.
func (m *ResourceManager) readMemoryUsage(pid int) (uint64, error) {
	statusPath := fmt.Sprintf("/proc/%d/status", pid)
	f, err := os.Open(statusPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// VmRSS is the resident set size (actual memory in use)
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, fmt.Errorf("malformed VmRSS line")
			}
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse VmRSS: %w", err)
			}
			return kb * 1024, nil
		}
	}

	return 0, fmt.Errorf("VmRSS not found")
}

// readDiskUsage reads disk usage from ZFS.
func (m *ResourceManager) readDiskUsage(ctx context.Context, id string) (uint64, error) {
	if m.config.StorageManagerAddress == "" {
		return 0, nil
	}

	storageURL, err := url.Parse(m.config.StorageManagerAddress)
	if err != nil {
		return 0, err
	}

	if storageURL.Scheme != "zfs" {
		return 0, nil
	}

	dataset := storageURL.Query().Get("dataset")
	if dataset == "" {
		return 0, nil
	}

	dsName := filepath.Join(dataset, id)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "zfs", "get", "-Hp", "-o", "value", "used", dsName)
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	return strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
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
