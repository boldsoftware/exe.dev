// ClickHouse mirror: stream vm_metrics into a ClickHouse table as they are
// written to DuckDB, enabling warehouse-style queries without having to pull
// the duckdb file anywhere.
package metricsd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"exe.dev/metricsd/types"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"
	"github.com/prometheus/client_golang/prometheus"
)

// clickHouseQueueSize bounds the channel of pending batches. When the
// queue is full (e.g. ClickHouse is slow or unreachable), newly-enqueued
// batches are dropped and counted in a metric — the existing queue is
// preserved and drained FIFO.
const clickHouseQueueSize = 64

// ClickHouseTableSQL is the CREATE TABLE statement for the mirrored table.
// Columns match types.Metric.
const ClickHouseTableSQL = `
CREATE TABLE IF NOT EXISTS vm_metrics (
	timestamp                   DateTime64(6, 'UTC'),
	host                        LowCardinality(String),
	vm_name                     String,
	resource_group              LowCardinality(String),
	vm_id                       String,
	disk_size_bytes             Int64,
	disk_used_bytes             Int64,
	disk_logical_used_bytes     Int64,
	memory_nominal_bytes        Int64,
	memory_rss_bytes            Int64,
	memory_swap_bytes           Int64,
	cpu_used_cumulative_seconds Float64,
	cpu_nominal                 Float64,
	network_tx_bytes            Int64,
	network_rx_bytes            Int64,
	io_read_bytes               Int64,
	io_write_bytes              Int64,
	memory_anon_bytes           Int64 DEFAULT 0,
	memory_file_bytes           Int64 DEFAULT 0,
	memory_kernel_bytes         Int64 DEFAULT 0,
	memory_shmem_bytes          Int64 DEFAULT 0,
	memory_slab_bytes           Int64 DEFAULT 0,
	memory_inactive_file_bytes  Int64 DEFAULT 0,
	fs_total_bytes              Int64 DEFAULT 0,
	fs_free_bytes               Int64 DEFAULT 0,
	fs_available_bytes          Int64 DEFAULT 0,
	fs_used_bytes               Int64 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp)
ORDER BY (vm_name, timestamp)
`

// clickHouseAlterStatements brings older deployments up to date. Each
// statement is idempotent (uses IF NOT EXISTS). Append new statements; do
// not reorder.
var clickHouseAlterStatements = []string{
	`ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS memory_anon_bytes Int64 DEFAULT 0`,
	`ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS memory_file_bytes Int64 DEFAULT 0`,
	`ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS memory_kernel_bytes Int64 DEFAULT 0`,
	`ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS memory_shmem_bytes Int64 DEFAULT 0`,
	`ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS memory_slab_bytes Int64 DEFAULT 0`,
	`ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS memory_inactive_file_bytes Int64 DEFAULT 0`,
	`ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS fs_total_bytes Int64 DEFAULT 0`,
	`ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS fs_free_bytes Int64 DEFAULT 0`,
	`ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS fs_available_bytes Int64 DEFAULT 0`,
	`ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS fs_used_bytes Int64 DEFAULT 0`,
}

const clickHouseInsertSQL = `INSERT INTO vm_metrics (
	timestamp, host, vm_name, resource_group, vm_id,
	disk_size_bytes, disk_used_bytes, disk_logical_used_bytes,
	memory_nominal_bytes, memory_rss_bytes, memory_swap_bytes,
	cpu_used_cumulative_seconds, cpu_nominal,
	network_tx_bytes, network_rx_bytes,
	io_read_bytes, io_write_bytes,
	memory_anon_bytes, memory_file_bytes, memory_kernel_bytes,
	memory_shmem_bytes, memory_slab_bytes, memory_inactive_file_bytes,
	fs_total_bytes, fs_free_bytes, fs_available_bytes, fs_used_bytes
)`

// ClickHouseConfig configures the async ClickHouse mirror.
type ClickHouseConfig struct {
	// DSN is a clickhouse-go DSN (e.g.
	// clickhouse://user:pass@host:9440/default?secure=true). If empty,
	// StartClickHouseSync returns nil and mirroring is disabled.
	DSN string
	// Logger is the logger; defaults to slog.Default().
	Logger *slog.Logger
	// Registry, if non-nil, gets ClickHouse mirror metrics registered on it.
	Registry *prometheus.Registry
}

// ClickHouseSync is an async mirror of metrics writes into a ClickHouse
// table. Each call to Enqueue pushes a batch onto a bounded channel; a
// background goroutine drains the channel and inserts rows using
// clickhouse-go batches. If the channel is full, newly-enqueued batches
// are dropped and counted in `metricsd_clickhouse_dropped_batches_total`.
type ClickHouseSync struct {
	conn clickhouse.Conn
	q    chan []types.Metric
	log  *slog.Logger

	cancel context.CancelFunc
	done   chan struct{}

	rowsInserted    prometheus.Counter
	batchesInserted prometheus.Counter
	batchesDropped  prometheus.Counter
	insertDuration  prometheus.Histogram
	insertFailures  prometheus.Counter
	queueDepth      prometheus.GaugeFunc

	dropLogMu   sync.Mutex
	lastDropLog time.Time
} // execAlterWithReplicaSync runs an ALTER, retrying on ClickHouse error 517
// ("replica doesn't catchup with latest ALTER query updates ... please retry
// this query"), which happens on SharedMergeTree / replicated tables when a
// previous ALTER hasn't propagated yet. We SYSTEM SYNC REPLICA between
// attempts so the local replica catches up before retrying.
func execAlterWithReplicaSync(ctx context.Context, conn clickhouse.Conn, alter string, log *slog.Logger) error {
	const maxAttempts = 6
	backoff := 500 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := conn.Exec(ctx, alter)
		if err == nil {
			return nil
		}
		lastErr = err
		var chErr *proto.Exception
		if !errors.As(err, &chErr) || chErr.Code != 517 {
			return err
		}
		log.WarnContext(ctx, "clickhouse alter not yet replicated, syncing replica and retrying",
			"attempt", attempt, "alter", alter, "error", err)
		// Best-effort: tell this replica to catch up before retrying. Errors
		// here are not fatal — we still retry the ALTER.
		if syncErr := conn.Exec(ctx, "SYSTEM SYNC REPLICA vm_metrics"); syncErr != nil {
			log.WarnContext(ctx, "clickhouse SYSTEM SYNC REPLICA failed", "error", syncErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 8*time.Second {
			backoff *= 2
		}
	}
	return fmt.Errorf("after %d retries: %w", maxAttempts, lastErr)
}

// StartClickHouseSync connects, creates the vm_metrics table if it doesn't
// exist, and spawns a background goroutine that drains enqueued batches
// until ctx is canceled. Returns nil (and no error) when cfg.DSN is empty.
func StartClickHouseSync(ctx context.Context, cfg ClickHouseConfig) (*ClickHouseSync, error) {
	if cfg.DSN == "" {
		return nil, nil
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	opts, err := clickhouse.ParseDSN(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse clickhouse DSN: %w", err)
	}
	// clickhouse-go honors ?secure=true in the DSN query string to enable
	// TLS with default settings — no extra config required here.
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ping clickhouse: %w", err)
	}
	if err := conn.Exec(ctx, ClickHouseTableSQL); err != nil {
		conn.Close()
		return nil, fmt.Errorf("create clickhouse table: %w", err)
	}
	for _, alter := range clickHouseAlterStatements {
		if err := execAlterWithReplicaSync(ctx, conn, alter, log); err != nil {
			conn.Close()
			return nil, fmt.Errorf("alter clickhouse table: %w", err)
		}
	}

	runCtx, cancel := context.WithCancel(context.Background())
	s := &ClickHouseSync{
		conn:   conn,
		q:      make(chan []types.Metric, clickHouseQueueSize),
		log:    log,
		cancel: cancel,
		done:   make(chan struct{}),
		rowsInserted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "metricsd_clickhouse_rows_inserted_total",
			Help: "Rows mirrored to ClickHouse.",
		}),
		batchesInserted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "metricsd_clickhouse_batches_inserted_total",
			Help: "Batches mirrored to ClickHouse.",
		}),
		batchesDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "metricsd_clickhouse_dropped_batches_total",
			Help: "Batches dropped because the ClickHouse queue was full.",
		}),
		insertDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "metricsd_clickhouse_insert_duration_seconds",
			Help:    "Time spent inserting a batch into ClickHouse.",
			Buckets: prometheus.DefBuckets,
		}),
		insertFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "metricsd_clickhouse_insert_failures_total",
			Help: "Number of failed ClickHouse batch inserts.",
		}),
	}
	s.queueDepth = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "metricsd_clickhouse_queue_depth",
		Help: "Current ClickHouse mirror queue depth.",
	}, func() float64 { return float64(len(s.q)) })
	if cfg.Registry != nil {
		cfg.Registry.MustRegister(
			s.rowsInserted, s.batchesInserted, s.batchesDropped,
			s.insertDuration, s.insertFailures, s.queueDepth,
		)
	}

	// Stop the drain goroutine if the caller-provided ctx is canceled,
	// even if Close is never called.
	go func() {
		select {
		case <-ctx.Done():
			s.cancel()
		case <-s.done:
		}
	}()

	go s.run(runCtx)
	return s, nil
}

// Enqueue schedules a batch of metrics for mirroring to ClickHouse.
// The slice is handed off to the mirror — the caller must not mutate it
// after the call. Nil or empty batches are a no-op.
func (s *ClickHouseSync) Enqueue(batch []types.Metric) {
	if s == nil || len(batch) == 0 {
		return
	}
	select {
	case s.q <- batch:
	default:
		s.batchesDropped.Inc()
		s.logDrop(len(batch))
	}
}

// logDrop warns at most once every 5s during a drop storm to avoid log spam.
func (s *ClickHouseSync) logDrop(size int) {
	s.dropLogMu.Lock()
	now := time.Now()
	quiet := now.Sub(s.lastDropLog) < 5*time.Second
	if !quiet {
		s.lastDropLog = now
	}
	s.dropLogMu.Unlock()
	if !quiet {
		s.log.Warn("clickhouse mirror queue full, dropping batch", "size", size)
	}
}

// Close stops the drain goroutine and closes the underlying ClickHouse
// connection. It waits for an in-flight insert (if any) to finish or hit
// its 30s timeout. In-queue batches at shutdown are dropped. It is safe
// to call Close on a nil *ClickHouseSync and to call it more than once.
func (s *ClickHouseSync) Close() error {
	if s == nil {
		return nil
	}
	s.cancel()
	<-s.done
	return s.conn.Close()
}

func (s *ClickHouseSync) run(ctx context.Context) {
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			return
		case batch := <-s.q:
			s.insertBatch(ctx, batch)
		}
	}
}

func (s *ClickHouseSync) insertBatch(ctx context.Context, metrics []types.Metric) {
	start := time.Now()
	// Use a bounded timeout so a slow/hanging ClickHouse doesn't
	// permanently stall the background drain goroutine.
	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := s.doInsert(ictx, metrics); err != nil {
		s.insertFailures.Inc()
		s.log.ErrorContext(ctx, "clickhouse mirror insert failed", "error", err, "rows", len(metrics))
		return
	}
	s.rowsInserted.Add(float64(len(metrics)))
	s.batchesInserted.Inc()
	s.insertDuration.Observe(time.Since(start).Seconds())
}

func (s *ClickHouseSync) doInsert(ctx context.Context, metrics []types.Metric) error {
	batch, err := s.conn.PrepareBatch(ctx, clickHouseInsertSQL)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, m := range metrics {
		// Timestamps have already been normalized (zero -> now.UTC())
		// by InsertMetrics, so DuckDB and ClickHouse see the same value.
		if err := batch.Append(
			m.Timestamp, m.Host, m.VMName, m.ResourceGroup, m.VMID,
			m.DiskSizeBytes, m.DiskUsedBytes, m.DiskLogicalUsedBytes,
			m.MemoryNominalBytes, m.MemoryRSSBytes, m.MemorySwapBytes,
			m.CPUUsedCumulativeSecs, m.CPUNominal,
			m.NetworkTXBytes, m.NetworkRXBytes,
			m.IOReadBytes, m.IOWriteBytes,
			m.MemoryAnonBytes, m.MemoryFileBytes, m.MemoryKernelBytes,
			m.MemoryShmemBytes, m.MemorySlabBytes, m.MemoryInactiveFileBytes,
			m.FsTotalBytes, m.FsFreeBytes, m.FsAvailableBytes, m.FsUsedBytes,
		); err != nil {
			return fmt.Errorf("append row: %w", err)
		}
	}
	return batch.Send()
}
