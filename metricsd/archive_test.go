package metricsd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiver_BasicFlow(t *testing.T) {
	ctx := context.Background()
	archiveDir := t.TempDir()

	connector, db, archiver, err := OpenDB(ctx, "", archiveDir)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()

	// Insert data spanning 5 days: 3 days ago, 4 days ago, 5 days ago, plus today and yesterday.
	now := time.Now().UTC().Truncate(time.Microsecond)
	metrics := []Metric{
		{Timestamp: now.Add(-5 * 24 * time.Hour), Host: "h1", VMName: "vm-old-5", DiskSizeBytes: 100, ResourceGroup: "grp"},
		{Timestamp: now.Add(-4 * 24 * time.Hour), Host: "h1", VMName: "vm-old-4", DiskSizeBytes: 200, ResourceGroup: "grp"},
		{Timestamp: now.Add(-3 * 24 * time.Hour), Host: "h1", VMName: "vm-old-3", DiskSizeBytes: 300, ResourceGroup: "grp"},
		{Timestamp: now.Add(-1 * 24 * time.Hour), Host: "h1", VMName: "vm-yesterday", DiskSizeBytes: 400, ResourceGroup: "grp"},
		{Timestamp: now, Host: "h1", VMName: "vm-today", DiskSizeBytes: 500, ResourceGroup: "grp"},
	}
	if err := srv.InsertMetrics(ctx, metrics); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	// Verify all 5 rows visible via view.
	var total int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM vm_metrics_all").Scan(&total); err != nil {
		t.Fatalf("count all: %v", err)
	}
	if total != 5 {
		t.Fatalf("expected 5 rows before archive, got %d", total)
	}

	// Run archival with 2-day cutoff.
	cutoff := now.Add(-48 * time.Hour)
	if err := archiver.RunOnce(ctx, cutoff); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Check that parquet files were created for the 3 old days.
	entries, err := archiver.ArchivedDays(ctx)
	if err != nil {
		t.Fatalf("ArchivedDays: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 archived days, got %d", len(entries))
	}
	for _, e := range entries {
		if _, err := os.Stat(e.FilePath); err != nil {
			t.Errorf("parquet file missing: %s", e.FilePath)
		}
		if e.RowCount != 1 {
			t.Errorf("expected 1 row for day %s, got %d", e.Day.Format("2006-01-02"), e.RowCount)
		}
	}

	// Check that the duckdb table only has the recent 2 rows.
	var tableCount int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM vm_metrics").Scan(&tableCount); err != nil {
		t.Fatalf("count table: %v", err)
	}
	if tableCount != 2 {
		t.Errorf("expected 2 rows in vm_metrics table, got %d", tableCount)
	}

	// But the view still shows all 5.
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM vm_metrics_all").Scan(&total); err != nil {
		t.Fatalf("count all after archive: %v", err)
	}
	if total != 5 {
		t.Errorf("expected 5 rows via view after archive, got %d", total)
	}

	// Query specific VMs to test filtering works across the view.
	var oldCount int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM vm_metrics_all WHERE vm_name = 'vm-old-5'").Scan(&oldCount); err != nil {
		t.Fatalf("query old vm: %v", err)
	}
	if oldCount != 1 {
		t.Errorf("expected 1 row for vm-old-5, got %d", oldCount)
	}

	var todayCount int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM vm_metrics_all WHERE vm_name = 'vm-today'").Scan(&todayCount); err != nil {
		t.Fatalf("query today vm: %v", err)
	}
	if todayCount != 1 {
		t.Errorf("expected 1 row for vm-today, got %d", todayCount)
	}
}

func TestArchiver_Idempotent(t *testing.T) {
	ctx := context.Background()
	archiveDir := t.TempDir()

	connector, db, archiver, err := OpenDB(ctx, "", archiveDir)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()

	now := time.Now().UTC().Truncate(time.Microsecond)
	metrics := []Metric{
		{Timestamp: now.Add(-5 * 24 * time.Hour), Host: "h1", VMName: "vm-a", DiskSizeBytes: 100, ResourceGroup: "grp"},
		{Timestamp: now, Host: "h1", VMName: "vm-b", DiskSizeBytes: 200, ResourceGroup: "grp"},
	}
	if err := srv.InsertMetrics(ctx, metrics); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	cutoff := now.Add(-48 * time.Hour)

	// Run twice.
	if err := archiver.RunOnce(ctx, cutoff); err != nil {
		t.Fatalf("RunOnce 1: %v", err)
	}
	if err := archiver.RunOnce(ctx, cutoff); err != nil {
		t.Fatalf("RunOnce 2: %v", err)
	}

	// Should still have exactly 1 archive entry.
	entries, err := archiver.ArchivedDays(ctx)
	if err != nil {
		t.Fatalf("ArchivedDays: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 archived day, got %d", len(entries))
	}

	// View should show both rows.
	var total int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM vm_metrics_all").Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2 {
		t.Errorf("expected 2 rows, got %d", total)
	}
}

func TestArchiver_NoDataToArchive(t *testing.T) {
	ctx := context.Background()
	archiveDir := t.TempDir()

	connector, db, archiver, err := OpenDB(ctx, "", archiveDir)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	// Run with no data.
	cutoff := time.Now().UTC().Add(-48 * time.Hour)
	if err := archiver.RunOnce(ctx, cutoff); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	entries, err := archiver.ArchivedDays(ctx)
	if err != nil {
		t.Fatalf("ArchivedDays: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 archived days, got %d", len(entries))
	}
}

func TestArchiver_MissingParquetFile(t *testing.T) {
	ctx := context.Background()
	archiveDir := t.TempDir()

	connector, db, archiver, err := OpenDB(ctx, "", archiveDir)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()

	now := time.Now().UTC().Truncate(time.Microsecond)
	metrics := []Metric{
		{Timestamp: now.Add(-5 * 24 * time.Hour), Host: "h1", VMName: "vm-a", DiskSizeBytes: 100, ResourceGroup: "grp"},
	}
	if err := srv.InsertMetrics(ctx, metrics); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	cutoff := now.Add(-48 * time.Hour)
	if err := archiver.RunOnce(ctx, cutoff); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Delete the parquet file.
	entries, _ := archiver.ArchivedDays(ctx)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	os.Remove(entries[0].FilePath)

	// RebuildView should not fail; it skips missing files.
	if err := archiver.RebuildView(ctx); err != nil {
		t.Fatalf("RebuildView with missing file: %v", err)
	}

	// The data from the deleted parquet is gone (1 row lost), but the view still works.
	var total int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM vm_metrics_all").Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 0 {
		t.Errorf("expected 0 rows (parquet deleted, table cleaned), got %d", total)
	}
}

func TestArchiver_WithOnDiskDB(t *testing.T) {
	// Test with an actual on-disk duckdb file to simulate real deployment.
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.duckdb")
	archiveDir := filepath.Join(tmpDir, "archive")

	connector, db, archiver, err := OpenDB(ctx, dbPath, archiveDir)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()

	// Anchor at noon UTC so rows don't spill across day boundaries
	// regardless of when the test runs.
	now := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	var metrics []Metric
	// Insert 100 rows per day for 5 old days.
	for d := 3; d <= 7; d++ {
		for i := 0; i < 100; i++ {
			metrics = append(metrics, Metric{
				Timestamp:     now.Add(-time.Duration(d) * 24 * time.Hour).Add(time.Duration(i) * time.Minute),
				Host:          "h1",
				VMName:        "vm-test",
				DiskSizeBytes: int64(d * 100),
				ResourceGroup: "grp",
			})
		}
	}
	// Plus some recent data.
	for i := 0; i < 50; i++ {
		metrics = append(metrics, Metric{
			Timestamp:     now.Add(-time.Duration(i) * time.Minute),
			Host:          "h1",
			VMName:        "vm-test",
			DiskSizeBytes: 999,
			ResourceGroup: "grp",
		})
	}

	if err := srv.InsertMetrics(ctx, metrics); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	// Total = 500 old + 50 recent = 550.
	var total int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM vm_metrics_all").Scan(&total); err != nil {
		t.Fatalf("count before: %v", err)
	}
	if total != 550 {
		t.Fatalf("expected 550 rows, got %d", total)
	}

	cutoff := now.Add(-48 * time.Hour)
	if err := archiver.RunOnce(ctx, cutoff); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// After archival, view should still show 550.
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM vm_metrics_all").Scan(&total); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if total != 550 {
		t.Errorf("expected 550 rows via view, got %d", total)
	}

	// Table should only have ~50 recent rows.
	var tableCount int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM vm_metrics").Scan(&tableCount); err != nil {
		t.Fatalf("count table: %v", err)
	}
	if tableCount != 50 {
		t.Errorf("expected 50 rows in table, got %d", tableCount)
	}

	// Should have 5 parquet files.
	files, _ := filepath.Glob(filepath.Join(archiveDir, "*.parquet"))
	if len(files) != 5 {
		t.Errorf("expected 5 parquet files, got %d", len(files))
	}

	// Verify time-range query works across the boundary.
	var rangeCount int
	err = db.QueryRowContext(ctx,
		"SELECT count(*) FROM vm_metrics_all WHERE timestamp >= ? AND timestamp < ?",
		now.Add(-4*24*time.Hour), now.Add(-2*24*time.Hour),
	).Scan(&rangeCount)
	if err != nil {
		t.Fatalf("range query: %v", err)
	}
	if rangeCount != 200 {
		t.Errorf("expected 200 rows in range, got %d", rangeCount)
	}
}

func TestArchiver_ReopenDB(t *testing.T) {
	// Simulate what happens on restart: archive, close, reopen.
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.duckdb")
	archiveDir := filepath.Join(tmpDir, "archive")

	// First open: insert data and archive.
	connector, db, archiver, err := OpenDB(ctx, dbPath, archiveDir)
	if err != nil {
		t.Fatalf("OpenDB 1: %v", err)
	}

	srv := NewServer(connector, db, false)

	now := time.Now().UTC().Truncate(time.Microsecond)
	metrics := []Metric{
		{Timestamp: now.Add(-5 * 24 * time.Hour), Host: "h1", VMName: "vm-old", DiskSizeBytes: 100, ResourceGroup: "grp"},
		{Timestamp: now, Host: "h1", VMName: "vm-new", DiskSizeBytes: 200, ResourceGroup: "grp"},
	}
	if err := srv.InsertMetrics(ctx, metrics); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	cutoff := now.Add(-48 * time.Hour)
	if err := archiver.RunOnce(ctx, cutoff); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	srv.Close()
	db.Close()
	connector.Close()

	// Second open: should rebuild view from archive_log.
	connector2, db2, _, err := OpenDB(ctx, dbPath, archiveDir)
	if err != nil {
		t.Fatalf("OpenDB 2: %v", err)
	}
	defer db2.Close()
	defer connector2.Close()

	var total int
	if err := db2.QueryRowContext(ctx, "SELECT count(*) FROM vm_metrics_all").Scan(&total); err != nil {
		t.Fatalf("count after reopen: %v", err)
	}
	if total != 2 {
		t.Errorf("expected 2 rows via view after reopen, got %d", total)
	}
}
