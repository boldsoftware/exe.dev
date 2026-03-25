package execore

import (
	"testing"
	"time"

	"exe.dev/metricsd/types"
)

func TestComputeUsageData(t *testing.T) {
	now := time.Now().UTC()
	metrics := []types.Metric{
		{Timestamp: now, VMName: "vm-a", DiskSizeBytes: 20e9, DiskUsedBytes: 3e9, DiskLogicalUsedBytes: 5e9, CPUUsedCumulativeSecs: 100, CPUNominal: 2, NetworkTXBytes: 1000, NetworkRXBytes: 500, MemoryNominalBytes: 8e9, MemoryRSSBytes: 4e9, MemorySwapBytes: 1e9, IOReadBytes: 10000, IOWriteBytes: 20000},
		{Timestamp: now.Add(10 * time.Minute), VMName: "vm-a", DiskSizeBytes: 20e9, DiskUsedBytes: 4e9, DiskLogicalUsedBytes: 6e9, CPUUsedCumulativeSecs: 220, CPUNominal: 2, NetworkTXBytes: 2000000, NetworkRXBytes: 1000000, MemoryNominalBytes: 8e9, MemoryRSSBytes: 5e9, MemorySwapBytes: 2e9, IOReadBytes: 10000000, IOWriteBytes: 20000000},
	}

	points := computeUsageData(metrics)

	// First point is dropped (no delta)
	if len(points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(points))
	}

	p := points[0]

	// Disk
	if p.DiskSizeGB != 20.0 {
		t.Errorf("disk_size_gb: got %f, want 20.0", p.DiskSizeGB)
	}
	if p.DiskUsedGB != 4.0 {
		t.Errorf("disk_used_gb: got %f, want 4.0", p.DiskUsedGB)
	}
	if p.DiskLogicalUsedGB != 6.0 {
		t.Errorf("disk_logical_used_gb: got %f, want 6.0", p.DiskLogicalUsedGB)
	}

	// CPU: 120 cpu-sec / 600s = 0.2 cores used
	dt := 10 * 60.0 // 600s
	expectedCPU := 120 / dt
	if p.CPUCores < expectedCPU-0.001 || p.CPUCores > expectedCPU+0.001 {
		t.Errorf("cpu_cores: got %f, want ~%f", p.CPUCores, expectedCPU)
	}
	if p.CPUNominal != 2.0 {
		t.Errorf("cpu_nominal: got %f, want 2.0", p.CPUNominal)
	}

	// Network: TX delta = 1999000 bytes, 8 bits/byte, / 1e6 Mbps / 600s
	expectedTX := float64(1999000) * 8 / 1e6 / dt
	if p.NetworkTXMbps < expectedTX-0.001 || p.NetworkTXMbps > expectedTX+0.001 {
		t.Errorf("network_tx_mbps: got %f, want ~%f", p.NetworkTXMbps, expectedTX)
	}

	// Memory
	if p.MemoryNominalGB != 8.0 {
		t.Errorf("memory_nominal_gb: got %f, want 8.0", p.MemoryNominalGB)
	}
	if p.MemoryRSSGB != 5.0 {
		t.Errorf("memory_rss_gb: got %f, want 5.0", p.MemoryRSSGB)
	}
	if p.MemorySwapGB != 2.0 {
		t.Errorf("memory_swap_gb: got %f, want 2.0", p.MemorySwapGB)
	}

	// IO rates
	expectedIORead := float64(10000000-10000) / dt / 1e6
	if p.IOReadMBps < expectedIORead-0.001 || p.IOReadMBps > expectedIORead+0.001 {
		t.Errorf("io_read_mbps: got %f, want ~%f", p.IOReadMBps, expectedIORead)
	}
	expectedIOWrite := float64(20000000-20000) / dt / 1e6
	if p.IOWriteMBps < expectedIOWrite-0.001 || p.IOWriteMBps > expectedIOWrite+0.001 {
		t.Errorf("io_write_mbps: got %f, want ~%f", p.IOWriteMBps, expectedIOWrite)
	}

	if p.VMName != "vm-a" {
		t.Errorf("vm_name: got %q, want %q", p.VMName, "vm-a")
	}
}

func TestComputeUsageData_Empty(t *testing.T) {
	points := computeUsageData(nil)
	if points != nil {
		t.Errorf("expected nil, got %v", points)
	}
}

func TestComputeUsageData_SinglePoint(t *testing.T) {
	metrics := []types.Metric{
		{Timestamp: time.Now(), VMName: "vm-a", DiskSizeBytes: 10e9},
	}
	points := computeUsageData(metrics)
	// Single point returned since we can't drop the first from a 1-element slice
	if len(points) != 1 {
		t.Errorf("expected 1 point, got %d", len(points))
	}
}
