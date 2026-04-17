package metricsd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"exe.dev/metricsd/types"
)

// insertRawMetrics directly inserts rows into vm_metrics using the server's
// InsertMetrics method.
func insertRawMetrics(t *testing.T, srv *Server, metrics []Metric) {
	t.Helper()
	if err := srv.InsertMetrics(context.Background(), metrics); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
}

func TestRollup_CounterReset(t *testing.T) {
	// Verifies: if a cumulative counter (cpu, network, io) goes DOWN between
	// samples (VM restart), the delta for that sample is treated as the raw
	// current value, not negative.
	ctx := context.Background()
	connector, db, _, err := OpenDB(ctx, "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()

	// Three samples for vm-a in one hour:
	// t0: cpu=100s, net_tx=1000
	// t1: cpu=200s, net_tx=2000  → delta = 100s / 1000
	// t2: cpu=50s,  net_tx=300   → counter reset: delta = 50s / 300 (not negative)
	hour := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	metrics := []Metric{
		{
			Timestamp:             hour.Add(0 * time.Minute),
			Host:                  "host1",
			VMID:                  "vm-a-id",
			VMName:                "vm-a",
			ResourceGroup:         "rg1",
			CPUUsedCumulativeSecs: 100,
			NetworkTXBytes:        1000,
			NetworkRXBytes:        500,
			IOReadBytes:           2000,
			IOWriteBytes:          1000,
			DiskLogicalUsedBytes:  10_000_000,
			DiskUsedBytes:         5_000_000,
			DiskSizeBytes:         20_000_000,
			MemoryRSSBytes:        1_000_000,
			MemorySwapBytes:       0,
		},
		{
			Timestamp:             hour.Add(20 * time.Minute),
			Host:                  "host1",
			VMID:                  "vm-a-id",
			VMName:                "vm-a",
			ResourceGroup:         "rg1",
			CPUUsedCumulativeSecs: 200,
			NetworkTXBytes:        2000,
			NetworkRXBytes:        1000,
			IOReadBytes:           4000,
			IOWriteBytes:          2000,
			DiskLogicalUsedBytes:  10_000_000,
			DiskUsedBytes:         5_000_000,
			DiskSizeBytes:         20_000_000,
			MemoryRSSBytes:        1_200_000,
			MemorySwapBytes:       0,
		},
		{
			Timestamp:     hour.Add(40 * time.Minute),
			Host:          "host1",
			VMID:          "vm-a-id",
			VMName:        "vm-a",
			ResourceGroup: "rg1",
			// Counter reset: values lower than previous sample
			CPUUsedCumulativeSecs: 50,
			NetworkTXBytes:        300,
			NetworkRXBytes:        100,
			IOReadBytes:           800,
			IOWriteBytes:          400,
			DiskLogicalUsedBytes:  10_000_000,
			DiskUsedBytes:         5_000_000,
			DiskSizeBytes:         20_000_000,
			MemoryRSSBytes:        1_100_000,
			MemorySwapBytes:       0,
		},
	}
	insertRawMetrics(t, srv, metrics)

	rollup := NewRollup(db)
	cutoff := hour.Add(24 * time.Hour) // roll up this day
	if err := rollup.RunOnce(ctx, cutoff); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Query daily table
	rows, err := db.QueryContext(ctx, `SELECT cpu_seconds, network_tx_bytes, network_rx_bytes FROM vm_metrics_daily WHERE vm_name = 'vm-a'`)
	if err != nil {
		t.Fatalf("query hourly: %v", err)
	}
	defer rows.Close()

	var found bool
	for rows.Next() {
		found = true
		var cpuDelta float64
		var netTX, netRX int64
		if err := rows.Scan(&cpuDelta, &netTX, &netRX); err != nil {
			t.Fatalf("scan: %v", err)
		}
		// cpu: (200-100) + 50 = 150 (counter reset treated as current value)
		if cpuDelta < 0 {
			t.Errorf("cpu_seconds = %v, want >= 0 (no negative deltas on counter reset)", cpuDelta)
		}
		// network tx: (2000-1000) + 300 = 1300
		if netTX < 0 {
			t.Errorf("network_tx_bytes = %v, want >= 0 (no negative deltas on counter reset)", netTX)
		}
		if netRX < 0 {
			t.Errorf("network_rx_bytes = %v, want >= 0 (no negative deltas on counter reset)", netRX)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if !found {
		t.Fatal("no daily row found for vm-a")
	}
}

func TestRollup_FirstSampleReturnsZero(t *testing.T) {
	// Verifies: when a VM has exactly one sample in an hour,
	// all delta columns (cpu, network, io) return 0, not the raw value.
	ctx := context.Background()
	connector, db, _, err := OpenDB(ctx, "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()

	hour := time.Date(2025, 2, 1, 8, 0, 0, 0, time.UTC)
	metrics := []Metric{
		{
			Timestamp:             hour.Add(5 * time.Minute),
			Host:                  "host1",
			VMID:                  "new-vm-id",
			VMName:                "new-vm",
			ResourceGroup:         "rg1",
			CPUUsedCumulativeSecs: 9999, // big cumulative value - should NOT appear in delta
			NetworkTXBytes:        500_000,
			NetworkRXBytes:        200_000,
			IOReadBytes:           100_000,
			IOWriteBytes:          50_000,
			DiskLogicalUsedBytes:  8_000_000_000,
			DiskUsedBytes:         4_000_000_000,
			DiskSizeBytes:         20_000_000_000,
			MemoryRSSBytes:        2_000_000_000,
			MemorySwapBytes:       0,
		},
	}
	insertRawMetrics(t, srv, metrics)

	rollup := NewRollup(db)
	if err := rollup.RunOnce(ctx, hour.Add(24*time.Hour)); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var cpuDelta float64
	var netTX, netRX, ioRead, ioWrite int64
	err = db.QueryRowContext(ctx,
		`SELECT cpu_seconds, network_tx_bytes, network_rx_bytes, io_read_bytes, io_write_bytes
		 FROM vm_metrics_daily WHERE vm_name = 'new-vm'`,
	).Scan(&cpuDelta, &netTX, &netRX, &ioRead, &ioWrite)
	if err != nil {
		t.Fatalf("query daily row: %v", err)
	}

	// All deltas must be 0 for a single sample.
	if cpuDelta != 0 {
		t.Errorf("cpu_seconds = %v, want 0 for first sample", cpuDelta)
	}
	if netTX != 0 {
		t.Errorf("network_tx_bytes = %v, want 0 for first sample", netTX)
	}
	if netRX != 0 {
		t.Errorf("network_rx_bytes = %v, want 0 for first sample", netRX)
	}
	if ioRead != 0 {
		t.Errorf("io_read_bytes = %v, want 0 for first sample", ioRead)
	}
	if ioWrite != 0 {
		t.Errorf("io_write_bytes = %v, want 0 for first sample", ioWrite)
	}
}

func TestRollup_VMIDRenameContinuity(t *testing.T) {
	// Verifies: when a VM is renamed but keeps its vm_id, the hourly rollup
	// uses vm_id as the partition key so deltas are computed correctly
	// across the rename boundary.
	ctx := context.Background()
	connector, db, _, err := OpenDB(ctx, "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()

	hour := time.Date(2025, 3, 10, 14, 0, 0, 0, time.UTC)
	// Same vm_id, different vm_name between first and second sample.
	// This simulates a rename mid-hour.
	metrics := []Metric{
		{
			Timestamp:             hour.Add(5 * time.Minute),
			Host:                  "host1",
			VMID:                  "stable-id-xyz",
			VMName:                "old-name",
			ResourceGroup:         "rg-rename",
			CPUUsedCumulativeSecs: 1000,
			NetworkTXBytes:        10_000,
			NetworkRXBytes:        5_000,
			IOReadBytes:           20_000,
			IOWriteBytes:          10_000,
			DiskLogicalUsedBytes:  5_000_000_000,
			DiskUsedBytes:         2_500_000_000,
			DiskSizeBytes:         10_000_000_000,
			MemoryRSSBytes:        1_000_000_000,
			MemorySwapBytes:       0,
		},
		{
			Timestamp:             hour.Add(25 * time.Minute),
			Host:                  "host1",
			VMID:                  "stable-id-xyz", // same vm_id
			VMName:                "new-name",      // renamed!
			ResourceGroup:         "rg-rename",
			CPUUsedCumulativeSecs: 1500,   // +500s from previous
			NetworkTXBytes:        15_000, // +5000
			NetworkRXBytes:        8_000,  // +3000
			IOReadBytes:           25_000, // +5000
			IOWriteBytes:          12_000, // +2000
			DiskLogicalUsedBytes:  5_000_000_000,
			DiskUsedBytes:         2_500_000_000,
			DiskSizeBytes:         10_000_000_000,
			MemoryRSSBytes:        1_100_000_000,
			MemorySwapBytes:       0,
		},
	}
	insertRawMetrics(t, srv, metrics)

	rollup := NewRollup(db)
	if err := rollup.RunOnce(ctx, hour.Add(24*time.Hour)); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Expect exactly one daily row (partitioned by vm_id, not vm_name).
	var rowCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vm_metrics_daily WHERE vm_id = 'stable-id-xyz'`).Scan(&rowCount); err != nil {
		t.Fatalf("count daily rows: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("daily row count for stable-id-xyz = %d, want 1 (rename should not split partitions)", rowCount)
	}

	// Delta should reflect the actual change between the two samples.
	var cpuDelta float64
	var netTX int64
	if err := db.QueryRowContext(ctx,
		`SELECT cpu_seconds, network_tx_bytes FROM vm_metrics_daily WHERE vm_id = 'stable-id-xyz'`,
	).Scan(&cpuDelta, &netTX); err != nil {
		t.Fatalf("scan daily row: %v", err)
	}
	// First sample delta = 0, second sample delta = 1500 - 1000 = 500.
	// Total = 500s CPU, 5000 bytes TX.
	if cpuDelta != 500 {
		t.Errorf("cpu_seconds = %v, want 500", cpuDelta)
	}
	if netTX != 5000 {
		t.Errorf("network_tx_bytes = %v, want 5000", netTX)
	}
}

func TestRollup_Idempotent(t *testing.T) {
	// Running rollup twice on the same data should produce the same result.
	ctx := context.Background()
	connector, db, _, err := OpenDB(ctx, "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()

	hour := time.Date(2025, 4, 5, 6, 0, 0, 0, time.UTC)
	metrics := []Metric{
		{
			Timestamp:             hour.Add(0 * time.Minute),
			Host:                  "host1",
			VMID:                  "idem-vm",
			VMName:                "idem-vm",
			ResourceGroup:         "rg-idem",
			CPUUsedCumulativeSecs: 100,
			NetworkTXBytes:        1000,
			NetworkRXBytes:        500,
			IOReadBytes:           2000,
			IOWriteBytes:          1000,
			DiskLogicalUsedBytes:  1_000_000_000,
			DiskUsedBytes:         500_000_000,
			DiskSizeBytes:         5_000_000_000,
			MemoryRSSBytes:        512_000_000,
			MemorySwapBytes:       0,
		},
		{
			Timestamp:             hour.Add(30 * time.Minute),
			Host:                  "host1",
			VMID:                  "idem-vm",
			VMName:                "idem-vm",
			ResourceGroup:         "rg-idem",
			CPUUsedCumulativeSecs: 200,
			NetworkTXBytes:        2000,
			NetworkRXBytes:        1000,
			IOReadBytes:           4000,
			IOWriteBytes:          2000,
			DiskLogicalUsedBytes:  1_000_000_000,
			DiskUsedBytes:         500_000_000,
			DiskSizeBytes:         5_000_000_000,
			MemoryRSSBytes:        600_000_000,
			MemorySwapBytes:       0,
		},
	}
	insertRawMetrics(t, srv, metrics)

	rollup := NewRollup(db)
	cutoff := hour.Add(24 * time.Hour)

	// Run twice.
	if err := rollup.RunOnce(ctx, cutoff); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	if err := rollup.RunOnce(ctx, cutoff); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vm_metrics_daily WHERE vm_name = 'idem-vm'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("daily row count = %d, want 1 (idempotent - no duplicate rows on re-run)", count)
	}
}

func TestRollupAPI_QueryUsage(t *testing.T) {
	// Integration test: insert raw metrics, run rollup, verify /query/usage endpoint.
	ctx := context.Background()
	connector, db, _, err := OpenDB(ctx, "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	day := time.Date(2025, 5, 10, 0, 0, 0, 0, time.UTC)

	// Insert two hours of data for two VMs in the same resource group.
	for h := 0; h < 2; h++ {
		hour := day.Add(time.Duration(h) * time.Hour)
		for _, vm := range []struct{ id, name string }{
			{"vm1-id", "vm1"},
			{"vm2-id", "vm2"},
		} {
			insertRawMetrics(t, srv, []Metric{
				{
					Timestamp:             hour,
					Host:                  "host1",
					VMID:                  vm.id,
					VMName:                vm.name,
					ResourceGroup:         "rg-usage",
					CPUUsedCumulativeSecs: float64(h) * 100,
					NetworkTXBytes:        int64(h) * 1_000_000,
					NetworkRXBytes:        int64(h) * 500_000,
					IOReadBytes:           int64(h) * 2_000_000,
					IOWriteBytes:          int64(h) * 1_000_000,
					DiskLogicalUsedBytes:  10_000_000_000,
					DiskUsedBytes:         5_000_000_000,
					DiskSizeBytes:         20_000_000_000,
					MemoryRSSBytes:        2_000_000_000,
					MemorySwapBytes:       0,
				},
			})
		}
	}

	// Run rollup for this day.
	rollup := NewRollup(db)
	// Cutoff must be past the day boundary to trigger daily rollup.
	if err := rollup.RunOnce(ctx, day.Add(25*time.Hour)); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Call /query/usage.
	reqBody, _ := json.Marshal(types.QueryUsageRequest{
		ResourceGroups: []string{"rg-usage"},
		Start:          day,
		End:            day.Add(48 * time.Hour),
	})
	resp, err := http.Post(ts.URL+"/query/usage", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /query/usage: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result types.QueryUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Metrics) != 1 {
		t.Fatalf("summaries count = %d, want 1", len(result.Metrics))
	}
	if result.Metrics[0].ResourceGroup != "rg-usage" {
		t.Errorf("resource_group = %q, want %q", result.Metrics[0].ResourceGroup, "rg-usage")
	}
	if len(result.Metrics[0].VMs) != 2 {
		t.Errorf("vm count = %d, want 2", len(result.Metrics[0].VMs))
	}
}

func TestRollupAPI_QueryVMsOverLimit(t *testing.T) {
	// Integration test: insert raw metrics, run rollup, verify /query/vms-over-limit.
	ctx := context.Background()
	connector, db, _, err := OpenDB(ctx, "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Use a day within the current month so the /query/vms-over-limit endpoint
	// (which uses current calendar month) picks it up.
	now := time.Now().UTC()
	day := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	// vm-big has disk > 25GB and bandwidth > 100GB (should appear as over-limit)
	// vm-small has disk = 5GB and bandwidth = 10GB (within limits)
	bigDisk := int64(30_000_000_000)  // 30 GB
	smallDisk := int64(5_000_000_000) // 5 GB

	// Insert two samples per VM so LAG produces non-zero deltas.
	insertRawMetrics(t, srv, []Metric{
		// big-vm: first sample (baseline)
		{
			Timestamp:            day.Add(0 * time.Minute),
			Host:                 "host1",
			VMID:                 "big-vm",
			VMName:               "big-vm-name",
			ResourceGroup:        "rg-limit",
			NetworkTXBytes:       10_000_000_000,
			NetworkRXBytes:       5_000_000_000,
			DiskLogicalUsedBytes: bigDisk,
			DiskUsedBytes:        bigDisk / 2,
			DiskSizeBytes:        40_000_000_000,
			MemoryRSSBytes:       1_000_000_000,
		},
		// big-vm: second sample — cumulative counters show +80 GB TX and +50 GB RX = 130 GB total
		{
			Timestamp:            day.Add(30 * time.Minute),
			Host:                 "host1",
			VMID:                 "big-vm",
			VMName:               "big-vm-name",
			ResourceGroup:        "rg-limit",
			NetworkTXBytes:       90_000_000_000, // +80 GB from previous
			NetworkRXBytes:       55_000_000_000, // +50 GB from previous → total 130 GB > 100 GB
			DiskLogicalUsedBytes: bigDisk,
			DiskUsedBytes:        bigDisk / 2,
			DiskSizeBytes:        40_000_000_000,
			MemoryRSSBytes:       1_000_000_000,
		},
		// small-vm: first sample (baseline)
		{
			Timestamp:            day.Add(0 * time.Minute),
			Host:                 "host1",
			VMID:                 "small-vm",
			VMName:               "small-vm-name",
			ResourceGroup:        "rg-limit",
			NetworkTXBytes:       1_000_000_000,
			NetworkRXBytes:       1_000_000_000,
			DiskLogicalUsedBytes: smallDisk,
			DiskUsedBytes:        smallDisk / 2,
			DiskSizeBytes:        10_000_000_000,
			MemoryRSSBytes:       512_000_000,
		},
		// small-vm: second sample — only +5 GB TX / +4 GB RX = 9 GB total, well under 100 GB
		{
			Timestamp:            day.Add(30 * time.Minute),
			Host:                 "host1",
			VMID:                 "small-vm",
			VMName:               "small-vm-name",
			ResourceGroup:        "rg-limit",
			NetworkTXBytes:       6_000_000_000, // +5 GB
			NetworkRXBytes:       5_000_000_000, // +4 GB
			DiskLogicalUsedBytes: smallDisk,
			DiskUsedBytes:        smallDisk / 2,
			DiskSizeBytes:        10_000_000_000,
			MemoryRSSBytes:       512_000_000,
		},
	})

	rollup := NewRollup(db)
	// Cutoff must be past the day boundary to populate vm_metrics_daily.
	if err := rollup.RunOnce(ctx, day.Add(25*time.Hour)); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	reqBody, _ := json.Marshal(types.QueryVMsOverLimitRequest{
		VMIDs:                  []string{"big-vm", "small-vm"},
		DiskIncludedBytes:      25_000_000_000,  // 25 GB
		BandwidthIncludedBytes: 100_000_000_000, // 100 GB
	})
	resp, err := http.Post(ts.URL+"/query/vms-over-limit", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /query/vms-over-limit: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result types.QueryVMsOverLimitResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Only big-vm should be over the limit.
	if len(result.VMs) != 1 {
		t.Fatalf("over-limit VMs = %d, want 1; got %+v", len(result.VMs), result.VMs)
	}
	if result.VMs[0].VMID != "big-vm" {
		t.Errorf("over-limit VM id = %q, want %q", result.VMs[0].VMID, "big-vm")
	}
	if !result.VMs[0].BandwidthOver {
		t.Errorf("bandwidth_over = false, want true for big-vm")
	}
}

func TestRollup_MonthlyGroupsByVMID(t *testing.T) {
	// Verifies: the monthly rollup groups by vm_id, so a VM that was renamed
	// mid-month produces one row (not two).
	ctx := context.Background()
	connector, db, _, err := OpenDB(ctx, "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()

	// Two days in the same month, same vm_id, different vm_name (rename).
	day1 := time.Date(2025, 6, 5, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)

	for _, d := range []struct {
		day  time.Time
		name string
	}{
		{day1, "old-name"},
		{day2, "new-name"},
	} {
		insertRawMetrics(t, srv, []Metric{
			{
				Timestamp:            d.day.Add(5 * time.Minute),
				Host:                 "host1",
				VMID:                 "rename-vm-id",
				VMName:               d.name,
				ResourceGroup:        "rg-monthly",
				DiskLogicalUsedBytes: 10_000_000_000,
				DiskUsedBytes:        5_000_000_000,
				DiskSizeBytes:        20_000_000_000,
				MemoryRSSBytes:       1_000_000_000,
			},
			{
				Timestamp:             d.day.Add(30 * time.Minute),
				Host:                  "host1",
				VMID:                  "rename-vm-id",
				VMName:                d.name,
				ResourceGroup:         "rg-monthly",
				CPUUsedCumulativeSecs: 100,
				NetworkTXBytes:        1000,
				DiskLogicalUsedBytes:  10_000_000_000,
				DiskUsedBytes:         5_000_000_000,
				DiskSizeBytes:         20_000_000_000,
				MemoryRSSBytes:        1_000_000_000,
			},
		})
	}

	rollup := NewRollup(db)
	if err := rollup.RunOnce(ctx, day2.Add(25*time.Hour)); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Monthly table should have exactly 1 row for this vm_id.
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM vm_metrics_monthly WHERE vm_id = 'rename-vm-id'`,
	).Scan(&count); err != nil {
		t.Fatalf("count monthly: %v", err)
	}
	if count != 1 {
		t.Errorf("monthly row count = %d, want 1 (rename should not create duplicate rows)", count)
	}

	// The vm_name should be the latest (new-name).
	var vmName string
	if err := db.QueryRowContext(ctx,
		`SELECT vm_name FROM vm_metrics_monthly WHERE vm_id = 'rename-vm-id'`,
	).Scan(&vmName); err != nil {
		t.Fatalf("query vm_name: %v", err)
	}
	if vmName != "new-name" {
		t.Errorf("vm_name = %q, want %q (should be the latest name)", vmName, "new-name")
	}
}

func TestRollup_MonthlyTwoVMsSameMonth(t *testing.T) {
	// Verifies: two different VMs in the same month produce two separate rows.
	ctx := context.Background()
	connector, db, _, err := OpenDB(ctx, "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()

	day := time.Date(2025, 7, 10, 0, 0, 0, 0, time.UTC)
	for _, vm := range []struct{ id, name string }{
		{"vm-a-id", "vm-a"},
		{"vm-b-id", "vm-b"},
	} {
		insertRawMetrics(t, srv, []Metric{
			{
				Timestamp:            day.Add(5 * time.Minute),
				Host:                 "host1",
				VMID:                 vm.id,
				VMName:               vm.name,
				ResourceGroup:        "rg-two-vms",
				DiskLogicalUsedBytes: 10_000_000_000,
				DiskUsedBytes:        5_000_000_000,
				DiskSizeBytes:        20_000_000_000,
				MemoryRSSBytes:       1_000_000_000,
			},
			{
				Timestamp:             day.Add(30 * time.Minute),
				Host:                  "host1",
				VMID:                  vm.id,
				VMName:                vm.name,
				ResourceGroup:         "rg-two-vms",
				CPUUsedCumulativeSecs: 100,
				NetworkTXBytes:        1000,
				DiskLogicalUsedBytes:  10_000_000_000,
				DiskUsedBytes:         5_000_000_000,
				DiskSizeBytes:         20_000_000_000,
				MemoryRSSBytes:        1_000_000_000,
			},
		})
	}

	rollup := NewRollup(db)
	if err := rollup.RunOnce(ctx, day.Add(25*time.Hour)); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM vm_metrics_monthly WHERE resource_group = 'rg-two-vms'`,
	).Scan(&count); err != nil {
		t.Fatalf("count monthly: %v", err)
	}
	if count != 2 {
		t.Errorf("monthly row count = %d, want 2 (one per VM)", count)
	}
}

func TestRollupAPI_QueryMonthlyPerVM(t *testing.T) {
	// Integration test: insert data for two VMs across two months,
	// run rollup, query /query/monthly with group_by_vm=true,
	// verify per-VM rows are returned.
	ctx := context.Background()
	connector, db, _, err := OpenDB(ctx, "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, month := range []time.Time{
		time.Date(2025, 3, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2025, 4, 10, 0, 0, 0, 0, time.UTC),
	} {
		for _, vm := range []struct{ id, name string }{
			{"mon-vm1", "web"},
			{"mon-vm2", "api"},
		} {
			insertRawMetrics(t, srv, []Metric{
				{
					Timestamp:            month.Add(5 * time.Minute),
					Host:                 "host1",
					VMID:                 vm.id,
					VMName:               vm.name,
					ResourceGroup:        "rg-monthly-api",
					DiskLogicalUsedBytes: 10_000_000_000,
					DiskUsedBytes:        5_000_000_000,
					DiskSizeBytes:        20_000_000_000,
					MemoryRSSBytes:       1_000_000_000,
				},
				{
					Timestamp:             month.Add(30 * time.Minute),
					Host:                  "host1",
					VMID:                  vm.id,
					VMName:                vm.name,
					ResourceGroup:         "rg-monthly-api",
					CPUUsedCumulativeSecs: 100,
					NetworkTXBytes:        5000,
					NetworkRXBytes:        3000,
					DiskLogicalUsedBytes:  10_000_000_000,
					DiskUsedBytes:         5_000_000_000,
					DiskSizeBytes:         20_000_000_000,
					MemoryRSSBytes:        1_000_000_000,
				},
			})
		}
	}

	rollup := NewRollup(db)
	if err := rollup.RunOnce(ctx, time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Query with group_by_vm=true.
	reqBody, _ := json.Marshal(types.QueryMonthlyRequest{
		ResourceGroups: []string{"rg-monthly-api"},
		Start:          time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC),
		End:            time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC),
		GroupByVM:      true,
	})
	resp, err := http.Post(ts.URL+"/query/monthly", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /query/monthly: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result types.QueryMonthlyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 2 VMs × 2 months = 4 rows.
	if len(result.Metrics) != 4 {
		t.Fatalf("monthly metrics count = %d, want 4", len(result.Metrics))
	}

	// Verify each row has a vm_id.
	vmIDs := map[string]int{}
	for _, m := range result.Metrics {
		if m.VMID == "" {
			t.Error("expected non-empty vm_id in grouped monthly response")
		}
		vmIDs[m.VMID]++
	}
	if vmIDs["mon-vm1"] != 2 {
		t.Errorf("mon-vm1 rows = %d, want 2", vmIDs["mon-vm1"])
	}
	if vmIDs["mon-vm2"] != 2 {
		t.Errorf("mon-vm2 rows = %d, want 2", vmIDs["mon-vm2"])
	}
}

func TestRollupAPI_QueryUsageGroupsByVMID(t *testing.T) {
	// Verifies: /query/usage groups per-VM stats by vm_id. A renamed VM
	// should appear as one entry, not two.
	ctx := context.Background()
	connector, db, _, err := OpenDB(ctx, "", "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Same vm_id, two different names across two days.
	day1 := time.Date(2025, 8, 5, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2025, 8, 6, 0, 0, 0, 0, time.UTC)

	for _, d := range []struct {
		day  time.Time
		name string
	}{
		{day1, "before-rename"},
		{day2, "after-rename"},
	} {
		insertRawMetrics(t, srv, []Metric{
			{
				Timestamp:            d.day.Add(5 * time.Minute),
				Host:                 "host1",
				VMID:                 "usage-rename-id",
				VMName:               d.name,
				ResourceGroup:        "rg-usage-rename",
				DiskLogicalUsedBytes: 10_000_000_000,
				DiskUsedBytes:        5_000_000_000,
				DiskSizeBytes:        20_000_000_000,
				MemoryRSSBytes:       1_000_000_000,
			},
			{
				Timestamp:             d.day.Add(30 * time.Minute),
				Host:                  "host1",
				VMID:                  "usage-rename-id",
				VMName:                d.name,
				ResourceGroup:         "rg-usage-rename",
				CPUUsedCumulativeSecs: 100,
				NetworkTXBytes:        1000,
				DiskLogicalUsedBytes:  10_000_000_000,
				DiskUsedBytes:         5_000_000_000,
				DiskSizeBytes:         20_000_000_000,
				MemoryRSSBytes:        1_000_000_000,
			},
		})
	}

	rollup := NewRollup(db)
	if err := rollup.RunOnce(ctx, day2.Add(25*time.Hour)); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	reqBody, _ := json.Marshal(types.QueryUsageRequest{
		ResourceGroups: []string{"rg-usage-rename"},
		Start:          day1,
		End:            day2.Add(48 * time.Hour),
	})
	resp, err := http.Post(ts.URL+"/query/usage", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /query/usage: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result types.QueryUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Metrics) != 1 {
		t.Fatalf("summaries = %d, want 1", len(result.Metrics))
	}
	// Should be exactly 1 VM entry (not 2 from the rename).
	if len(result.Metrics[0].VMs) != 1 {
		t.Fatalf("VM count = %d, want 1 (renamed VM should not produce duplicates)", len(result.Metrics[0].VMs))
	}
	vm := result.Metrics[0].VMs[0]
	if vm.VMID != "usage-rename-id" {
		t.Errorf("vm_id = %q, want %q", vm.VMID, "usage-rename-id")
	}
	// Should have the latest name.
	if vm.VMName != "after-rename" {
		t.Errorf("vm_name = %q, want %q", vm.VMName, "after-rename")
	}
}
