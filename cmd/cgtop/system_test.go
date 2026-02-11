//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// computeCPUUsage
// ---------------------------------------------------------------------------

func TestComputeCPUUsage_ZeroDelta(t *testing.T) {
	j := cpuJiffies{user: 100, nice: 0, system: 50, idle: 800, iowait: 10, irq: 5, softirq: 5, steal: 30}
	usage := computeCPUUsage(j, j)
	if usage.IdlePct != 100 {
		t.Errorf("IdlePct = %f, want 100", usage.IdlePct)
	}
	if usage.UserPct != 0 {
		t.Errorf("UserPct = %f, want 0", usage.UserPct)
	}
}

func TestComputeCPUUsage_NormalDelta(t *testing.T) {
	prev := cpuJiffies{user: 100, nice: 0, system: 50, idle: 800, iowait: 10, irq: 5, softirq: 5, steal: 30}
	cur := cpuJiffies{user: 200, nice: 0, system: 100, idle: 1600, iowait: 30, irq: 10, softirq: 10, steal: 50}

	usage := computeCPUUsage(prev, cur)

	// Total delta = (200+0+100+1600+30+10+10+50) - (100+0+50+800+10+5+5+30) = 2000-1000 = 1000
	// User delta = (200+0)-(100+0) = 100 -> 10%
	// Sys delta = (100+10+10)-(50+5+5) = 60 -> 6%
	// Idle delta = 1600-800 = 800 -> 80%
	// IOWait delta = 30-10 = 20 -> 2%

	assertClose(t, "UserPct", usage.UserPct, 10.0)
	assertClose(t, "SysPct", usage.SysPct, 6.0)
	assertClose(t, "IdlePct", usage.IdlePct, 80.0)
	assertClose(t, "IOWaitPct", usage.IOWaitPct, 2.0)
}

func assertClose(t *testing.T, name string, got, want float64) {
	t.Helper()
	if diff := got - want; diff > 0.01 || diff < -0.01 {
		t.Errorf("%s = %f, want %f", name, got, want)
	}
}

// ---------------------------------------------------------------------------
// cpuJiffies.total
// ---------------------------------------------------------------------------

func TestCPUJiffies_Total(t *testing.T) {
	j := cpuJiffies{user: 1, nice: 2, system: 3, idle: 4, iowait: 5, irq: 6, softirq: 7, steal: 8}
	if got := j.total(); got != 36 {
		t.Errorf("total() = %d, want 36", got)
	}
}

// ---------------------------------------------------------------------------
// readPSI
// ---------------------------------------------------------------------------

func TestReadPSI(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "cpu.pressure")
	content := `some avg10=1.50 avg60=2.30 avg300=3.10 total=12345
full avg10=0.50 avg60=0.60 avg300=0.70 total=6789
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	psi, err := readPSI(path)
	if err != nil {
		t.Fatal(err)
	}

	assertClose(t, "Some.Avg10", psi.Some.Avg10, 1.50)
	assertClose(t, "Some.Avg60", psi.Some.Avg60, 2.30)
	assertClose(t, "Some.Avg300", psi.Some.Avg300, 3.10)
	assertClose(t, "Full.Avg10", psi.Full.Avg10, 0.50)
	assertClose(t, "Full.Avg60", psi.Full.Avg60, 0.60)
	assertClose(t, "Full.Avg300", psi.Full.Avg300, 0.70)
}

func TestReadPSI_FileNotFound(t *testing.T) {
	_, err := readPSI("/nonexistent/path")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestReadPSI_OnlySome(t *testing.T) {
	// Some PSI files (like cpu.pressure on some kernels) only have "some" line.
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "cpu.pressure")
	content := "some avg10=5.00 avg60=4.00 avg300=3.00 total=99999\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	psi, err := readPSI(path)
	if err != nil {
		t.Fatal(err)
	}

	assertClose(t, "Some.Avg10", psi.Some.Avg10, 5.0)
	// Full should remain zero.
	if psi.Full.Avg10 != 0 {
		t.Errorf("Full.Avg10 = %f, want 0", psi.Full.Avg10)
	}
}
