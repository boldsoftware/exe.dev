package collector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryCollect(t *testing.T) {
	content := `MemTotal:       16384000 kB
MemFree:         4096000 kB
MemAvailable:    8192000 kB
Buffers:         1024000 kB
Cached:          2048000 kB
SwapTotal:       2048000 kB
SwapFree:        1024000 kB
`
	dir := t.TempDir()
	path := filepath.Join(dir, "meminfo")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &Memory{procPath: path}
	if err := m.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	expectedTotal := int64(16384000) * 1024
	if m.Total != expectedTotal {
		t.Errorf("Total = %d, want %d", m.Total, expectedTotal)
	}

	// Should use MemAvailable (8192000 kB) instead of MemFree+Buffers+Cached.
	expectedFree := int64(8192000) * 1024
	if m.Free != expectedFree {
		t.Errorf("Free = %d, want %d", m.Free, expectedFree)
	}

	expectedUsed := expectedTotal - expectedFree
	if m.Used != expectedUsed {
		t.Errorf("Used = %d, want %d", m.Used, expectedUsed)
	}

	expectedSwap := int64(2048000-1024000) * 1024
	if m.SwapUsed != expectedSwap {
		t.Errorf("SwapUsed = %d, want %d", m.SwapUsed, expectedSwap)
	}
}

func TestMemoryCollectFallback(t *testing.T) {
	// Kernel without MemAvailable — should fall back to MemFree+Buffers+Cached.
	content := `MemTotal:       16384000 kB
MemFree:         4096000 kB
Buffers:         1024000 kB
Cached:          2048000 kB
SwapTotal:       2048000 kB
SwapFree:        1024000 kB
`
	dir := t.TempDir()
	path := filepath.Join(dir, "meminfo")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &Memory{procPath: path}
	if err := m.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	expectedTotal := int64(16384000) * 1024
	expectedFree := int64(4096000+1024000+2048000) * 1024
	expectedUsed := expectedTotal - expectedFree

	if m.Total != expectedTotal {
		t.Errorf("Total = %d, want %d", m.Total, expectedTotal)
	}
	if m.Free != expectedFree {
		t.Errorf("Free = %d, want %d", m.Free, expectedFree)
	}
	if m.Used != expectedUsed {
		t.Errorf("Used = %d, want %d", m.Used, expectedUsed)
	}
}
