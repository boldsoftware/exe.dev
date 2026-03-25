package metricsd

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Archiver handles rolling old metrics data from DuckDB into parquet files.
type Archiver struct {
	db         *sql.DB
	archiveDir string
}

// NewArchiver creates an archiver that stores parquet files in archiveDir.
func NewArchiver(db *sql.DB, archiveDir string) *Archiver {
	return &Archiver{db: db, archiveDir: archiveDir}
}

// archiveEntry represents a single archived day.
type archiveEntry struct {
	Day      time.Time
	FilePath string
	RowCount int64
}

// ArchivedDays returns all days that have been archived, sorted ascending.
func (a *Archiver) ArchivedDays(ctx context.Context) ([]archiveEntry, error) {
	rows, err := a.db.QueryContext(ctx, "SELECT day, file_path, row_count FROM archive_log ORDER BY day ASC")
	if err != nil {
		return nil, fmt.Errorf("query archive_log: %w", err)
	}
	defer rows.Close()

	var entries []archiveEntry
	for rows.Next() {
		var e archiveEntry
		if err := rows.Scan(&e.Day, &e.FilePath, &e.RowCount); err != nil {
			return nil, fmt.Errorf("scan archive_log: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// parquetPathForDay returns the path for a day's parquet file.
func (a *Archiver) parquetPathForDay(day time.Time) string {
	return filepath.Join(a.archiveDir, fmt.Sprintf("vm_metrics_%s.parquet", day.Format("2006-01-02")))
}

// RunOnce archives all complete days older than the cutoff.
// A "complete day" is one where the entire UTC day is before the cutoff.
// cutoff is typically now() - 2 days, truncated to day boundary.
func (a *Archiver) RunOnce(ctx context.Context, cutoff time.Time) error {
	cutoff = cutoff.UTC().Truncate(24 * time.Hour)

	if err := os.MkdirAll(a.archiveDir, 0o755); err != nil {
		return fmt.Errorf("create archive dir: %w", err)
	}

	// Find the oldest and newest day in the duckdb table that are before the cutoff.
	var minDay, maxDay sql.NullTime
	err := a.db.QueryRowContext(ctx,
		"SELECT min(date_trunc('day', timestamp)), max(date_trunc('day', timestamp)) FROM vm_metrics WHERE timestamp < ?",
		cutoff,
	).Scan(&minDay, &maxDay)
	if err != nil {
		return fmt.Errorf("find date range: %w", err)
	}
	if !minDay.Valid || !maxDay.Valid {
		slog.InfoContext(ctx, "no data to archive before cutoff", "cutoff", cutoff)
		return nil
	}

	// Get already-archived days.
	archived, err := a.ArchivedDays(ctx)
	if err != nil {
		return err
	}
	archivedSet := make(map[string]bool, len(archived))
	for _, e := range archived {
		archivedSet[e.Day.Format("2006-01-02")] = true
	}

	// Iterate day by day.
	for day := minDay.Time.UTC().Truncate(24 * time.Hour); !day.After(maxDay.Time.UTC().Truncate(24 * time.Hour)); day = day.Add(24 * time.Hour) {
		dayStr := day.Format("2006-01-02")
		if archivedSet[dayStr] {
			// Already archived; just delete the data from duckdb.
			if err := a.deleteDay(ctx, day); err != nil {
				return fmt.Errorf("delete already-archived day %s: %w", dayStr, err)
			}
			continue
		}

		if err := a.archiveDay(ctx, day); err != nil {
			return fmt.Errorf("archive day %s: %w", dayStr, err)
		}
	}

	// Rebuild the view.
	return a.RebuildView(ctx)
}

// archiveDay exports a single day's data to parquet, records it, and deletes from duckdb.
func (a *Archiver) archiveDay(ctx context.Context, day time.Time) error {
	nextDay := day.Add(24 * time.Hour)
	path := a.parquetPathForDay(day)
	dayStr := day.Format("2006-01-02")

	// Count rows first.
	var rowCount int64
	err := a.db.QueryRowContext(ctx,
		"SELECT count(*) FROM vm_metrics WHERE timestamp >= ? AND timestamp < ?",
		day, nextDay,
	).Scan(&rowCount)
	if err != nil {
		return fmt.Errorf("count rows: %w", err)
	}
	if rowCount == 0 {
		slog.InfoContext(ctx, "no rows for day, skipping", "day", dayStr)
		return nil
	}

	// Export to parquet (write to temp file first, then rename for atomicity).
	tmpPath := path + ".tmp"
	exportSQL := fmt.Sprintf(
		"COPY (SELECT * FROM vm_metrics WHERE timestamp >= '%s' AND timestamp < '%s' ORDER BY vm_name, timestamp) TO '%s' (FORMAT PARQUET)",
		day.Format("2006-01-02"), nextDay.Format("2006-01-02"), tmpPath,
	)
	if _, err := a.db.ExecContext(ctx, exportSQL); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("export parquet: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename parquet: %w", err)
	}

	// Record in archive_log.
	_, err = a.db.ExecContext(ctx,
		"INSERT INTO archive_log (day, file_path, row_count) VALUES (?, ?, ?)",
		day, path, rowCount,
	)
	if err != nil {
		return fmt.Errorf("record archive: %w", err)
	}

	// Delete from duckdb.
	if err := a.deleteDay(ctx, day); err != nil {
		return fmt.Errorf("delete day: %w", err)
	}

	slog.InfoContext(ctx, "archived day", "day", dayStr, "rows", rowCount, "path", path)
	return nil
}

// deleteDay removes a day's data from the duckdb table.
func (a *Archiver) deleteDay(ctx context.Context, day time.Time) error {
	nextDay := day.Add(24 * time.Hour)
	_, err := a.db.ExecContext(ctx,
		"DELETE FROM vm_metrics WHERE timestamp >= ? AND timestamp < ?",
		day, nextDay,
	)
	return err
}

// RebuildView creates or replaces the vm_metrics_all view that unions the
// duckdb table with all archived parquet files.
func (a *Archiver) RebuildView(ctx context.Context) error {
	entries, err := a.ArchivedDays(ctx)
	if err != nil {
		return err
	}

	// Drop the old view first.
	if _, err := a.db.ExecContext(ctx, "DROP VIEW IF EXISTS vm_metrics_all"); err != nil {
		return fmt.Errorf("drop view: %w", err)
	}

	if len(entries) == 0 {
		// No archives yet - view is just the table.
		_, err := a.db.ExecContext(ctx, "CREATE VIEW vm_metrics_all AS SELECT * FROM vm_metrics")
		if err != nil {
			return fmt.Errorf("create view (no archives): %w", err)
		}
		return nil
	}

	// Validate that all parquet files exist. If one is missing, skip it
	// (it might have been manually removed).
	var validPaths []string
	for _, e := range entries {
		if _, err := os.Stat(e.FilePath); err == nil {
			validPaths = append(validPaths, e.FilePath)
		} else {
			slog.WarnContext(ctx, "archived parquet file missing, skipping", "path", e.FilePath)
		}
	}

	sort.Strings(validPaths)

	var viewSQL strings.Builder
	viewSQL.WriteString("CREATE VIEW vm_metrics_all AS\nSELECT * FROM vm_metrics\n")
	if len(validPaths) > 0 {
		viewSQL.WriteString("UNION ALL\nSELECT * FROM read_parquet([")
		for i, p := range validPaths {
			if i > 0 {
				viewSQL.WriteString(", ")
			}
			viewSQL.WriteString(fmt.Sprintf("'%s'", p))
		}
		viewSQL.WriteString("])")
	}

	if _, err := a.db.ExecContext(ctx, viewSQL.String()); err != nil {
		return fmt.Errorf("create view: %w", err)
	}

	slog.InfoContext(ctx, "rebuilt vm_metrics_all view", "parquet_files", len(validPaths))
	return nil
}

// RunPeriodic starts a goroutine that runs archival every interval.
func (a *Archiver) RunPeriodic(ctx context.Context, interval time.Duration) {
	go func() {
		// Run once at startup.
		cutoff := time.Now().UTC().Add(-48 * time.Hour)
		if err := a.RunOnce(ctx, cutoff); err != nil {
			slog.ErrorContext(ctx, "archival failed", "error", err)
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cutoff := time.Now().UTC().Add(-48 * time.Hour)
				if err := a.RunOnce(ctx, cutoff); err != nil {
					slog.ErrorContext(ctx, "archival failed", "error", err)
				}
			}
		}
	}()
}
