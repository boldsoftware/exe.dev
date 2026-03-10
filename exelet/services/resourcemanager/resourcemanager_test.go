package resourcemanager

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"exe.dev/exelet/config"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
	api "exe.dev/pkg/api/exe/resource/v1"
)

func TestCapacityDetectMemory(t *testing.T) {
	tmpDir := t.TempDir()
	meminfoPath := filepath.Join(tmpDir, "meminfo")

	// Write test meminfo
	content := `MemTotal:       16384000 kB
MemFree:         1234567 kB
MemAvailable:    8000000 kB
SwapTotal:       2097148 kB
SwapFree:        2097148 kB
Buffers:          123456 kB
Cached:          4000000 kB
`
	if err := os.WriteFile(meminfoPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write meminfo: %v", err)
	}

	c := &Capacity{
		procMeminfo: meminfoPath,
		log:         slog.Default(),
	}

	memBytes, err := c.detectMemory(t.Context())
	if err != nil {
		t.Fatalf("detectMemory failed: %v", err)
	}

	// 16384000 kB * 1024 = 16777216000 bytes
	expected := uint64(16384000 * 1024)
	if memBytes != expected {
		t.Errorf("detectMemory() = %d, want %d", memBytes, expected)
	}
}

func TestCapacityDetectMemoryMissing(t *testing.T) {
	c := &Capacity{
		procMeminfo: "/nonexistent/path",
		log:         slog.Default(),
	}

	_, err := c.detectMemory(t.Context())
	if err == nil {
		t.Error("detectMemory() expected error for missing file")
	}
}

func TestCapacityDetectMemoryMalformed(t *testing.T) {
	tmpDir := t.TempDir()
	meminfoPath := filepath.Join(tmpDir, "meminfo")

	// Write malformed meminfo (no MemTotal)
	content := `MemFree:         1234567 kB
Buffers:          123456 kB
`
	if err := os.WriteFile(meminfoPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write meminfo: %v", err)
	}

	c := &Capacity{
		procMeminfo: meminfoPath,
		log:         slog.Default(),
	}

	_, err := c.detectMemory(t.Context())
	if err == nil {
		t.Error("detectMemory() expected error for missing MemTotal")
	}
}

func TestSanitizeCgroupName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple-id", "simple-id"},
		{"id/with/slashes", "id_with_slashes"},
		{"", ""},
		{"uuid-1234-5678", "uuid-1234-5678"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeCgroupName(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeCgroupName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestPriorityWeights(t *testing.T) {
	// CPU weights
	if cpuWeightNormal <= cpuWeightLow {
		t.Errorf("cpuWeightNormal (%d) should be greater than cpuWeightLow (%d)", cpuWeightNormal, cpuWeightLow)
	}

	if cpuWeightNormal != 100 {
		t.Errorf("cpuWeightNormal = %d, want 100", cpuWeightNormal)
	}

	if cpuWeightLow != 50 {
		t.Errorf("cpuWeightLow = %d, want 50", cpuWeightLow)
	}

	// IO weights
	if ioWeightNormal <= ioWeightLow {
		t.Errorf("ioWeightNormal (%d) should be greater than ioWeightLow (%d)", ioWeightNormal, ioWeightLow)
	}

	if ioWeightNormal != 100 {
		t.Errorf("ioWeightNormal = %d, want 100", ioWeightNormal)
	}

	if ioWeightLow != 50 {
		t.Errorf("ioWeightLow = %d, want 50", ioWeightLow)
	}

	// Memory high ratio
	if memoryHighRatio <= 0 || memoryHighRatio >= 1 {
		t.Errorf("memoryHighRatio (%v) should be between 0 and 1", memoryHighRatio)
	}

	if memoryHighRatio != 0.8 {
		t.Errorf("memoryHighRatio = %v, want 0.8", memoryHighRatio)
	}
}

func TestRequiredControllers(t *testing.T) {
	// Verify that cpu, io, and memory controllers are required
	expected := map[string]bool{"cpu": true, "io": true, "memory": true}

	if len(requiredControllers) != len(expected) {
		t.Errorf("requiredControllers has %d controllers, want %d", len(requiredControllers), len(expected))
	}

	for _, ctrl := range requiredControllers {
		if !expected[ctrl] {
			t.Errorf("unexpected controller %q in requiredControllers", ctrl)
		}
	}
}

func TestVMPriorityValues(t *testing.T) {
	// Ensure the priority enum values are as expected
	if api.VMPriority_PRIORITY_NORMAL != 0 {
		t.Errorf("PRIORITY_NORMAL = %d, want 0", api.VMPriority_PRIORITY_NORMAL)
	}
	if api.VMPriority_PRIORITY_LOW != 1 {
		t.Errorf("PRIORITY_LOW = %d, want 1", api.VMPriority_PRIORITY_LOW)
	}
}

func TestDefaultConfig(t *testing.T) {
	if DefaultPollInterval != 30*time.Second {
		t.Errorf("DefaultPollInterval = %v, want 30s", DefaultPollInterval)
	}
}

func TestReadCPUUsageParsing(t *testing.T) {
	// Test the CPU usage parsing logic similar to resourcemonitor
	tests := []struct {
		name            string
		data            string
		expectedSeconds float64
		wantErr         bool
	}{
		{
			name:            "valid data",
			data:            "1234 (process) S 1 1234 1234 0 -1 4194304 100 0 0 0 1000 500 0 0 20 0 1 0 1000 1000 100 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0",
			expectedSeconds: 15.0, // (1000 + 500) / 100
			wantErr:         false,
		},
		{
			name:            "process with parens in name",
			data:            "1234 (process (test)) S 1 1234 1234 0 -1 4194304 100 0 0 0 2000 1000 0 0 20 0 1 0 1000 1000 100 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0",
			expectedSeconds: 30.0, // (2000 + 1000) / 100
			wantErr:         false,
		},
		{
			name:    "missing closing paren",
			data:    "1234 (process S 1 1234",
			wantErr: true,
		},
		{
			name:    "insufficient fields",
			data:    "1234 (process) S 1 1234",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file with stat data
			tmpDir := t.TempDir()
			statPath := filepath.Join(tmpDir, "stat")
			if err := os.WriteFile(statPath, []byte(tt.data), 0o644); err != nil {
				t.Fatalf("failed to write stat: %v", err)
			}

			data, err := os.ReadFile(statPath)
			if err != nil {
				t.Fatalf("failed to read stat: %v", err)
			}

			// Parse the data manually (similar to readCPUUsage)
			got, parseErr := parseCPUUsage(data)

			if tt.wantErr {
				if parseErr == nil {
					t.Errorf("expected error but got nil")
				}
				return
			}
			if parseErr != nil {
				t.Errorf("unexpected error: %v", parseErr)
				return
			}
			if got != tt.expectedSeconds {
				t.Errorf("parseCPUUsage() = %v, want %v", got, tt.expectedSeconds)
			}
		})
	}
}

// parseCPUUsage is a test helper that mimics the parsing logic
func parseCPUUsage(data []byte) (float64, error) {
	const clockTicks = 100.0

	// Find the last closing paren
	closing := -1
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == ')' {
			closing = i
			break
		}
	}
	if closing == -1 {
		return 0, os.ErrNotExist
	}

	// Parse fields after the closing paren
	fields := make([]string, 0, 20)
	field := ""
	for i := closing + 1; i < len(data); i++ {
		if data[i] == ' ' || data[i] == '\n' {
			if field != "" {
				fields = append(fields, field)
				field = ""
			}
		} else {
			field += string(data[i])
		}
	}
	if field != "" {
		fields = append(fields, field)
	}

	if len(fields) < 14 {
		return 0, os.ErrNotExist
	}

	// Parse utime (field 11, 0-indexed after state)
	var utime, stime uint64
	for i, f := range fields[11:13] {
		var val uint64
		for _, c := range f {
			if c >= '0' && c <= '9' {
				val = val*10 + uint64(c-'0')
			}
		}
		if i == 0 {
			utime = val
		} else {
			stime = val
		}
	}

	return float64(utime+stime) / clockTicks, nil
}

func TestCgroupPath(t *testing.T) {
	m := &ResourceManager{
		cgroupRoot: "/sys/fs/cgroup",
	}

	id := "test-vm-123"
	expectedScope := "vm-test-vm-123.scope"
	expectedPath := filepath.Join(m.cgroupRoot, cgroupSlice, expectedScope)

	scopeName := "vm-" + sanitizeCgroupName(id) + ".scope"
	cgroupPath := filepath.Join(m.cgroupRoot, cgroupSlice, scopeName)

	if cgroupPath != expectedPath {
		t.Errorf("cgroup path = %q, want %q", cgroupPath, expectedPath)
	}
}

func TestCleanupMissing(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	m := &ResourceManager{
		usageState:       make(map[string]*vmUsageState),
		priorityOverride: make(map[string]api.VMPriority),
		cgroupRoot:       tmpDir,
		log:              slog.Default(),
	}

	// Add some state
	m.usageState["vm1"] = &vmUsageState{name: "vm1"}
	m.usageState["vm2"] = &vmUsageState{name: "vm2"}
	m.usageState["vm3"] = &vmUsageState{name: "vm3"}
	m.priorityOverride["vm1"] = api.VMPriority_PRIORITY_LOW
	m.priorityOverride["vm3"] = api.VMPriority_PRIORITY_LOW

	// Only vm1 and vm2 are still running
	seen := map[string]struct{}{
		"vm1": {},
		"vm2": {},
	}

	m.cleanupMissing(ctx, seen)

	// vm3 should be removed
	if _, ok := m.usageState["vm1"]; !ok {
		t.Error("vm1 should still exist")
	}
	if _, ok := m.usageState["vm2"]; !ok {
		t.Error("vm2 should still exist")
	}
	if _, ok := m.usageState["vm3"]; ok {
		t.Error("vm3 should be removed")
	}

	// Priority override for vm3 should be removed
	if _, ok := m.priorityOverride["vm1"]; !ok {
		t.Error("vm1 priority override should still exist")
	}
	if _, ok := m.priorityOverride["vm3"]; ok {
		t.Error("vm3 priority override should be removed")
	}
}

func TestDefaultGroupID(t *testing.T) {
	if defaultGroupID != "default" {
		t.Errorf("defaultGroupID = %q, want %q", defaultGroupID, "default")
	}
}

func TestAccountSlicePath(t *testing.T) {
	m := &ResourceManager{
		cgroupRoot: "/sys/fs/cgroup",
	}

	tests := []struct {
		name        string
		groupID     string
		expectedDir string
	}{
		{
			name:        "specific group",
			groupID:     "acct_123",
			expectedDir: "acct_123.slice",
		},
		{
			name:        "group with special chars",
			groupID:     "acct/with/slashes",
			expectedDir: "acct_with_slashes.slice",
		},
		{
			name:        "empty group uses default",
			groupID:     "",
			expectedDir: "default.slice",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groupID := tt.groupID
			if groupID == "" {
				groupID = defaultGroupID
			}
			sliceName := sanitizeCgroupName(groupID) + ".slice"
			slicePath := filepath.Join(m.cgroupRoot, cgroupSlice, sliceName)

			expectedPath := filepath.Join(m.cgroupRoot, cgroupSlice, tt.expectedDir)
			if slicePath != expectedPath {
				t.Errorf("group slice path = %q, want %q", slicePath, expectedPath)
			}
		})
	}
}

func TestCgroupPathWithGroup(t *testing.T) {
	m := &ResourceManager{
		cgroupRoot: "/sys/fs/cgroup",
	}

	tests := []struct {
		name         string
		vmID         string
		groupID      string
		expectedPath string
	}{
		{
			name:         "vm with group",
			vmID:         "vm000001-testbox",
			groupID:      "acct_123",
			expectedPath: "/sys/fs/cgroup/exelet.slice/acct_123.slice/vm-vm000001-testbox.scope",
		},
		{
			name:         "vm with default group",
			vmID:         "vm000002-anotherbox",
			groupID:      "",
			expectedPath: "/sys/fs/cgroup/exelet.slice/default.slice/vm-vm000002-anotherbox.scope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groupID := tt.groupID
			if groupID == "" {
				groupID = defaultGroupID
			}
			sliceName := sanitizeCgroupName(groupID) + ".slice"
			groupSlicePath := filepath.Join(m.cgroupRoot, cgroupSlice, sliceName)
			scopeName := "vm-" + sanitizeCgroupName(tt.vmID) + ".scope"
			cgroupPath := filepath.Join(groupSlicePath, scopeName)

			if cgroupPath != tt.expectedPath {
				t.Errorf("cgroup path = %q, want %q", cgroupPath, tt.expectedPath)
			}
		})
	}
}

func TestReadCgroupMemory(t *testing.T) {
	tmpDir := t.TempDir()
	m := &ResourceManager{cgroupRoot: tmpDir}

	// Set up a fake cgroup directory
	cgroupPath := m.vmCgroupPath("vm-test", "acct_123")
	if err := os.MkdirAll(cgroupPath, 0o755); err != nil {
		t.Fatalf("failed to create cgroup dir: %v", err)
	}

	// Write memory.current
	if err := os.WriteFile(filepath.Join(cgroupPath, "memory.current"), []byte("428326912\n"), 0o644); err != nil {
		t.Fatalf("failed to write memory.current: %v", err)
	}

	got, err := m.readCgroupMemory(cgroupPath)
	if err != nil {
		t.Fatalf("readCgroupMemory failed: %v", err)
	}
	if got != 428326912 {
		t.Errorf("readCgroupMemory() = %d, want 428326912", got)
	}
}

func TestReadCgroupSwap(t *testing.T) {
	tmpDir := t.TempDir()
	m := &ResourceManager{cgroupRoot: tmpDir}

	// Set up a fake cgroup directory
	cgroupPath := m.vmCgroupPath("vm-test", "acct_123")
	if err := os.MkdirAll(cgroupPath, 0o755); err != nil {
		t.Fatalf("failed to create cgroup dir: %v", err)
	}

	// Write memory.swap.current
	if err := os.WriteFile(filepath.Join(cgroupPath, "memory.swap.current"), []byte("6195773440\n"), 0o644); err != nil {
		t.Fatalf("failed to write memory.swap.current: %v", err)
	}

	got, err := m.readCgroupSwap(cgroupPath)
	if err != nil {
		t.Fatalf("readCgroupSwap failed: %v", err)
	}
	if got != 6195773440 {
		t.Errorf("readCgroupSwap() = %d, want 6195773440", got)
	}
}

func TestReadCgroupMemoryMissing(t *testing.T) {
	m := &ResourceManager{cgroupRoot: "/nonexistent"}
	_, err := m.readCgroupMemory("/nonexistent/path")
	if err == nil {
		t.Error("expected error for missing cgroup file")
	}
}

func TestVmCgroupPath(t *testing.T) {
	m := &ResourceManager{cgroupRoot: "/sys/fs/cgroup"}

	tests := []struct {
		name     string
		vmID     string
		groupID  string
		expected string
	}{
		{
			name:     "with group",
			vmID:     "vm000001-testbox",
			groupID:  "acct_123",
			expected: "/sys/fs/cgroup/exelet.slice/acct_123.slice/vm-vm000001-testbox.scope",
		},
		{
			name:     "empty group uses default",
			vmID:     "vm000002-anotherbox",
			groupID:  "",
			expected: "/sys/fs/cgroup/exelet.slice/default.slice/vm-vm000002-anotherbox.scope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.vmCgroupPath(tt.vmID, tt.groupID)
			if got != tt.expected {
				t.Errorf("vmCgroupPath(%q, %q) = %q, want %q", tt.vmID, tt.groupID, got, tt.expected)
			}
		})
	}
}

func TestVmUsageStateGroupID(t *testing.T) {
	state := &vmUsageState{
		name:    "test-vm",
		groupID: "acct_456",
	}

	if state.groupID != "acct_456" {
		t.Errorf("groupID = %q, want %q", state.groupID, "acct_456")
	}

	// Test empty groupID
	emptyState := &vmUsageState{
		name: "test-vm-2",
	}
	if emptyState.groupID != "" {
		t.Errorf("empty groupID = %q, want empty string", emptyState.groupID)
	}
}

func TestInitControllersCpuset(t *testing.T) {
	tmpDir := t.TempDir()

	// Create fake cgroup.subtree_control files so enableController can work
	rootSubtreeControl := filepath.Join(tmpDir, "cgroup.subtree_control")
	if err := os.WriteFile(rootSubtreeControl, []byte(""), 0o644); err != nil {
		t.Fatalf("write root subtree_control: %v", err)
	}

	t.Run("reserves CPUs when configured", func(t *testing.T) {
		sliceDir := filepath.Join(tmpDir, cgroupSlice)
		os.RemoveAll(sliceDir)

		m := &ResourceManager{
			config:     &config.ExeletConfig{ReservedCPUs: 2},
			cgroupRoot: tmpDir,
			log:        slog.Default(),
		}
		m.initControllers(t.Context())

		// Verify cpuset.cpus was written on the slice
		data, err := os.ReadFile(filepath.Join(sliceDir, "cpuset.cpus"))
		if err != nil {
			t.Fatalf("read cpuset.cpus: %v", err)
		}
		got := strings.TrimSpace(string(data))
		want := fmt.Sprintf("2-%d", runtime.NumCPU()-1)
		if got != want {
			t.Errorf("cpuset.cpus = %q, want %q", got, want)
		}

		// Verify cpuset was enabled in root subtree_control
		rootData, err := os.ReadFile(rootSubtreeControl)
		if err != nil {
			t.Fatalf("read root subtree_control: %v", err)
		}
		if !strings.Contains(string(rootData), "cpuset") {
			t.Error("cpuset not enabled in root cgroup.subtree_control")
		}
	})

	t.Run("skips cpuset when not configured", func(t *testing.T) {
		sliceDir := filepath.Join(tmpDir, cgroupSlice)
		os.RemoveAll(sliceDir)
		// Reset subtree_control
		os.WriteFile(rootSubtreeControl, []byte(""), 0o644)

		m := &ResourceManager{
			config:     &config.ExeletConfig{},
			cgroupRoot: tmpDir,
			log:        slog.Default(),
		}
		m.initControllers(t.Context())

		// cpuset.cpus should NOT exist
		if _, err := os.Stat(filepath.Join(sliceDir, "cpuset.cpus")); err == nil {
			t.Error("cpuset.cpus should not exist when ReservedCPUs=0")
		}
	})
}

func TestPollInstanceStoppedZerosRuntimeMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	m := &ResourceManager{
		config:           &config.ExeletConfig{}, // no StorageManagerAddress → readZFSVolumeInfo returns nil
		usageState:       make(map[string]*vmUsageState),
		priorityOverride: make(map[string]api.VMPriority),
		cgroupRoot:       tmpDir,
		log:              slog.Default(),
	}

	now := time.Now()
	m.pollInstance(t.Context(), "vm-stopped", "stopped-vm", "grp1", nil, computeapi.VMState_STOPPED, now)

	m.usageMu.Lock()
	state, exists := m.usageState["vm-stopped"]
	m.usageMu.Unlock()

	if !exists {
		t.Fatal("expected usageState entry for stopped VM")
	}

	// Runtime metrics should all be zero
	if state.cpuSeconds != 0 {
		t.Errorf("cpuSeconds = %v, want 0", state.cpuSeconds)
	}
	if state.cpuPercent != 0 {
		t.Errorf("cpuPercent = %v, want 0", state.cpuPercent)
	}
	if state.memoryBytes != 0 {
		t.Errorf("memoryBytes = %d, want 0", state.memoryBytes)
	}
	if state.swapBytes != 0 {
		t.Errorf("swapBytes = %d, want 0", state.swapBytes)
	}
	if state.netRxBytes != 0 {
		t.Errorf("netRxBytes = %d, want 0", state.netRxBytes)
	}
	if state.netTxBytes != 0 {
		t.Errorf("netTxBytes = %d, want 0", state.netTxBytes)
	}
	if state.ioReadBytes != 0 {
		t.Errorf("ioReadBytes = %d, want 0", state.ioReadBytes)
	}
	if state.ioWriteBytes != 0 {
		t.Errorf("ioWriteBytes = %d, want 0", state.ioWriteBytes)
	}
}

func TestPollInstancePausedAttemptsFullCollection(t *testing.T) {
	// PAUSED VMs should take the collectUsage path, not the stopped path.
	// collectUsage will fail (no cloud-hypervisor socket), causing pollInstance
	// to return early without creating a usageState entry.
	tmpDir := t.TempDir()
	m := &ResourceManager{
		config:           &config.ExeletConfig{},
		usageState:       make(map[string]*vmUsageState),
		priorityOverride: make(map[string]api.VMPriority),
		cgroupRoot:       tmpDir,
		log:              slog.Default(),
	}

	now := time.Now()
	m.pollInstance(t.Context(), "vm-paused", "paused-vm", "grp1", nil, computeapi.VMState_PAUSED, now)

	// collectUsage fails → returns early → no state entry created
	m.usageMu.Lock()
	_, exists := m.usageState["vm-paused"]
	m.usageMu.Unlock()

	if exists {
		t.Error("PAUSED VM should attempt collectUsage (which fails here), not take the stopped path")
	}
}

func TestPollInstanceStoppedCPUPercentNotNegative(t *testing.T) {
	// Simulate a running→stopped transition: pre-populate state with prevCPUSeconds > 0,
	// then poll as STOPPED. cpuPercent must be 0, not negative.
	tmpDir := t.TempDir()
	m := &ResourceManager{
		config:           &config.ExeletConfig{},
		usageState:       make(map[string]*vmUsageState),
		priorityOverride: make(map[string]api.VMPriority),
		cgroupRoot:       tmpDir,
		log:              slog.Default(),
	}

	prevTime := time.Now().Add(-30 * time.Second)
	m.usageState["vm-transition"] = &vmUsageState{
		name:           "transition-vm",
		groupID:        "grp1",
		priority:       api.VMPriority_PRIORITY_NORMAL,
		prevCPUSeconds: 10.0,
		cpuSeconds:     10.0,
		prevPollTime:   prevTime,
	}

	now := time.Now()
	m.pollInstance(t.Context(), "vm-transition", "transition-vm", "grp1", nil, computeapi.VMState_STOPPED, now)

	m.usageMu.Lock()
	state := m.usageState["vm-transition"]
	m.usageMu.Unlock()

	if state.cpuPercent < 0 {
		t.Errorf("cpuPercent = %v, must not be negative after running→stopped transition", state.cpuPercent)
	}
	if state.cpuPercent != 0 {
		t.Errorf("cpuPercent = %v, want 0 for stopped VM", state.cpuPercent)
	}
	// prevCPUSeconds should be updated to 0 (the new usage.cpuSeconds)
	if state.prevCPUSeconds != 0 {
		t.Errorf("prevCPUSeconds = %v, want 0", state.prevCPUSeconds)
	}
}
