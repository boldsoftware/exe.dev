// Test to verify the PK collision hypothesis
package metricsd

import (
	"context"
	"testing"
	"time"
)

func TestRollup_PKCollision_DifferentVMID_SameName(t *testing.T) {
	// Two different VMs (different vm_id) that share the same vm_name.
	// The rollup groups by vm_key (vm_id), producing two rows, but the
	// daily table PK is (day_start, vm_name), so the INSERT should fail
	// with a duplicate key violation.
	ctx := context.Background()
	connector, db, _, err := OpenDB(ctx, "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()

	day := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	// Two VMs with different vm_id but same vm_name
	insertRawMetrics(t, srv, []Metric{
		{
			Timestamp:            day.Add(5 * time.Minute),
			Host:                 "host1",
			VMID:                 "id-1",
			VMName:               "shared-name",
			ResourceGroup:        "rg1",
			DiskLogicalUsedBytes: 1_000_000_000,
			DiskUsedBytes:        500_000_000,
			DiskSizeBytes:        10_000_000_000,
			MemoryRSSBytes:       512_000_000,
		},
		{
			Timestamp:            day.Add(10 * time.Minute),
			Host:                 "host1",
			VMID:                 "id-1",
			VMName:               "shared-name",
			ResourceGroup:        "rg1",
			CPUUsedCumulativeSecs: 100,
			NetworkTXBytes:        1000,
			DiskLogicalUsedBytes:  1_000_000_000,
			DiskUsedBytes:         500_000_000,
			DiskSizeBytes:         10_000_000_000,
			MemoryRSSBytes:        512_000_000,
		},
		{
			Timestamp:            day.Add(5 * time.Minute),
			Host:                 "host2",
			VMID:                 "id-2",
			VMName:               "shared-name", // same name, different vm_id
			ResourceGroup:        "rg2",
			DiskLogicalUsedBytes: 2_000_000_000,
			DiskUsedBytes:        1_000_000_000,
			DiskSizeBytes:        20_000_000_000,
			MemoryRSSBytes:       1_000_000_000,
		},
		{
			Timestamp:            day.Add(10 * time.Minute),
			Host:                 "host2",
			VMID:                 "id-2",
			VMName:               "shared-name",
			ResourceGroup:        "rg2",
			CPUUsedCumulativeSecs: 200,
			NetworkTXBytes:        2000,
			DiskLogicalUsedBytes:  2_000_000_000,
			DiskUsedBytes:         1_000_000_000,
			DiskSizeBytes:         20_000_000_000,
			MemoryRSSBytes:        1_000_000_000,
		},
	})

	rollup := NewRollup(db)
	err = rollup.RunOnce(ctx, day.Add(25*time.Hour))
	if err != nil {
		t.Fatalf("RunOnce failed (PK collision): %v", err)
	}

	// We should get 2 daily rows — one per vm_id
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vm_metrics_daily WHERE day_start = ?`, day).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("daily row count = %d, want 2 (one per vm_id)", count)
	}
}
