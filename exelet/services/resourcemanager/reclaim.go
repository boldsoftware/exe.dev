package resourcemanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	api "exe.dev/pkg/api/exe/resource/v1"
)

const (
	// reclaimWriteTimeout is the maximum time to wait for a single
	// memory.reclaim write to complete. The write is a synchronous kernel
	// call that blocks while the kernel scans and reclaims pages.
	reclaimWriteTimeout = 2 * time.Second

	// reclaimSettleTimeout is the time to wait after all reclaim writes
	// for MemAvailable to update.
	reclaimSettleTimeout = 3 * time.Second

	// reclaimMinBytes is the minimum cgroup resident memory worth
	// reclaiming. Cgroups below this threshold are skipped because the
	// kernel reclaim scan overhead exceeds the benefit.
	reclaimMinBytes = 50 * 1024 * 1024 // 50 MB
)

// reclaimTarget holds the info needed to reclaim memory from a single VM.
type reclaimTarget struct {
	id          string
	priority    api.VMPriority
	memoryBytes uint64 // current resident memory from cgroup
	cgroupPath  string
	stale       bool // true if memoryBytes is from poll cache, not a fresh read
}

// ReclaimMemory proactively reclaims physical memory by writing to cgroup v2
// memory.reclaim files, pushing VM pages to swap.
//
// Cloud-hypervisor processes fault in guest RAM before the resource manager
// moves them into exelet cgroups, so the guest memory pages are typically
// charged to the root cgroup rather than individual VM scopes. For this
// reason, the root cgroup reclaim (phase 2) is usually where the actual
// memory gets freed.
func (m *ResourceManager) ReclaimMemory(ctx context.Context, bytes uint64) error {
	targets := m.reclaimTargets()

	// Sort: LOW priority first, then by memory usage descending (biggest first).
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].priority != targets[j].priority {
			return targets[i].priority > targets[j].priority // LOW (1) before NORMAL (0)
		}
		return targets[i].memoryBytes > targets[j].memoryBytes
	})

	m.log.InfoContext(ctx, "reclaiming memory for live migration",
		"need_bytes", bytes,
		"available_bytes", m.readMemAvailable(),
		"vm_count", len(targets))

	// Phase 1: Reclaim from individual VM cgroups that have meaningful
	// resident memory. Small cgroups are skipped — the kernel reclaim scan
	// blocks for seconds even on tiny cgroups with nothing to reclaim.
	for _, t := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if avail := m.readMemAvailable(); avail >= bytes {
			m.log.InfoContext(ctx, "memory reclaim complete",
				"available_bytes", avail,
				"need_bytes", bytes,
				"phase", "vm-cgroups")
			return nil
		}
		if !t.stale && t.memoryBytes < reclaimMinBytes {
			m.log.DebugContext(ctx, "skipping VM cgroup — too small to reclaim",
				"vm", t.id,
				"resident_bytes", t.memoryBytes,
				"min_bytes", reclaimMinBytes)
			continue
		}
		m.log.InfoContext(ctx, "reclaiming memory from VM cgroup",
			"vm", t.id,
			"priority", t.priority.String(),
			"resident_bytes", t.memoryBytes,
			"cgroup", t.cgroupPath)
		m.writeMemoryReclaim(ctx, filepath.Join(t.cgroupPath, "memory.reclaim"), t.memoryBytes)
	}

	// Phase 2: Reclaim from the root cgroup. This is where the bulk of
	// VM guest RAM lives because VMM processes fault in pages before being
	// moved into exelet cgroups. Requires kernel 6.1+.
	if avail := m.readMemAvailable(); avail < bytes {
		rootPath := filepath.Join(m.cgroupRoot, "memory.reclaim")
		deficit := bytes - avail
		m.log.InfoContext(ctx, "reclaiming memory from root cgroup",
			"available_bytes", avail,
			"need_bytes", bytes,
			"deficit_bytes", deficit,
			"path", rootPath)
		m.writeMemoryReclaim(ctx, rootPath, deficit)
	}

	// Give the kernel a moment to finish writeback and update MemAvailable.
	deadline := time.Now().Add(reclaimSettleTimeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		avail := m.readMemAvailable()
		if avail >= bytes {
			m.log.InfoContext(ctx, "memory reclaim complete after settling",
				"available_bytes", avail,
				"need_bytes", bytes)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	avail := m.readMemAvailable()
	return fmt.Errorf("reclaimed what we could but only %d MB available, need %d MB",
		avail/(1024*1024), bytes/(1024*1024))
}

// writeMemoryReclaim writes to a cgroup v2 memory.reclaim file.
//
// The write is a synchronous kernel call that blocks while the kernel scans
// and reclaims pages — it can hang if the kernel struggles to find reclaimable
// pages. We run it in a goroutine with a timeout to avoid blocking the caller.
//
// Per-path deduplication (reclaimInflight) ensures at most one kernel write
// per cgroup path at a time. If a previous write to the same path is still
// blocked in the kernel, the new write is skipped — the kernel is already
// doing the work. The inflight marker is cleared on timeout so future
// attempts can retry the path.
func (m *ResourceManager) writeMemoryReclaim(ctx context.Context, path string, bytes uint64) {
	// Skip if a previous write to this path is still in-flight.
	if _, loaded := m.reclaimInflight.LoadOrStore(path, struct{}{}); loaded {
		m.log.InfoContext(ctx, "skipping memory.reclaim write — previous write to this path still in-flight",
			"path", path,
			"reclaim_bytes", bytes)
		return
	}

	done := make(chan error, 1)
	go func() {
		// Open write-only without create — memory.reclaim is a kernel
		// interface file that must already exist. Using O_WRONLY avoids
		// silently creating a regular file on misconfigured cgroupRoot
		// or kernels that don't expose memory.reclaim (<6.1 for root).
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			done <- err
			return
		}
		_, err = f.Write([]byte(strconv.FormatUint(bytes, 10)))
		f.Close()
		done <- err
	}()

	timeout := time.NewTimer(reclaimWriteTimeout)
	defer timeout.Stop()

	select {
	case err := <-done:
		m.reclaimInflight.Delete(path)
		if err != nil {
			m.log.WarnContext(ctx, "memory.reclaim write failed",
				"error", err,
				"path", path,
				"reclaim_bytes", bytes)
		} else {
			m.log.InfoContext(ctx, "memory.reclaim write succeeded",
				"path", path,
				"reclaim_bytes", bytes,
				"available_after", m.readMemAvailable())
		}
	case <-timeout.C:
		// Clear inflight marker so future attempts can retry this path.
		// The goroutine may still be blocked in the kernel, but that's
		// a single stuck syscall — allowing retries is more important
		// than preventing a duplicate write to the same cgroup.
		m.reclaimInflight.Delete(path)
		m.log.WarnContext(ctx, "memory.reclaim write timed out — kernel is still reclaiming in the background",
			"path", path,
			"reclaim_bytes", bytes,
			"timeout", reclaimWriteTimeout,
			"available_now", m.readMemAvailable())
	case <-ctx.Done():
		m.reclaimInflight.Delete(path)
		m.log.WarnContext(ctx, "memory.reclaim write cancelled",
			"path", path,
			"reclaim_bytes", bytes)
	}
}

// reclaimTargets collects all tracked VMs with their cgroup paths and
// fresh memory.current values read directly from cgroup files.
func (m *ResourceManager) reclaimTargets() []reclaimTarget {
	// Snapshot state under lock — no filesystem IO while holding usageMu.
	m.usageMu.Lock()
	type snapshot struct {
		id          string
		groupID     string
		priority    api.VMPriority
		memoryBytes uint64 // fallback if fresh read fails
	}
	snaps := make([]snapshot, 0, len(m.usageState))
	for id, state := range m.usageState {
		snaps = append(snaps, snapshot{
			id:          id,
			groupID:     state.groupID,
			priority:    state.priority,
			memoryBytes: state.memoryBytes,
		})
	}
	m.usageMu.Unlock()

	// Read fresh memory.current outside the lock.
	targets := make([]reclaimTarget, 0, len(snaps))
	for _, s := range snaps {
		cgroupPath := m.vmCgroupPath(s.id, s.groupID)

		var stale bool
		memBytes, err := m.readCgroupMemory(cgroupPath)
		if err != nil {
			m.log.Debug("cgroup memory read failed, using stale value",
				"vm", s.id,
				"error", err,
				"stale_bytes", s.memoryBytes)
			memBytes = s.memoryBytes
			stale = true
		}

		targets = append(targets, reclaimTarget{
			id:          s.id,
			priority:    s.priority,
			memoryBytes: memBytes,
			cgroupPath:  cgroupPath,
			stale:       stale,
		})
	}
	return targets
}

// readMemAvailable returns available memory in bytes. Tests can override
// readMemAvailableFn to simulate low-memory conditions.
func (m *ResourceManager) readMemAvailable() uint64 {
	if m.readMemAvailableFn != nil {
		return m.readMemAvailableFn()
	}
	return readMemAvailableFromProc()
}

// readMemAvailableFromProc reads MemAvailable from /proc/meminfo and returns it in bytes.
// Returns 0 if the value cannot be read.
func readMemAvailableFromProc() uint64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			var kb uint64
			fmt.Sscanf(line, "MemAvailable: %d kB", &kb)
			return kb * 1024
		}
	}
	return 0
}
