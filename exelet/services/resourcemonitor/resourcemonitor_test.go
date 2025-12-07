package resourcemonitor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"exe.dev/exelet/utils"
)

func TestGetTapName(t *testing.T) {
	// Test that utils.GetTapName matches the expected values
	tests := []struct {
		id       string
		expected string
	}{
		{"test-id-123", "tap-4595da"},
		{"", "tap-da39a3"},
		{"vm000025", "tap-8f0c4b"},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got := utils.GetTapName(tt.id)
			if got != tt.expected {
				t.Errorf("utils.GetTapName(%q) = %q, want %q", tt.id, got, tt.expected)
			}
		})
	}
}

func TestReadNetStat(t *testing.T) {
	// Create a temporary directory structure mimicking /sys/class/net
	tmpDir := t.TempDir()
	ifaceName := "tap-test"
	statsDir := filepath.Join(tmpDir, "class", "net", ifaceName, "statistics")
	if err := os.MkdirAll(statsDir, 0755); err != nil {
		t.Fatalf("failed to create stats dir: %v", err)
	}

	// Write test values
	if err := os.WriteFile(filepath.Join(statsDir, "rx_bytes"), []byte("12345\n"), 0644); err != nil {
		t.Fatalf("failed to write rx_bytes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(statsDir, "tx_bytes"), []byte("67890\n"), 0644); err != nil {
		t.Fatalf("failed to write tx_bytes: %v", err)
	}

	m := &ResourceMonitor{sysRoot: tmpDir}
	ctx := context.Background()

	rxBytes, err := m.readNetStat(ctx, ifaceName, "rx_bytes")
	if err != nil {
		t.Errorf("readNetStat rx_bytes failed: %v", err)
	}
	if rxBytes != 12345 {
		t.Errorf("readNetStat rx_bytes = %d, want 12345", rxBytes)
	}

	txBytes, err := m.readNetStat(ctx, ifaceName, "tx_bytes")
	if err != nil {
		t.Errorf("readNetStat tx_bytes failed: %v", err)
	}
	if txBytes != 67890 {
		t.Errorf("readNetStat tx_bytes = %d, want 67890", txBytes)
	}
}

func TestReadNetStatTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	m := &ResourceMonitor{sysRoot: tmpDir}

	// Create a context that's already cancelled
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(10 * time.Millisecond) // Ensure timeout expires

	_, err := m.readNetStat(ctx, "nonexistent", "rx_bytes")
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestRecordNetStats(t *testing.T) {
	m := &ResourceMonitor{
		netState: make(map[string]*networkState),
	}

	// First observation - should return full values as delta
	rxDelta, txDelta := m.recordNetStats("vm1", "test-vm", "tap-abc123", 1000, 2000)
	if rxDelta != 1000 {
		t.Errorf("first rxDelta = %d, want 1000", rxDelta)
	}
	if txDelta != 2000 {
		t.Errorf("first txDelta = %d, want 2000", txDelta)
	}

	// Second observation - should return delta
	rxDelta, txDelta = m.recordNetStats("vm1", "test-vm", "tap-abc123", 1500, 2500)
	if rxDelta != 500 {
		t.Errorf("second rxDelta = %d, want 500", rxDelta)
	}
	if txDelta != 500 {
		t.Errorf("second txDelta = %d, want 500", txDelta)
	}

	// Counter wrap - should return new value
	rxDelta, txDelta = m.recordNetStats("vm1", "test-vm", "tap-abc123", 100, 200)
	if rxDelta != 100 {
		t.Errorf("wrap rxDelta = %d, want 100", rxDelta)
	}
	if txDelta != 200 {
		t.Errorf("wrap txDelta = %d, want 200", txDelta)
	}
}

func TestRecordNetStatsNameChange(t *testing.T) {
	m := &ResourceMonitor{
		netState: make(map[string]*networkState),
	}

	// First observation
	m.recordNetStats("vm1", "old-name", "tap-abc123", 1000, 2000)

	// Name change - should reset state and return full values
	rxDelta, txDelta := m.recordNetStats("vm1", "new-name", "tap-abc123", 1500, 2500)
	if rxDelta != 1500 {
		t.Errorf("name change rxDelta = %d, want 1500", rxDelta)
	}
	if txDelta != 2500 {
		t.Errorf("name change txDelta = %d, want 2500", txDelta)
	}
}

func TestGetOrCacheTapName(t *testing.T) {
	m := &ResourceMonitor{
		netState: make(map[string]*networkState),
	}

	// First call should compute the tap name
	tapName := m.getOrCacheTapName("vm1")
	expected := utils.GetTapName("vm1")
	if tapName != expected {
		t.Errorf("getOrCacheTapName first call = %q, want %q", tapName, expected)
	}

	// Cache it in state
	m.netState["vm1"] = &networkState{tapName: expected, name: "test"}

	// Second call should return cached value
	tapName = m.getOrCacheTapName("vm1")
	if tapName != expected {
		t.Errorf("getOrCacheTapName cached call = %q, want %q", tapName, expected)
	}
}

func TestRecordDiskState(t *testing.T) {
	m := &ResourceMonitor{
		diskState: make(map[string]string),
	}

	m.recordDiskState("vm1", "test-vm")
	if m.diskState["vm1"] != "test-vm" {
		t.Errorf("diskState[vm1] = %q, want %q", m.diskState["vm1"], "test-vm")
	}

	// Update with same name
	m.recordDiskState("vm1", "test-vm")
	if m.diskState["vm1"] != "test-vm" {
		t.Errorf("diskState[vm1] after same name = %q, want %q", m.diskState["vm1"], "test-vm")
	}

	// Update with different name
	m.recordDiskState("vm1", "new-name")
	if m.diskState["vm1"] != "new-name" {
		t.Errorf("diskState[vm1] after name change = %q, want %q", m.diskState["vm1"], "new-name")
	}
}

func TestForgetNetState(t *testing.T) {
	m := &ResourceMonitor{
		netState: make(map[string]*networkState),
	}

	m.netState["vm1"] = &networkState{rxBytes: 100, txBytes: 200, name: "test"}
	m.forgetNetState("vm1")

	if _, ok := m.netState["vm1"]; ok {
		t.Error("forgetNetState did not delete state")
	}

	// Should not panic on non-existent key
	m.forgetNetState("vm2")
}

func TestForgetDiskState(t *testing.T) {
	m := &ResourceMonitor{
		diskState: make(map[string]string),
	}

	m.diskState["vm1"] = "test-vm"
	m.forgetDiskState("vm1")

	if _, ok := m.diskState["vm1"]; ok {
		t.Error("forgetDiskState did not delete state")
	}

	// Should not panic on non-existent key
	m.forgetDiskState("vm2")
}

func TestParseProcessTotalTicks(t *testing.T) {
	// Test the existing CPU parsing function
	tests := []struct {
		name     string
		data     string
		expected uint64
		wantErr  bool
	}{
		{
			name:     "valid data",
			data:     "1234 (process) S 1 1234 1234 0 -1 4194304 100 0 0 0 10 20 0 0 20 0 1 0 1000 1000 100 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0",
			expected: 30, // utime=10, stime=20
			wantErr:  false,
		},
		{
			name:     "process with parens in name",
			data:     "1234 (process (with) parens) S 1 1234 1234 0 -1 4194304 100 0 0 0 15 25 0 0 20 0 1 0 1000 1000 100 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0",
			expected: 40, // utime=15, stime=25
			wantErr:  false,
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
			got, err := parseProcessTotalTicks([]byte(tt.data))
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseProcessTotalTicks() expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("parseProcessTotalTicks() unexpected error: %v", err)
				return
			}
			if got != tt.expected {
				t.Errorf("parseProcessTotalTicks() = %d, want %d", got, tt.expected)
			}
		})
	}
}
