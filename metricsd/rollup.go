package metricsd

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// Rollup rebuilds hourly, daily, and monthly aggregation tables from raw
// vm_metrics using CREATE OR REPLACE TABLE ... AS SELECT. Each run is a full
// rebuild so the tables are always consistent with the raw data; no
// incremental state or idempotency log is required.
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

// RunOnce rebuilds vm_metrics_hourly, vm_metrics_daily, and vm_metrics_monthly
// from all raw data in vm_metrics_all up to cutoff.
// cutoff is typically now() truncated to the current hour.
func (r *Rollup) RunOnce(ctx context.Context, cutoff time.Time) error {
	cutoff = cutoff.UTC().Truncate(time.Hour)

	if err := r.rebuildHourly(ctx, cutoff); err != nil {
		return fmt.Errorf("rebuild hourly: %w", err)
	}
	if err := r.rebuildDaily(ctx); err != nil {
		return fmt.Errorf("rebuild daily: %w", err)
	}
	if err := r.rebuildMonthly(ctx); err != nil {
		return fmt.Errorf("rebuild monthly: %w", err)
	}
	return nil
}

// rebuildHourly replaces vm_metrics_hourly with a full recomputation from
// vm_metrics_all. Uses a CTE+LAG pattern to compute per-sample deltas for
// cumulative counters (CPU, network, IO). GREATEST(0, delta) handles counter
// resets (e.g. VM restarts). The partition key is COALESCE(NULLIF(vm_id,”),
// vm_name) for stability across renames.
func (r *Rollup) rebuildHourly(ctx context.Context, cutoff time.Time) error {
	const rebuildSQL = `
CREATE OR REPLACE TABLE vm_metrics_hourly AS
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
        GREATEST(0, network_tx_bytes - COALESCE(LAG(network_tx_bytes) OVER w, network_tx_bytes))                           AS tx_delta,
        GREATEST(0, network_rx_bytes - COALESCE(LAG(network_rx_bytes) OVER w, network_rx_bytes))                           AS rx_delta,
        GREATEST(0, cpu_used_cumulative_seconds - COALESCE(LAG(cpu_used_cumulative_seconds) OVER w, cpu_used_cumulative_seconds)) AS cpu_delta,
        GREATEST(0, io_read_bytes  - COALESCE(LAG(io_read_bytes)  OVER w, io_read_bytes))                                  AS io_read_delta,
        GREATEST(0, io_write_bytes - COALESCE(LAG(io_write_bytes) OVER w, io_write_bytes))                                 AS io_write_delta
    FROM (
        SELECT *, COALESCE(NULLIF(vm_id,''), vm_name) AS vm_key
        FROM vm_metrics_all
        WHERE timestamp < ?
    ) raw
    WINDOW w AS (PARTITION BY vm_key ORDER BY timestamp ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)
)
SELECT
    date_trunc('hour', timestamp)::TIMESTAMPTZ          AS hour_start,
    CAST(date_trunc('hour', timestamp) AS DATE)         AS day_start,
    LAST(host ORDER BY timestamp)                       AS host,
    COALESCE(LAST(vm_id ORDER BY timestamp), '')         AS vm_id,
    LAST(vm_name ORDER BY timestamp)                    AS vm_name,
    LAST(resource_group ORDER BY timestamp)             AS resource_group,
    MAX(disk_logical_used_bytes)                        AS disk_logical_max_bytes,
    MAX(disk_used_bytes)                                AS disk_compressed_max_bytes,
    MAX(disk_size_bytes)                                AS disk_provisioned_bytes,
    SUM(tx_delta)                                       AS network_tx_delta_bytes,
    SUM(rx_delta)                                       AS network_rx_delta_bytes,
    SUM(cpu_delta)                                      AS cpu_delta_seconds,
    SUM(io_read_delta)                                  AS io_read_delta_bytes,
    SUM(io_write_delta)                                 AS io_write_delta_bytes,
    MAX(memory_rss_bytes)                               AS memory_rss_max_bytes,
    MAX(memory_swap_bytes)                              AS memory_swap_max_bytes,
    COUNT(*)                                            AS sample_count
FROM windowed
GROUP BY date_trunc('hour', timestamp), vm_key
`
	start := time.Now()
	_, err := r.db.ExecContext(ctx, rebuildSQL, cutoff)
	if err != nil {
		return fmt.Errorf("create or replace hourly: %w", err)
	}
	var count int
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vm_metrics_hourly`).Scan(&count)
	slog.InfoContext(ctx, "hourly rollup rebuilt", "rows", count, "elapsed", time.Since(start).Round(time.Millisecond))
	return nil
}

// rebuildDaily replaces vm_metrics_daily by aggregating vm_metrics_hourly.
func (r *Rollup) rebuildDaily(ctx context.Context) error {
	const rebuildSQL = `
CREATE OR REPLACE TABLE vm_metrics_daily AS
SELECT
    day_start,
    LAST(host ORDER BY hour_start)                      AS host,
    COALESCE(LAST(vm_id ORDER BY hour_start), '')        AS vm_id,
    LAST(vm_name ORDER BY hour_start)                   AS vm_name,
    LAST(resource_group ORDER BY hour_start)            AS resource_group,
    AVG(disk_logical_max_bytes)::BIGINT                 AS disk_logical_avg_bytes,
    MAX(disk_logical_max_bytes)                         AS disk_logical_max_bytes,
    AVG(disk_compressed_max_bytes)::BIGINT              AS disk_compressed_avg_bytes,
    MAX(disk_provisioned_bytes)                         AS disk_provisioned_max_bytes,
    SUM(network_tx_delta_bytes)                         AS network_tx_bytes,
    SUM(network_rx_delta_bytes)                         AS network_rx_bytes,
    SUM(cpu_delta_seconds)                              AS cpu_seconds,
    SUM(io_read_delta_bytes)                            AS io_read_bytes,
    SUM(io_write_delta_bytes)                           AS io_write_bytes,
    MAX(memory_rss_max_bytes)                           AS memory_rss_max_bytes,
    MAX(memory_swap_max_bytes)                          AS memory_swap_max_bytes,
    COUNT(*)                                            AS hours_with_data
FROM vm_metrics_hourly
GROUP BY day_start, COALESCE(NULLIF(vm_id,''), vm_name)
`
	start := time.Now()
	_, err := r.db.ExecContext(ctx, rebuildSQL)
	if err != nil {
		return fmt.Errorf("create or replace daily: %w", err)
	}
	var count int
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vm_metrics_daily`).Scan(&count)
	slog.InfoContext(ctx, "daily rollup rebuilt", "rows", count, "elapsed", time.Since(start).Round(time.Millisecond))
	return nil
}

// rebuildMonthly replaces vm_metrics_monthly by aggregating vm_metrics_daily.
func (r *Rollup) rebuildMonthly(ctx context.Context) error {
	const rebuildSQL = `
CREATE OR REPLACE TABLE vm_metrics_monthly AS
SELECT
    date_trunc('month', day_start)::DATE                AS month_start,
    LAST(host ORDER BY day_start)                       AS host,
    COALESCE(LAST(vm_id ORDER BY day_start), '')         AS vm_id,
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
		return fmt.Errorf("create or replace monthly: %w", err)
	}
	var count int
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vm_metrics_monthly`).Scan(&count)
	slog.InfoContext(ctx, "monthly rollup rebuilt", "rows", count, "elapsed", time.Since(start).Round(time.Millisecond))
	return nil
}

// RunPeriodic starts a goroutine that runs rollups every interval.
// Follows the archiver pattern.
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
