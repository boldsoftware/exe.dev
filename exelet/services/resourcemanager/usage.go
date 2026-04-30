package resourcemanager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"exe.dev/exelet/storage/ext4"
	"exe.dev/exelet/utils"
	"exe.dev/exelet/vmm/cloudhypervisor/client"
)

// vmCgroupPath returns the cgroup path for a VM without creating it.
func (m *ResourceManager) vmCgroupPath(id, groupID string) string {
	if groupID == "" {
		groupID = defaultGroupID
	}
	sliceName := fmt.Sprintf("%s.slice", sanitizeCgroupName(groupID))
	scopeName := fmt.Sprintf("vm-%s.scope", sanitizeCgroupName(id))
	return filepath.Join(m.cgroupRoot, cgroupSlice, sliceName, scopeName)
}

// usageData holds collected usage metrics for a VM
type usageData struct {
	cpuSeconds       float64
	memoryBytes      uint64 // cgroup memory.current (sum of charges; see types.Metric.MemoryRSSBytes)
	swapBytes        uint64
	diskVolsizeBytes uint64 // ZFS volsize (provisioned size)
	diskBytes        uint64 // ZFS used (actual compressed bytes on disk)
	diskLogicalBytes uint64 // ZFS logicalused (uncompressed)

	// Filesystem-level (ext4) view of the zvol, read directly from the
	// ext4 superblock by [readFilesystemUsage]. Zero if unavailable
	// (volume not yet formatted, not ext4, transient I/O error).
	fsTotalBytes     uint64 // ext4 capacity (block_size * blocks_count)
	fsFreeBytes      uint64 // ext4 free bytes (block_size * free_blocks_count)
	fsAvailableBytes uint64 // ext4 free bytes minus root reservation
	fsUsedBytes      uint64 // ext4 used bytes (TotalBytes - FreeBytes)
	netRxBytes       uint64
	netTxBytes       uint64
	ioReadBytes      uint64 // cumulative IO read bytes from cgroup io.stat
	ioWriteBytes     uint64 // cumulative IO write bytes from cgroup io.stat

	// Detailed memory breakdown from cgroup memory.stat.
	// For a cloud-hypervisor VM, anonBytes is the closest proxy to the VM
	// guest's actual working set (since the VM's RAM is backed by anonymous
	// memory in the host). fileBytes is host page cache (reclaimable) from the
	// VM's disk I/O. inactiveFileBytes is the easily-reclaimable subset of
	// fileBytes.
	memoryAnonBytes         uint64 // memory.stat anon
	memoryFileBytes         uint64 // memory.stat file (page cache; reclaimable)
	memoryKernelBytes       uint64 // memory.stat kernel
	memoryShmemBytes        uint64 // memory.stat shmem (tmpfs)
	memorySlabBytes         uint64 // memory.stat slab
	memoryInactiveFileBytes uint64 // memory.stat inactive_file (easily reclaimable)
}

// collectUsage collects resource usage for a VM.
func (m *ResourceManager) collectUsage(ctx context.Context, id, name, groupID string) (*usageData, error) {
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

	// Memory and swap from cgroup
	cgroupPath := m.vmCgroupPath(id, groupID)
	usage.memoryBytes, err = m.readCgroupMemory(cgroupPath)
	if err != nil {
		m.log.DebugContext(ctx, "failed to read cgroup memory", "id", id, "error", err)
	}
	usage.swapBytes, err = m.readCgroupSwap(cgroupPath)
	if err != nil {
		m.log.DebugContext(ctx, "failed to read cgroup swap", "id", id, "error", err)
	}
	if breakdown, err := m.readCgroupMemoryStat(cgroupPath); err != nil {
		m.log.DebugContext(ctx, "failed to read cgroup memory.stat", "id", id, "error", err)
	} else {
		usage.memoryAnonBytes = breakdown.anon
		usage.memoryFileBytes = breakdown.file
		usage.memoryKernelBytes = breakdown.kernel
		usage.memoryShmemBytes = breakdown.shmem
		usage.memorySlabBytes = breakdown.slab
		usage.memoryInactiveFileBytes = breakdown.inactiveFile
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

	// Filesystem-level usage from the zvol's ext4 superblock. Read-only,
	// non-blocking, safe against a live VM (see exelet/storage/ext4).
	// Gated: only collected when configured (env-wide or per group ID).
	if m.ext4UsageAllowed(groupID) {
		readFn := m.readFilesystemUsageFn
		if readFn == nil {
			readFn = m.readFilesystemUsage
		}
		if fsUsage, ok := readFn(ctx, id); ok {
			usage.fsTotalBytes = fsUsage.TotalBytes()
			usage.fsFreeBytes = fsUsage.FreeBytes()
			usage.fsAvailableBytes = fsUsage.AvailableBytes()
			usage.fsUsedBytes = fsUsage.UsedBytes()
		}
	}

	// Network usage from tap device
	usage.netRxBytes, usage.netTxBytes, err = m.readNetworkUsage(ctx, id)
	if err != nil {
		m.log.DebugContext(ctx, "failed to read network usage", "id", id, "error", err)
	}

	// IO usage from cgroup io.stat
	usage.ioReadBytes, usage.ioWriteBytes, err = readIOStat(cgroupPath)
	if err != nil {
		m.log.DebugContext(ctx, "failed to read IO usage", "id", id, "error", err)
	}

	return usage, nil
}

// getVMPID retrieves the PID for a VM from cloud-hypervisor.
func (m *ResourceManager) getVMPID(ctx context.Context, id string) (int, error) {
	runtimeURL, err := url.Parse(m.config.RuntimeAddress)
	if err != nil {
		return 0, fmt.Errorf("parse runtime address: %w", err)
	}

	if runtimeURL.Scheme != "cloudhypervisor" {
		return 0, fmt.Errorf("unsupported runtime scheme: %s", runtimeURL.Scheme)
	}

	socketPath := filepath.Join(runtimeURL.Path, id, "chh.sock")

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Use retry=false - fail fast for monitoring queries
	cl, err := client.NewCloudHypervisorClient(reqCtx, socketPath, false, m.log)
	if err != nil {
		return 0, err
	}
	defer cl.Close()

	pingResp, err := cl.GetVmmPingWithResponse(reqCtx)
	if err != nil {
		return 0, err
	}
	if pingResp.JSON200 == nil || pingResp.JSON200.Pid == nil {
		return 0, fmt.Errorf("cloud-hypervisor did not report PID")
	}
	return int(*pingResp.JSON200.Pid), nil
}

// readCgroupMemory reads memory.current from the VM's cgroup.
func (m *ResourceManager) readCgroupMemory(cgroupPath string) (uint64, error) {
	data, err := os.ReadFile(filepath.Join(cgroupPath, "memory.current"))
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

// memoryStatBreakdown holds selected fields parsed from cgroup v2 memory.stat.
type memoryStatBreakdown struct {
	anon         uint64
	file         uint64
	kernel       uint64
	shmem        uint64
	slab         uint64
	inactiveFile uint64
}

// readCgroupMemoryStat reads memory.stat and extracts the fields we care about.
// memory.stat is line-oriented: "<key> <value>\n". Unknown keys are ignored,
// and missing keys leave the corresponding field at zero.
func (m *ResourceManager) readCgroupMemoryStat(cgroupPath string) (memoryStatBreakdown, error) {
	data, err := os.ReadFile(filepath.Join(cgroupPath, "memory.stat"))
	if err != nil {
		return memoryStatBreakdown{}, err
	}
	return parseMemoryStat(data), nil
}

// parseMemoryStat parses cgroup v2 memory.stat content.
func parseMemoryStat(data []byte) memoryStatBreakdown {
	var b memoryStatBreakdown
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		val, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "anon":
			b.anon = val
		case "file":
			b.file = val
		case "kernel":
			b.kernel = val
		case "shmem":
			b.shmem = val
		case "slab":
			b.slab = val
		case "inactive_file":
			b.inactiveFile = val
		}
	}
	return b
}

// readCgroupSwap reads memory.swap.current from the VM's cgroup.
func (m *ResourceManager) readCgroupSwap(cgroupPath string) (uint64, error) {
	data, err := os.ReadFile(filepath.Join(cgroupPath, "memory.swap.current"))
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
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

// readIOStat reads cumulative IO bytes from a cgroup's io.stat file.
// It sums rbytes and wbytes across all devices.
func readIOStat(cgroupPath string) (readBytes, writeBytes uint64, err error) {
	data, err := os.ReadFile(filepath.Join(cgroupPath, "io.stat"))
	if err != nil {
		return 0, 0, err
	}
	return parseIOStat(data)
}

// parseIOStat parses cgroup v2 io.stat content, summing rbytes and wbytes across all devices.
// Format: "major:minor rbytes=N wbytes=N rios=N wios=N dbytes=N dios=N"
func parseIOStat(data []byte) (readBytes, writeBytes uint64, err error) {
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// Skip the "major:minor" prefix, parse key=value pairs
		for _, kv := range fields[1:] {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) != 2 {
				continue
			}
			val, parseErr := strconv.ParseUint(parts[1], 10, 64)
			if parseErr != nil {
				continue
			}
			switch parts[0] {
			case "rbytes":
				readBytes += val
			case "wbytes":
				writeBytes += val
			}
		}
	}
	return readBytes, writeBytes, nil
}

// zvolDevicePath returns /dev/zvol/<dataset>/<id> when the configured
// storage manager is ZFS, or "" otherwise (no storage configured, a
// non-ZFS backend, or an `id` that doesn't look like a single safe
// path segment — we don't trust callers to have validated it, since
// this is on a metrics polling path that runs against persisted state).
func (m *ResourceManager) zvolDevicePath(id string) string {
	if m.config.StorageManagerAddress == "" {
		return ""
	}
	u, err := url.Parse(m.config.StorageManagerAddress)
	if err != nil || !strings.EqualFold(u.Scheme, "zfs") {
		return ""
	}
	dataset := u.Query().Get("dataset")
	if dataset == "" {
		return ""
	}
	if !isSafePathSegment(id) {
		return ""
	}
	return filepath.Join("/dev/zvol", dataset, id)
}

// isSafePathSegment returns true if s is a single non-empty path
// component: no separators, no NUL bytes, not "." or "..".
func isSafePathSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if strings.ContainsAny(s, "/\\\x00") {
		return false
	}
	return true
}

// readFilesystemUsage reads the ext4 superblock on the instance's zvol
// and returns the resulting usage. The second return value is false on
// any failure (no zvol path resolvable, device missing, not ext4, I/O
// error). All errors are logged at debug level — this is opportunistic.
//
// The read is O_RDONLY, single-call, no locking; safe to perform while
// a guest VM has the zvol mounted read-write. We run it in a goroutine
// with a short timeout so a stalled device (e.g. mid-resilver) can't
// block the metrics poll loop, which iterates over every VM.
func (m *ResourceManager) readFilesystemUsage(ctx context.Context, id string) (ext4.Usage, bool) {
	devPath := m.zvolDevicePath(id)
	if devPath == "" {
		return ext4.Usage{}, false
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	type result struct {
		u   ext4.Usage
		err error
	}
	done := make(chan result, 1)
	go func() {
		u, err := ext4.ReadUsage(devPath)
		done <- result{u, err}
	}()

	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		m.log.DebugContext(ctx, "ext4 superblock read timed out", "id", id, "path", devPath)
		return ext4.Usage{}, false
	}
	if res.err != nil {
		// ErrNotExt4 is expected on volumes that aren't yet formatted or
		// hold something else; don't spam debug logs every poll.
		if !errors.Is(res.err, ext4.ErrNotExt4) {
			m.log.DebugContext(ctx, "failed to read ext4 superblock", "id", id, "path", devPath, "error", res.err)
		}
		return ext4.Usage{}, false
	}
	return res.u, true
}
