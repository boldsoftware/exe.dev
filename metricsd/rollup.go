package metricsd

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// Rollup incrementally maintains daily and monthly aggregation tables from raw
// vm_metrics. The daily rollup processes data one day at a time to bound memory.
type Rollup struct {
	db      *sql.DB
	stopped chan bool
}

// NewRollup creates a Rollup backed by db.
func NewRollup(db *sql.DB) *Rollup {
	return &Rollup{
		db:      db,
		stopped: make(chan bool),
	}
}

// RunOnce runs an incremental rollup cycle.
// cutoff is the upper bound (exclusive) — typically now() truncated to the hour.
func (r *Rollup) RunOnce(ctx context.Context, cutoff time.Time) error {
	cutoff = cutoff.UTC().Truncate(time.Hour)

	if err := r.rollupDaily(ctx, cutoff); err != nil {
		return fmt.Errorf("rollup daily: %w", err)
	}
	if err := r.rollupMonthly(ctx); err != nil {
		return fmt.Errorf("rollup monthly: %w", err)
	}
	return nil
}

// dayInsertSQL is the per-day rollup query. It takes 4 params:
//
//	?1 = lookback start (day - 15min, for LAG seeding)
//	?2 = day end
//	?3 = day start (for filtering lookback rows out of INSERT)
//	?4 = day end   (same as ?2, for the WHERE filter)
const dayInsertSQL = `
INSERT INTO vm_metrics_daily
WITH windowed AS (
    SELECT
        vm_key,
        timestamp,
        host,
        vm_id,
        vm_name,
        resource_group,
        disk_logical_used_bytes,
        disk_used_bytes,
        disk_size_bytes,
        memory_rss_bytes,
        memory_swap_bytes,
        GREATEST(0, network_tx_bytes - COALESCE(LAG(network_tx_bytes) OVER w, network_tx_bytes))                                  AS tx_delta,
        GREATEST(0, network_rx_bytes - COALESCE(LAG(network_rx_bytes) OVER w, network_rx_bytes))                                  AS rx_delta,
        GREATEST(0, cpu_used_cumulative_seconds - COALESCE(LAG(cpu_used_cumulative_seconds) OVER w, cpu_used_cumulative_seconds)) AS cpu_delta,
        GREATEST(0, io_read_bytes  - COALESCE(LAG(io_read_bytes)  OVER w, io_read_bytes))                                        AS io_read_delta,
        GREATEST(0, io_write_bytes - COALESCE(LAG(io_write_bytes) OVER w, io_write_bytes))                                       AS io_write_delta
    FROM (
        SELECT *, COALESCE(NULLIF(vm_id,''), vm_name) AS vm_key
        FROM vm_metrics
        WHERE timestamp >= ? AND timestamp < ?
    ) raw
    WINDOW w AS (PARTITION BY vm_key ORDER BY timestamp ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)
)
SELECT
    CAST(date_trunc('day', timestamp) AS DATE)           AS day_start,
    LAST(host ORDER BY timestamp)                        AS host,
    COALESCE(LAST(vm_id ORDER BY timestamp), '')         AS vm_id,
    LAST(vm_name ORDER BY timestamp)                     AS vm_name,
    LAST(resource_group ORDER BY timestamp)              AS resource_group,
    AVG(disk_logical_used_bytes)::BIGINT                 AS disk_logical_avg_bytes,
    MAX(disk_logical_used_bytes)                          AS disk_logical_max_bytes,
    AVG(disk_used_bytes)::BIGINT                         AS disk_compressed_avg_bytes,
    MAX(disk_size_bytes)                                  AS disk_provisioned_max_bytes,
    SUM(tx_delta)                                         AS network_tx_bytes,
    SUM(rx_delta)                                         AS network_rx_bytes,
    SUM(cpu_delta)                                        AS cpu_seconds,
    SUM(io_read_delta)                                    AS io_read_bytes,
    SUM(io_write_delta)                                   AS io_write_bytes,
    MAX(memory_rss_bytes)                                 AS memory_rss_max_bytes,
    MAX(memory_swap_bytes)                                AS memory_swap_max_bytes,
    COUNT(*)                                              AS hours_with_data
FROM windowed
WHERE timestamp >= ? AND timestamp < ?
GROUP BY date_trunc('day', timestamp), vm_key
`

// rollupDaily computes daily aggregates from raw vm_metrics, one day at a time.
// Uses a high-water mark from vm_metrics_daily; re-processes the last day
// (may have been partial). Each day includes a 15-minute lookback to seed LAG().
func (r *Rollup) rollupDaily(ctx context.Context, cutoff time.Time) error {
	// Find watermark: start of the last daily row (re-process it, may be partial).
	// If no daily rows exist, find the earliest raw data.
	var watermark time.Time
	var maxDay sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT MAX(day_start) FROM vm_metrics_daily`).Scan(&maxDay)
	if err != nil {
		return fmt.Errorf("query max day: %w", err)
	}
	if maxDay.Valid {
		// Re-process from the last day (it may have been partial).
		watermark = maxDay.Time.UTC().Truncate(24 * time.Hour)
	} else {
		var minTS sql.NullTime
		err := r.db.QueryRowContext(ctx, `SELECT MIN(timestamp) FROM vm_metrics`).Scan(&minTS)
		if err != nil {
			return fmt.Errorf("query min timestamp: %w", err)
		}
		if !minTS.Valid {
			slog.InfoContext(ctx, "no raw data to roll up")
			return nil
		}
		watermark = minTS.Time.UTC().Truncate(24 * time.Hour)
	}

	// Truncate cutoff to day boundary.
	cutoffDay := cutoff.UTC().Truncate(24 * time.Hour)
	if !watermark.Before(cutoffDay) {
		return nil
	}

	start := time.Now()
	daysProcessed := 0

	for day := watermark; day.Before(cutoffDay); day = day.Add(24 * time.Hour) {
		dayEnd := day.Add(24 * time.Hour)
		lookback := day.Add(-15 * time.Minute)

		// Delete this day (idempotent re-process).
		_, err := r.db.ExecContext(ctx, `DELETE FROM vm_metrics_daily WHERE day_start = ?`, day)
		if err != nil {
			return fmt.Errorf("delete day %s: %w", day.Format("2006-01-02"), err)
		}

		// Insert with lookback for LAG seeding.
		_, err = r.db.ExecContext(ctx, dayInsertSQL, lookback, dayEnd, day, dayEnd)
		if err != nil {
			return fmt.Errorf("insert day %s: %w", day.Format("2006-01-02"), err)
		}

		daysProcessed++
	}

	var count int
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vm_metrics_daily`).Scan(&count)
	slog.InfoContext(ctx, "daily rollup complete",
		"from", watermark.Format("2006-01-02"),
		"to", cutoffDay.Format("2006-01-02"),
		"days_processed", daysProcessed,
		"total_rows", count,
		"elapsed", time.Since(start).Round(time.Millisecond))
	return nil
}

// rollupMonthly rebuilds vm_metrics_monthly from vm_metrics_daily.
// Uses CTAS since monthly is small and fast to rebuild.
func (r *Rollup) rollupMonthly(ctx context.Context) error {
	const rebuildSQL = `
CREATE OR REPLACE TABLE vm_metrics_monthly AS
SELECT
    date_trunc('month', day_start)::DATE                AS month_start,
    LAST(host ORDER BY day_start)                       AS host,
    COALESCE(LAST(vm_id ORDER BY day_start), '')        AS vm_id,
    LAST(vm_name ORDER BY day_start)                    AS vm_name,
    LAST(resource_group ORDER BY day_start)             AS resource_group,
    AVG(disk_logical_avg_bytes)::BIGINT                 AS disk_logical_avg_bytes,
    MAX(disk_logical_max_bytes)                         AS disk_logical_max_bytes,
    AVG(disk_compressed_avg_bytes)::BIGINT              AS disk_compressed_avg_bytes,
    MAX(disk_provisioned_max_bytes)                     AS disk_provisioned_max_bytes,
    SUM(network_tx_bytes)                               AS network_tx_bytes,
    SUM(network_rx_bytes)                               AS network_rx_bytes,
    SUM(cpu_seconds)                                    AS cpu_seconds,
    SUM(io_read_bytes)                                  AS io_read_bytes,
    SUM(io_write_bytes)                                 AS io_write_bytes,
    MAX(memory_rss_max_bytes)                           AS memory_rss_max_bytes,
    MAX(memory_swap_max_bytes)                          AS memory_swap_max_bytes,
    COUNT(*)                                            AS days_with_data
FROM vm_metrics_daily
GROUP BY date_trunc('month', day_start), COALESCE(NULLIF(vm_id,''), vm_name)
`
	start := time.Now()
	_, err := r.db.ExecContext(ctx, rebuildSQL)
	if err != nil {
		return fmt.Errorf("rebuild monthly: %w", err)
	}
	var count int
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vm_metrics_monthly`).Scan(&count)
	slog.InfoContext(ctx, "monthly rollup rebuilt", "rows", count, "elapsed", time.Since(start).Round(time.Millisecond))
	return nil
}

// RunPeriodic starts a goroutine that runs rollups every interval.
func (r *Rollup) RunPeriodic(ctx context.Context, interval time.Duration) {
	go func() {
		defer close(r.stopped)

		// Run once at startup to catch up.
		cutoff := time.Now().UTC().Truncate(time.Hour)
		if err := r.RunOnce(ctx, cutoff); err != nil {
			slog.ErrorContext(ctx, "rollup startup failed", "error", err)
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cutoff := time.Now().UTC().Truncate(time.Hour)
				if err := r.RunOnce(ctx, cutoff); err != nil {
					slog.ErrorContext(ctx, "rollup failed", "error", err)
				}
			}
		}
	}()
}

// WaitUntilStopped waits until the Rollup goroutine has stopped.
func (r *Rollup) WaitUntilStopped() {
	<-r.stopped
}
