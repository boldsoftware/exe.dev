package resourcemanager

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	api "exe.dev/pkg/api/exe/resource/v1"
)

// waitForFile polls until path exists with non-empty content or deadline passes.
func waitForFile(t *testing.T, path string, deadline time.Duration) []byte {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return data
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %s not written within %v", path, deadline)
	return nil
}

func TestReclaimTargetsSortOrder(t *testing.T) {
	targets := []reclaimTarget{
		{id: "normal-small", priority: api.VMPriority_PRIORITY_NORMAL, memoryBytes: 1 << 30},   // 1 GB
		{id: "low-small", priority: api.VMPriority_PRIORITY_LOW, memoryBytes: 512 << 20},       // 512 MB
		{id: "normal-large", priority: api.VMPriority_PRIORITY_NORMAL, memoryBytes: 8 << 30},    // 8 GB
		{id: "low-large", priority: api.VMPriority_PRIORITY_LOW, memoryBytes: 4 << 30},          // 4 GB
		{id: "normal-medium", priority: api.VMPriority_PRIORITY_NORMAL, memoryBytes: 4 << 30},   // 4 GB
	}

	sort.Slice(targets, func(i, j int) bool {
		if targets[i].priority != targets[j].priority {
			return targets[i].priority > targets[j].priority // LOW (1) before NORMAL (0)
		}
		return targets[i].memoryBytes > targets[j].memoryBytes
	})

	// Expected order: LOW priority first (sorted by memory desc), then NORMAL (sorted by memory desc)
	expectedOrder := []string{"low-large", "low-small", "normal-large", "normal-medium", "normal-small"}
	for i, target := range targets {
		if target.id != expectedOrder[i] {
			t.Errorf("position %d: got %s, want %s", i, target.id, expectedOrder[i])
		}
	}
}

func TestReclaimTargetsCollectsFromUsageState(t *testing.T) {
	tmpDir := t.TempDir()
	m := &ResourceManager{
		usageState: map[string]*vmUsageState{
			"vm1": {groupID: "acct_1", priority: api.VMPriority_PRIORITY_LOW, memoryBytes: 2 << 30},
			"vm2": {groupID: "acct_1", priority: api.VMPriority_PRIORITY_NORMAL, memoryBytes: 4 << 30},
			"vm3": {groupID: "acct_2", priority: api.VMPriority_PRIORITY_LOW, memoryBytes: 1 << 30},
		},
		cgroupRoot: tmpDir,
		log:        slog.Default(),
	}

	// Create fake cgroup dirs with memory.current so fresh reads work
	for id, state := range m.usageState {
		cgroupPath := m.vmCgroupPath(id, state.groupID)
		os.MkdirAll(cgroupPath, 0o755)
		os.WriteFile(filepath.Join(cgroupPath, "memory.current"),
			[]byte(strconv.FormatUint(state.memoryBytes, 10)), 0o644)
	}

	targets := m.reclaimTargets()
	if len(targets) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(targets))
	}

	// Verify all VMs are present with correct memory values
	byID := make(map[string]reclaimTarget)
	for _, target := range targets {
		byID[target.id] = target
		if !strings.Contains(target.cgroupPath, target.id) {
			t.Errorf("target %s: cgroup path %q doesn't contain VM ID", target.id, target.cgroupPath)
		}
	}

	for _, id := range []string{"vm1", "vm2", "vm3"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("missing target %s", id)
		}
	}

	// Verify fresh memory values were read
	if byID["vm1"].memoryBytes != 2<<30 {
		t.Errorf("vm1 memoryBytes = %d, want %d", byID["vm1"].memoryBytes, 2<<30)
	}
	if byID["vm2"].memoryBytes != 4<<30 {
		t.Errorf("vm2 memoryBytes = %d, want %d", byID["vm2"].memoryBytes, 4<<30)
	}
}

func TestReclaimMemoryWritesReclaimFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create fake cgroup directories with memory.reclaim files.
	// The files must pre-exist because writeMemoryReclaim opens O_WRONLY
	// without O_CREATE — matching real cgroup v2 interface files.
	vm1Path := filepath.Join(tmpDir, "exelet.slice", "acct_1.slice", "vm-vm1.scope")
	vm2Path := filepath.Join(tmpDir, "exelet.slice", "acct_1.slice", "vm-vm2.scope")
	if err := os.MkdirAll(vm1Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vm2Path, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(vm1Path, "memory.reclaim"),
		filepath.Join(vm2Path, "memory.reclaim"),
		filepath.Join(tmpDir, "memory.reclaim"),
	} {
		os.WriteFile(p, nil, 0o644)
	}

	m := &ResourceManager{
		usageState: map[string]*vmUsageState{
			"vm1": {groupID: "acct_1", priority: api.VMPriority_PRIORITY_LOW, memoryBytes: 2 << 30},
			"vm2": {groupID: "acct_1", priority: api.VMPriority_PRIORITY_NORMAL, memoryBytes: 4 << 30},
		},
		cgroupRoot:         tmpDir,
		log:                slog.Default(),
		readMemAvailableFn: func() uint64 { return 0 },
	}

	// readMemAvailableFn always returns 0 so reclaim never short-circuits.
	// Use a short context so the settling loop exits quickly.
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	_ = m.ReclaimMemory(ctx, 1<<30)

	// Poll for memory.reclaim files — the writes happen in goroutines that
	// may outlive the ReclaimMemory call when the context times out.
	for _, vmPath := range []string{vm1Path, vm2Path} {
		waitForFile(t, filepath.Join(vmPath, "memory.reclaim"), 2*time.Second)
	}
	waitForFile(t, filepath.Join(tmpDir, "memory.reclaim"), 2*time.Second)
}

func TestReclaimMemorySkipsZeroMemoryVMs(t *testing.T) {
	tmpDir := t.TempDir()

	vmPath := filepath.Join(tmpDir, "exelet.slice", "default.slice", "vm-vm1.scope")
	if err := os.MkdirAll(vmPath, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a fresh memory.current of 0 so the cgroup read succeeds
	// and the skip threshold applies (stale reads bypass the threshold).
	os.WriteFile(filepath.Join(vmPath, "memory.current"), []byte("0"), 0o644)
	// Pre-create root memory.reclaim (but NOT the VM's — it should be skipped).
	os.WriteFile(filepath.Join(tmpDir, "memory.reclaim"), nil, 0o644)

	m := &ResourceManager{
		usageState: map[string]*vmUsageState{
			"vm1": {priority: api.VMPriority_PRIORITY_LOW, memoryBytes: 0},
		},
		cgroupRoot:         tmpDir,
		log:                slog.Default(),
		readMemAvailableFn: func() uint64 { return 0 },
	}

	ctx2, cancel2 := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel2()
	_ = m.ReclaimMemory(ctx2, 1<<30)

	// Wait for root cgroup write to complete — phase 2 runs after phase 1
	// skips are decided, so if root is written, the VM skip already happened.
	waitForFile(t, filepath.Join(tmpDir, "memory.reclaim"), 2*time.Second)

	// memory.reclaim should NOT be written for a VM with 0 memory
	if _, err := os.Stat(filepath.Join(vmPath, "memory.reclaim")); err == nil {
		t.Error("memory.reclaim should not be written for VM with 0 memoryBytes")
	}
}

func TestReclaimMemoryNoTargetsStillReclaimsRoot(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "memory.reclaim"), nil, 0o644)
	m := &ResourceManager{
		usageState:         map[string]*vmUsageState{},
		cgroupRoot:         tmpDir,
		log:                slog.Default(),
		readMemAvailableFn: func() uint64 { return 0 },
	}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	_ = m.ReclaimMemory(ctx, 1<<30)

	// Root cgroup reclaim should still be attempted even with no VM targets.
	waitForFile(t, filepath.Join(tmpDir, "memory.reclaim"), 2*time.Second)
}
