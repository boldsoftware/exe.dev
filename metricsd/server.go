// Package metricsd provides a metrics collection server that stores VM metrics in DuckDB.
package metricsd

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"exe.dev/logging"
	"exe.dev/metricsd/types"
	"github.com/duckdb/duckdb-go/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"tailscale.com/net/tsaddr"
)

//go:embed static
var staticFiles embed.FS

// Server handles HTTP requests for metrics collection.
type Server struct {
	db        *sql.DB
	connector *duckdb.Connector

	// Protects appender access - DuckDB appenders are not thread-safe
	mu       sync.Mutex
	conn     driver.Conn
	appender *duckdb.Appender

	// Prometheus metrics
	registry           *prometheus.Registry
	uptimeGauge        prometheus.Gauge
	rowsInsertedTotal  prometheus.Counter
	insertBatchSeconds prometheus.Histogram
	insertRowSeconds   prometheus.Histogram
	startTime          time.Time

	requireTailscale bool // whether to verify requests come from tailscale/loopback

	// Last batch info
	lastBatchMu   sync.RWMutex
	lastBatchTime time.Time
	lastBatchSize int

	// Optional async ClickHouse mirror (nil if not configured).
	clickhouse *ClickHouseSync
}

// SetClickHouse attaches an optional ClickHouse mirror that receives each
// successfully-inserted batch. Pass nil to disable. Calling SetClickHouse
// replaces any existing mirror. Safe to call concurrently with writes.
func (s *Server) SetClickHouse(ch *ClickHouseSync) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clickhouse = ch
}

// NewServer creates a new metrics server with the given DuckDB connector and database.
// If requireTailscale is true, all requests must originate from a tailscale or loopback IP.
func NewServer(connector *duckdb.Connector, db *sql.DB, requireTailscale bool) *Server {
	reg := prometheus.NewRegistry()

	uptimeGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "metricsd_uptime_seconds",
		Help: "Number of seconds the metricsd server has been running",
	})

	rowsInsertedTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "metricsd_rows_inserted_total",
		Help: "Total number of metric rows inserted",
	})

	insertBatchSeconds := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "metricsd_insert_batch_duration_seconds",
		Help:    "Time spent inserting a batch of metrics",
		Buckets: prometheus.DefBuckets,
	})

	insertRowSeconds := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "metricsd_insert_row_duration_seconds",
		Help:    "Time spent inserting a single metric row",
		Buckets: prometheus.DefBuckets,
	})

	reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	reg.MustRegister(uptimeGauge, rowsInsertedTotal, insertBatchSeconds, insertRowSeconds)

	s := &Server{
		db:                 db,
		connector:          connector,
		requireTailscale:   requireTailscale,
		registry:           reg,
		uptimeGauge:        uptimeGauge,
		rowsInsertedTotal:  rowsInsertedTotal,
		insertBatchSeconds: insertBatchSeconds,
		insertRowSeconds:   insertRowSeconds,
		startTime:          time.Now(),
	}

	// Update uptime gauge periodically
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.uptimeGauge.Set(time.Since(s.startTime).Seconds())
		}
	}()

	return s
}

// OpenDB opens a DuckDB database, runs migrations, and sets up the archive view.
// archiveDir is the directory where parquet archive files are stored.
// If archiveDir is empty, no archiving is configured and the view points
// directly to the vm_metrics table.
// Returns the connector, sql.DB handle, and archiver (nil if archiveDir is empty).
func OpenDB(ctx context.Context, path, archiveDir string) (*duckdb.Connector, *sql.DB, *Archiver, error) {
	connector, err := duckdb.NewConnector(path, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create connector: %w", err)
	}

	db := sql.OpenDB(connector)
	if err := RunMigrations(ctx, db); err != nil {
		db.Close()
		connector.Close()
		return nil, nil, nil, fmt.Errorf("run migrations: %w", err)
	}

	var archiver *Archiver
	if archiveDir != "" {
		archiver = NewArchiver(db, archiveDir)
	}

	// Build the view (either with or without parquet files).
	if archiver != nil {
		if err := archiver.RebuildView(ctx); err != nil {
			db.Close()
			connector.Close()
			return nil, nil, nil, fmt.Errorf("rebuild view: %w", err)
		}
	} else {
		// No archiver — create a simple passthrough view.
		if _, err := db.ExecContext(ctx, "DROP VIEW IF EXISTS vm_metrics_all"); err != nil {
			db.Close()
			connector.Close()
			return nil, nil, nil, fmt.Errorf("drop view: %w", err)
		}
		if _, err := db.ExecContext(ctx, "CREATE VIEW vm_metrics_all AS SELECT * FROM vm_metrics"); err != nil {
			db.Close()
			connector.Close()
			return nil, nil, nil, fmt.Errorf("create view: %w", err)
		}
	}

	return connector, db, archiver, nil
}

// Close releases resources held by the server.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.appender != nil {
		s.appender.Close()
		s.appender = nil
	}
	if s.conn != nil {
		if closer, ok := s.conn.(interface{ Close() error }); ok {
			closer.Close()
		}
		s.conn = nil
	}

	if s.db != nil {
		if err := s.db.Close(); err != nil {
			slog.Error("error closing duckdb", "error", err)
		}
	}

	return nil
}

// getAppender returns the current appender, creating one if needed.
// Caller must hold s.mu.
func (s *Server) getAppender(ctx context.Context) (*duckdb.Appender, error) {
	if s.appender != nil {
		return s.appender, nil
	}

	conn, err := s.connector.Connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	appender, err := duckdb.NewAppenderFromConn(conn, "", "vm_metrics")
	if err != nil {
		if closer, ok := conn.(interface{ Close() error }); ok {
			closer.Close()
		}
		return nil, fmt.Errorf("create appender: %w", err)
	}

	s.conn = conn
	s.appender = appender
	return appender, nil
}

// Handler returns an http.Handler for the metrics server.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("POST /write", s.handlePostMetrics)
	mux.HandleFunc("GET /query", s.handleGetMetrics)
	mux.HandleFunc("GET /query/sparkline", s.handleSparklineData)
	mux.HandleFunc("GET /sparklines", s.handleSparklines)
	mux.HandleFunc("POST /query/vms", s.handleQueryVMs)
	mux.HandleFunc("POST /query/vms/pool", s.handleQueryVMsPool)
	mux.HandleFunc("POST /query/usage", s.handleQueryUsage)
	mux.HandleFunc("POST /query/hourly", s.handleQueryHourly)
	mux.HandleFunc("POST /query/daily", s.handleQueryDaily)
	mux.HandleFunc("POST /query/vms-over-limit", s.handleQueryVMsOverLimit)
	mux.HandleFunc("POST /query/monthly", s.handleQueryMonthly)
	staticFS, _ := fs.Sub(staticFiles, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("GET /health", s.handleHealth)

	// Prometheus metrics endpoint
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))

	// Git SHA for deploy verification
	mux.HandleFunc("GET /debug/gitsha", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, logging.GitCommit())
	})

	// pprof debug endpoints
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	mux.Handle("GET /debug/pprof/goroutine", pprof.Handler("goroutine"))
	mux.Handle("GET /debug/pprof/heap", pprof.Handler("heap"))
	mux.Handle("GET /debug/pprof/allocs", pprof.Handler("allocs"))
	mux.Handle("GET /debug/pprof/block", pprof.Handler("block"))
	mux.Handle("GET /debug/pprof/mutex", pprof.Handler("mutex"))
	mux.Handle("GET /debug/pprof/threadcreate", pprof.Handler("threadcreate"))

	if s.requireTailscale {
		return tailscaleOnly(mux)
	}
	return mux
}

// tailscaleOnly wraps an http.Handler to reject requests not from tailscale or loopback IPs.
func tailscaleOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		remoteIP, err := netip.ParseAddr(host)
		if err != nil {
			http.Error(w, "bad remote addr", http.StatusInternalServerError)
			return
		}
		if !remoteIP.IsLoopback() && !tsaddr.IsTailscaleIP(remoteIP) {
			http.Error(w, "access denied", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.lastBatchMu.RLock()
	lastTime := s.lastBatchTime
	lastSize := s.lastBatchSize
	s.lastBatchMu.RUnlock()

	var lastBatchInfo string
	if lastTime.IsZero() {
		lastBatchInfo = "none yet"
	} else {
		lastBatchInfo = fmt.Sprintf("%s (%d metrics, %s ago)",
			lastTime.UTC().Format(time.RFC3339),
			lastSize,
			time.Since(lastTime).Truncate(time.Second))
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>metricsd</title></head>
<body>
<h1>metricsd</h1>
<p>Last batch received: %s</p>
<ul>
<li><a href="/debug/pprof/">/debug/pprof/</a> - profiling</li>
<li><a href="/metrics">/metrics</a> - prometheus metrics</li>
<li><a href="/health">/health</a> - health check</li>
<li><a href="/query">/query</a> - query metrics (add ?vm_name=...&amp;limit=...)</li>
<li><a href="/sparklines">/sparklines</a> - sparklines dashboard</li>
</ul>
</body>
</html>
`, lastBatchInfo)
}

func (s *Server) handlePostMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var batch MetricsBatch
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		slog.ErrorContext(ctx, "failed to decode request", "error", err)
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if len(batch.Metrics) == 0 {
		http.Error(w, "no metrics provided", http.StatusBadRequest)
		return
	}

	if err := s.InsertMetrics(ctx, batch.Metrics); err != nil {
		slog.ErrorContext(ctx, "failed to insert metrics", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Update last batch info
	s.lastBatchMu.Lock()
	s.lastBatchTime = time.Now()
	s.lastBatchSize = len(batch.Metrics)
	s.lastBatchMu.Unlock()

	slog.InfoContext(ctx, "inserted metrics", "count", len(batch.Metrics))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"inserted": %d}`, len(batch.Metrics))
}

func (s *Server) handleGetMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vmName := r.URL.Query().Get("vm_name")
	limit := r.URL.Query().Get("limit")
	if limit == "" {
		limit = "100"
	}

	metrics, err := s.QueryMetrics(ctx, vmName, limit)
	if err != nil {
		slog.ErrorContext(ctx, "failed to query metrics", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(MetricsBatch{Metrics: metrics})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.db.PingContext(r.Context()); err != nil {
		http.Error(w, "database unhealthy", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// InsertMetrics inserts a batch of metrics into the database using the Appender API.
func (s *Server) InsertMetrics(ctx context.Context, metrics []Metric) error {
	batchStart := time.Now()
	defer func() {
		s.insertBatchSeconds.Observe(time.Since(batchStart).Seconds())
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	appender, err := s.getAppender(ctx)
	if err != nil {
		return err
	}

	// Normalize timestamps up-front so the DuckDB and ClickHouse sinks
	// see identical values. Zero timestamps are backfilled with now().
	now := time.Now()
	for i := range metrics {
		if metrics[i].Timestamp.IsZero() {
			metrics[i].Timestamp = now
		}
		metrics[i].Timestamp = metrics[i].Timestamp.UTC()
	}

	for _, m := range metrics {
		rowStart := time.Now()
		ts := m.Timestamp
		// Column order must match the physical table layout.
		// resource_group is added by migration 002, io_read/write_bytes by migration 003,
		// vm_id by migration 006 (ALTER TABLE) — it appears later physically — and
		// the memory.stat breakdown columns by migration 010.
		err := appender.AppendRow(
			ts, m.Host, m.VMName,
			m.DiskSizeBytes, m.DiskUsedBytes, m.DiskLogicalUsedBytes,
			m.MemoryNominalBytes, m.MemoryRSSBytes, m.MemorySwapBytes,
			m.CPUUsedCumulativeSecs, m.CPUNominal,
			m.NetworkTXBytes, m.NetworkRXBytes,
			m.ResourceGroup,
			m.IOReadBytes, m.IOWriteBytes,
			m.VMID,
			m.MemoryAnonBytes, m.MemoryFileBytes, m.MemoryKernelBytes,
			m.MemoryShmemBytes, m.MemorySlabBytes, m.MemoryInactiveFileBytes,
			m.FsTotalBytes, m.FsFreeBytes, m.FsAvailableBytes,
		)
		s.insertRowSeconds.Observe(time.Since(rowStart).Seconds())
		if err != nil {
			return fmt.Errorf("append row for %s: %w", m.VMName, err)
		}
	}

	if err := appender.Flush(); err != nil {
		return fmt.Errorf("flush appender: %w", err)
	}

	s.rowsInsertedTotal.Add(float64(len(metrics)))

	// Mirror to ClickHouse if configured. Enqueue is non-blocking and
	// drops batches if the mirror queue is full.
	if s.clickhouse != nil {
		s.clickhouse.Enqueue(metrics)
	}
	return nil
}

// QueryMetrics retrieves metrics from the database.
func (s *Server) QueryMetrics(ctx context.Context, vmName, limit string) ([]Metric, error) {
	var rows *sql.Rows
	var err error

	if vmName != "" {
		rows, err = s.db.QueryContext(ctx, SelectSQL+"WHERE vm_name = ? ORDER BY timestamp DESC LIMIT ?", vmName, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, SelectSQL+"ORDER BY timestamp DESC LIMIT ?", limit)
	}
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var metrics []Metric
	for rows.Next() {
		var m Metric
		if err := scanMetric(rows, &m); err != nil {
			return nil, err
		}
		metrics = append(metrics, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}
	return metrics, nil
}

// scanMetric scans a single metric row. Column order must match SelectSQL/SparklineSQL.
func scanMetric(rows *sql.Rows, m *Metric) error {
	if err := rows.Scan(
		&m.Timestamp, &m.Host, &m.VMName,
		&m.DiskSizeBytes, &m.DiskUsedBytes, &m.DiskLogicalUsedBytes,
		&m.MemoryNominalBytes, &m.MemoryRSSBytes, &m.MemorySwapBytes,
		&m.CPUUsedCumulativeSecs, &m.CPUNominal,
		&m.NetworkTXBytes, &m.NetworkRXBytes,
		&m.ResourceGroup,
		&m.IOReadBytes, &m.IOWriteBytes,
		&m.VMID,
		&m.MemoryAnonBytes, &m.MemoryFileBytes, &m.MemoryKernelBytes,
		&m.MemoryShmemBytes, &m.MemorySlabBytes, &m.MemoryInactiveFileBytes,
		&m.FsTotalBytes, &m.FsFreeBytes, &m.FsAvailableBytes,
	); err != nil {
		return fmt.Errorf("scan row: %w", err)
	}
	return nil
}

func (s *Server) handleSparklineData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hoursStr := r.URL.Query().Get("hours")
	if hoursStr == "" {
		hoursStr = "24"
	}
	hours, err := strconv.Atoi(hoursStr)
	if err != nil || hours < 1 || hours > 168 {
		http.Error(w, "hours must be an integer between 1 and 168", http.StatusBadRequest)
		return
	}

	// Create temp file for parquet output
	tmpFile, err := os.CreateTemp("/tmp", "sparkline-*.parquet")
	if err != nil {
		slog.ErrorContext(ctx, "failed to create temp file", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	// Use DuckDB COPY to write parquet directly
	query := fmt.Sprintf(`COPY (%s) TO '%s' (FORMAT PARQUET)`, fmt.Sprintf(SparklineSQL, hours), tmpPath)
	if _, err := s.db.ExecContext(ctx, query); err != nil {
		slog.ErrorContext(ctx, "failed to write parquet", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		slog.ErrorContext(ctx, "failed to open parquet file", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		slog.ErrorContext(ctx, "failed to stat parquet file", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=sparklines.parquet")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (s *Server) handleSparklines(w http.ResponseWriter, r *http.Request) {
	data, err := staticFiles.ReadFile("static/sparklines.html")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleQueryVMs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req QueryVMsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.ErrorContext(ctx, "failed to decode request", "error", err)
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if len(req.VMNames) == 0 {
		http.Error(w, "vm_names cannot be empty", http.StatusBadRequest)
		return
	}
	if len(req.VMNames) > 200 {
		http.Error(w, "vm_names cannot contain more than 200 VMs", http.StatusBadRequest)
		return
	}
	if req.Hours < 1 || req.Hours > 744 {
		http.Error(w, "hours must be between 1 and 744", http.StatusBadRequest)
		return
	}

	placeholders := make([]string, len(req.VMNames))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	placeholderStr := strings.Join(placeholders, ", ")

	args := make([]interface{}, len(req.VMNames))
	for i, name := range req.VMNames {
		args[i] = name
	}

	// Data is cumulative counters (CPU, network) and instantaneous gauges
	// (disk, memory), so we can thin by simply keeping every Nth raw sample
	// — no aggregation needed. Samples arrive ~every 10 min, so:
	//   ≤24h  → ~144 pts/VM, keep all (step 1)
	//   ≤168h → ~1008 pts/VM, keep every 6th → ~168 pts (hourly)
	//   >168h → ~4464 pts/VM, keep every 36th → ~124 pts (every 6h)
	var step int
	switch {
	case req.Hours <= 24:
		step = 1
	case req.Hours <= 168:
		step = 6
	default:
		step = 36
	}

	query := fmt.Sprintf(`
		SELECT timestamp, vm_name, host,
			disk_size_bytes, disk_used_bytes, disk_logical_used_bytes,
			memory_nominal_bytes, memory_rss_bytes, memory_swap_bytes,
			cpu_used_cumulative_seconds, cpu_nominal,
			network_tx_bytes, network_rx_bytes, resource_group,
			COALESCE(vm_id, '') AS vm_id,
			COALESCE(memory_anon_bytes, 0) AS memory_anon_bytes,
			COALESCE(memory_file_bytes, 0) AS memory_file_bytes,
			COALESCE(memory_kernel_bytes, 0) AS memory_kernel_bytes,
			COALESCE(memory_shmem_bytes, 0) AS memory_shmem_bytes,
			COALESCE(memory_slab_bytes, 0) AS memory_slab_bytes,
			COALESCE(memory_inactive_file_bytes, 0) AS memory_inactive_file_bytes,
			COALESCE(fs_total_bytes, 0) AS fs_total_bytes,
			COALESCE(fs_free_bytes, 0) AS fs_free_bytes,
			COALESCE(fs_available_bytes, 0) AS fs_available_bytes
		FROM (
			SELECT *,
				row_number() OVER (PARTITION BY vm_name ORDER BY timestamp) AS rn
			FROM vm_metrics_all
			WHERE vm_name IN (%s)
				AND timestamp > now() - INTERVAL '%d' HOUR
		) sub
		WHERE (rn - 1) %% %d = 0
		ORDER BY vm_name, timestamp ASC
	`, placeholderStr, req.Hours, step)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		slog.ErrorContext(ctx, "failed to query metrics", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	vmMetrics := make(map[string][]Metric)
	for rows.Next() {
		var m Metric
		if err := rows.Scan(
			&m.Timestamp,
			&m.VMName,
			&m.Host,
			&m.DiskSizeBytes,
			&m.DiskUsedBytes,
			&m.DiskLogicalUsedBytes,
			&m.MemoryNominalBytes,
			&m.MemoryRSSBytes,
			&m.MemorySwapBytes,
			&m.CPUUsedCumulativeSecs,
			&m.CPUNominal,
			&m.NetworkTXBytes,
			&m.NetworkRXBytes,
			&m.ResourceGroup,
			&m.VMID,
			&m.MemoryAnonBytes,
			&m.MemoryFileBytes,
			&m.MemoryKernelBytes,
			&m.MemoryShmemBytes,
			&m.MemorySlabBytes,
			&m.MemoryInactiveFileBytes,
			&m.FsTotalBytes,
			&m.FsFreeBytes,
			&m.FsAvailableBytes,
		); err != nil {
			slog.ErrorContext(ctx, "failed to scan row", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		vmMetrics[m.VMName] = append(vmMetrics[m.VMName], m)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "rows iteration error", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(QueryVMsResponse{VMs: vmMetrics})
}

func (s *Server) handleQueryVMsPool(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req types.QueryVMsPoolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.ErrorContext(ctx, "failed to decode request", "error", err)
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if len(req.VMNames) == 0 {
		http.Error(w, "vm_names cannot be empty", http.StatusBadRequest)
		return
	}
	if len(req.VMNames) > 200 {
		http.Error(w, "vm_names cannot contain more than 200 VMs", http.StatusBadRequest)
		return
	}
	if req.Hours < 1 || req.Hours > 744 {
		http.Error(w, "hours must be between 1 and 744", http.StatusBadRequest)
		return
	}

	placeholders := make([]string, len(req.VMNames))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	placeholderStr := strings.Join(placeholders, ", ")

	args := make([]interface{}, len(req.VMNames))
	for i, name := range req.VMNames {
		args[i] = name
	}

	// Bucket interval: 10 min for <=24h, 1 hour for <=168h, 6 hours for >168h.
	var bucketInterval string
	switch {
	case req.Hours <= 24:
		bucketInterval = "10 MINUTE"
	case req.Hours <= 168:
		bucketInterval = "1 HOUR"
	default:
		bucketInterval = "6 HOUR"
	}

	// Query: bucket timestamps, compute CPU delta per VM using LAG,
	// then aggregate across VMs per bucket.
	query := fmt.Sprintf(`
		WITH raw AS (
			SELECT
				time_bucket(INTERVAL '%s', timestamp) AS bucket,
				vm_name,
				LAST(memory_rss_bytes ORDER BY timestamp) AS mem_bytes,
				LAST(cpu_used_cumulative_seconds ORDER BY timestamp) AS cpu_cum,
				FIRST(cpu_used_cumulative_seconds ORDER BY timestamp) AS cpu_cum_first,
				LAST(timestamp ORDER BY timestamp) AS last_ts,
				FIRST(timestamp ORDER BY timestamp) AS first_ts
			FROM vm_metrics_all
			WHERE vm_name IN (%s)
				AND timestamp > now() - INTERVAL '%d' HOUR
			GROUP BY bucket, vm_name
		),
		deltas AS (
			SELECT
				bucket,
				vm_name,
				mem_bytes,
				CASE
					WHEN epoch(last_ts) - epoch(first_ts) > 0
						AND cpu_cum >= cpu_cum_first
					THEN (cpu_cum - cpu_cum_first) / (epoch(last_ts) - epoch(first_ts))
					ELSE 0
				END AS cpu_cores
			FROM raw
		)
		SELECT
			bucket,
			AVG(cpu_cores) AS cpu_avg,
			SUM(cpu_cores) AS cpu_sum,
			AVG(mem_bytes) AS mem_avg,
			SUM(mem_bytes) AS mem_sum
		FROM deltas
		GROUP BY bucket
		ORDER BY bucket ASC
	`, bucketInterval, placeholderStr, req.Hours)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		slog.ErrorContext(ctx, "failed to query pool history", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	points := make([]types.PoolPoint, 0)
	for rows.Next() {
		var bucket time.Time
		var cpuAvg, cpuSum, memAvg, memSum float64
		if err := rows.Scan(&bucket, &cpuAvg, &cpuSum, &memAvg, &memSum); err != nil {
			slog.ErrorContext(ctx, "failed to scan pool row", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		points = append(points, types.PoolPoint{
			Timestamp: bucket.UTC().Format(time.RFC3339),
			CPUCores:  types.PoolMetric{Avg: cpuAvg, Sum: cpuSum},
			MemBytes:  types.PoolMetric{Avg: memAvg, Sum: memSum},
		})
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "rows iteration error", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(types.QueryVMsPoolResponse{Points: points})
}

func (s *Server) handleQueryUsage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req types.QueryUsageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if len(req.ResourceGroups) == 0 {
		http.Error(w, "resource_groups cannot be empty", http.StatusBadRequest)
		return
	}
	if len(req.ResourceGroups) > 500 {
		http.Error(w, "resource_groups cannot contain more than 500 entries", http.StatusBadRequest)
		return
	}
	if req.Start.IsZero() || req.End.IsZero() || !req.Start.Before(req.End) {
		http.Error(w, "start and end must be valid and start must be before end", http.StatusBadRequest)
		return
	}
	if req.End.Sub(req.Start) > 366*24*time.Hour {
		http.Error(w, "period cannot exceed 366 days", http.StatusBadRequest)
		return
	}

	placeholders := make([]string, len(req.ResourceGroups))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	phStr := strings.Join(placeholders, ", ")

	querySQL := fmt.Sprintf(`
		SELECT
			vm_id AS vm_key,
			COALESCE(LAST(vm_id ORDER BY day_start), '')      AS vm_id,
			LAST(vm_name ORDER BY day_start)    AS vm_name,
			resource_group,
			AVG(disk_logical_avg_bytes)::BIGINT  AS disk_avg_bytes,
			MAX(disk_logical_max_bytes)          AS disk_max_bytes,
			MAX(disk_provisioned_max_bytes)      AS disk_provisioned_max_bytes,
			SUM(network_tx_bytes + network_rx_bytes) AS bandwidth_bytes,
			SUM(cpu_seconds)                    AS cpu_seconds,
			SUM(io_read_bytes)                  AS io_read_bytes,
			SUM(io_write_bytes)                 AS io_write_bytes,
			COUNT(*)                            AS days_with_data
		FROM vm_metrics_daily
		WHERE resource_group IN (%s)
		  AND day_start >= ?
		  AND day_start < ?
		GROUP BY vm_id, resource_group
		ORDER BY resource_group, vm_name
	`, phStr)

	args := make([]interface{}, 0, len(req.ResourceGroups)+2)
	for _, rg := range req.ResourceGroups {
		args = append(args, rg)
	}
	args = append(args, req.Start, req.End)

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		slog.ErrorContext(ctx, "query usage failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type rgData struct {
		vms []types.VMUsageSummary
	}
	rgMap := make(map[string]*rgData)
	for _, rg := range req.ResourceGroups {
		rgMap[rg] = &rgData{vms: make([]types.VMUsageSummary, 0)}
	}

	for rows.Next() {
		var vmKey, vmID, vmName, rg string
		var diskAvg, diskMax, diskProvisioned, bandwidth, ioRead, ioWrite int64
		var cpuSecs float64
		var daysWithData int
		if err := rows.Scan(&vmKey, &vmID, &vmName, &rg, &diskAvg, &diskMax, &diskProvisioned, &bandwidth, &cpuSecs, &ioRead, &ioWrite, &daysWithData); err != nil {
			slog.ErrorContext(ctx, "scan usage row failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if d, ok := rgMap[rg]; ok {
			d.vms = append(d.vms, types.VMUsageSummary{
				VMID:                    vmID,
				VMName:                  vmName,
				ResourceGroup:           rg,
				DiskAvgBytes:            diskAvg,
				DiskMaxBytes:            diskMax,
				DiskProvisionedMaxBytes: diskProvisioned,
				BandwidthBytes:          bandwidth,
				CPUSeconds:              cpuSecs,
				IOReadBytes:             ioRead,
				IOWriteBytes:            ioWrite,
				DaysWithData:            daysWithData,
			})
		}
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "rows iteration error", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	summaries := make([]types.UsageSummary, 0, len(req.ResourceGroups))
	for _, rg := range req.ResourceGroups {
		d := rgMap[rg]
		vms := make([]types.VMUsageSummary, 0)
		if d != nil {
			vms = d.vms
		}
		var totalDiskAvg, totalDiskPeak, totalBandwidth, totalIORead, totalIOWrite int64
		var totalCPU float64
		for _, vm := range vms {
			totalDiskAvg += vm.DiskAvgBytes
			if vm.DiskMaxBytes > totalDiskPeak {
				totalDiskPeak = vm.DiskMaxBytes
			}
			totalBandwidth += vm.BandwidthBytes
			totalCPU += vm.CPUSeconds
			totalIORead += vm.IOReadBytes
			totalIOWrite += vm.IOWriteBytes
		}
		summaries = append(summaries, types.UsageSummary{
			ResourceGroup:  rg,
			PeriodStart:    req.Start,
			PeriodEnd:      req.End,
			DiskAvgBytes:   totalDiskAvg,
			DiskPeakBytes:  totalDiskPeak,
			BandwidthBytes: totalBandwidth,
			CPUSeconds:     totalCPU,
			VMs:            vms,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(types.QueryUsageResponse{Metrics: summaries})
}

func (s *Server) handleQueryHourly(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req types.QueryHourlyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if len(req.ResourceGroups) == 0 {
		http.Error(w, "resource_groups cannot be empty", http.StatusBadRequest)
		return
	}
	if len(req.ResourceGroups) > 500 {
		http.Error(w, "resource_groups cannot contain more than 500 entries", http.StatusBadRequest)
		return
	}
	if req.Start.IsZero() || req.End.IsZero() || !req.Start.Before(req.End) {
		http.Error(w, "start and end must be valid and start must be before end", http.StatusBadRequest)
		return
	}
	if req.End.Sub(req.Start) > 31*24*time.Hour {
		http.Error(w, "period cannot exceed 31 days", http.StatusBadRequest)
		return
	}

	placeholders := make([]string, len(req.ResourceGroups))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	phStr := strings.Join(placeholders, ", ")

	querySQL := fmt.Sprintf(`
WITH windowed AS (
    SELECT
        vm_id AS vm_key,
        timestamp,
        host, vm_id, vm_name, resource_group,
        disk_logical_used_bytes, disk_used_bytes, disk_size_bytes,
        memory_rss_bytes, memory_swap_bytes,
        GREATEST(0, network_tx_bytes - COALESCE(LAG(network_tx_bytes) OVER w, network_tx_bytes)) AS tx_delta,
        GREATEST(0, network_rx_bytes - COALESCE(LAG(network_rx_bytes) OVER w, network_rx_bytes)) AS rx_delta,
        GREATEST(0, cpu_used_cumulative_seconds - COALESCE(LAG(cpu_used_cumulative_seconds) OVER w, cpu_used_cumulative_seconds)) AS cpu_delta,
        GREATEST(0, io_read_bytes - COALESCE(LAG(io_read_bytes) OVER w, io_read_bytes)) AS io_read_delta,
        GREATEST(0, io_write_bytes - COALESCE(LAG(io_write_bytes) OVER w, io_write_bytes)) AS io_write_delta
    FROM (
        SELECT *, vm_id AS vm_key_inner
        FROM vm_metrics
        WHERE resource_group IN (%s)
          AND timestamp >= ? AND timestamp < ?
    ) raw
    WINDOW w AS (PARTITION BY vm_id ORDER BY timestamp ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)
)
SELECT
    date_trunc('hour', timestamp)::TIMESTAMPTZ AS hour_start,
    CAST(date_trunc('hour', timestamp) AS DATE) AS day_start,
    LAST(host ORDER BY timestamp) AS host,
    COALESCE(LAST(vm_id ORDER BY timestamp), '') AS vm_id,
    LAST(vm_name ORDER BY timestamp) AS vm_name,
    LAST(resource_group ORDER BY timestamp) AS resource_group,
    MAX(disk_logical_used_bytes) AS disk_logical_max_bytes,
    MAX(disk_used_bytes) AS disk_compressed_max_bytes,
    MAX(disk_size_bytes) AS disk_provisioned_bytes,
    SUM(tx_delta) AS network_tx_delta_bytes,
    SUM(rx_delta) AS network_rx_delta_bytes,
    SUM(cpu_delta) AS cpu_delta_seconds,
    SUM(io_read_delta) AS io_read_delta_bytes,
    SUM(io_write_delta) AS io_write_delta_bytes,
    MAX(memory_rss_bytes) AS memory_rss_max_bytes,
    MAX(memory_swap_bytes) AS memory_swap_max_bytes,
    COUNT(*) AS sample_count
FROM windowed
GROUP BY date_trunc('hour', timestamp), vm_key
ORDER BY vm_name, hour_start
	`, phStr)

	args := make([]interface{}, 0, len(req.ResourceGroups)+2)
	for _, rg := range req.ResourceGroups {
		args = append(args, rg)
	}
	args = append(args, req.Start, req.End)

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		slog.ErrorContext(ctx, "query hourly failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	hours := make([]types.HourlyMetric, 0)
	for rows.Next() {
		var h types.HourlyMetric
		if err := rows.Scan(
			&h.HourStart, &h.DayStart, &h.Host, &h.VMID, &h.VMName, &h.ResourceGroup,
			&h.DiskLogicalMaxBytes, &h.DiskCompressedMaxBytes, &h.DiskProvisionedBytes,
			&h.NetworkTXDeltaBytes, &h.NetworkRXDeltaBytes,
			&h.CPUDeltaSeconds,
			&h.IOReadDeltaBytes, &h.IOWriteDeltaBytes,
			&h.MemoryRSSMaxBytes, &h.MemorySwapMaxBytes,
			&h.SampleCount,
		); err != nil {
			slog.ErrorContext(ctx, "scan hourly row failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		hours = append(hours, h)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "rows iteration error", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(types.QueryHourlyResponse{Metrics: hours})
}

func (s *Server) handleQueryDaily(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req types.QueryDailyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if len(req.ResourceGroups) == 0 {
		http.Error(w, "resource_groups cannot be empty", http.StatusBadRequest)
		return
	}
	if len(req.ResourceGroups) > 500 {
		http.Error(w, "resource_groups cannot contain more than 500 entries", http.StatusBadRequest)
		return
	}
	if req.Start.IsZero() || req.End.IsZero() || !req.Start.Before(req.End) {
		http.Error(w, "start and end must be valid and start must be before end", http.StatusBadRequest)
		return
	}
	if req.End.Sub(req.Start) > 366*24*time.Hour {
		http.Error(w, "period cannot exceed 366 days", http.StatusBadRequest)
		return
	}

	placeholders := make([]string, len(req.ResourceGroups))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	phStr := strings.Join(placeholders, ", ")

	var querySQL string
	if req.GroupByVM {
		querySQL = fmt.Sprintf(`
			SELECT
				day_start, host, vm_id, vm_name, resource_group,
				disk_logical_avg_bytes, disk_logical_max_bytes,
				disk_compressed_avg_bytes, disk_provisioned_max_bytes,
				network_tx_bytes, network_rx_bytes,
				cpu_seconds,
				io_read_bytes, io_write_bytes,
				memory_rss_max_bytes, memory_swap_max_bytes,
				hours_with_data
			FROM vm_metrics_daily
			WHERE resource_group IN (%s)
			  AND day_start >= ?
			  AND day_start < ?
			ORDER BY vm_name, day_start
		`, phStr)
	} else {
		querySQL = fmt.Sprintf(`
			SELECT
				day_start, '' AS host, '' AS vm_id, '' AS vm_name, '' AS resource_group,
				SUM(disk_logical_avg_bytes)::BIGINT,
				SUM(disk_logical_max_bytes)::BIGINT,
				SUM(disk_compressed_avg_bytes)::BIGINT,
				SUM(disk_provisioned_max_bytes)::BIGINT,
				SUM(network_tx_bytes)::BIGINT,
				SUM(network_rx_bytes)::BIGINT,
				SUM(cpu_seconds),
				SUM(io_read_bytes)::BIGINT,
				SUM(io_write_bytes)::BIGINT,
				MAX(memory_rss_max_bytes),
				MAX(memory_swap_max_bytes),
				SUM(hours_with_data)
			FROM vm_metrics_daily
			WHERE resource_group IN (%s)
			  AND day_start >= ?
			  AND day_start < ?
			GROUP BY day_start
			ORDER BY day_start
		`, phStr)
	}

	args := make([]interface{}, 0, len(req.ResourceGroups)+2)
	for _, rg := range req.ResourceGroups {
		args = append(args, rg)
	}
	args = append(args, req.Start, req.End)

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		slog.ErrorContext(ctx, "query daily failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	days := make([]types.DailyMetric, 0)
	for rows.Next() {
		var d types.DailyMetric
		if err := rows.Scan(
			&d.DayStart, &d.Host, &d.VMID, &d.VMName, &d.ResourceGroup,
			&d.DiskLogicalAvgBytes, &d.DiskLogicalMaxBytes,
			&d.DiskCompressedAvgBytes, &d.DiskProvisionedMaxBytes,
			&d.NetworkTXBytes, &d.NetworkRXBytes,
			&d.CPUSeconds,
			&d.IOReadBytes, &d.IOWriteBytes,
			&d.MemoryRSSMaxBytes, &d.MemorySwapMaxBytes,
			&d.HoursWithData,
		); err != nil {
			slog.ErrorContext(ctx, "scan daily row failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		days = append(days, d)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "rows iteration error", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(types.QueryDailyResponse{Metrics: days})
}

func (s *Server) handleQueryMonthly(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req types.QueryMonthlyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if len(req.ResourceGroups) == 0 {
		http.Error(w, "resource_groups cannot be empty", http.StatusBadRequest)
		return
	}
	if len(req.ResourceGroups) > 500 {
		http.Error(w, "resource_groups cannot contain more than 500 entries", http.StatusBadRequest)
		return
	}
	if req.Start.IsZero() || req.End.IsZero() || !req.Start.Before(req.End) {
		http.Error(w, "start and end must be valid and start must be before end", http.StatusBadRequest)
		return
	}

	placeholders := make([]string, len(req.ResourceGroups))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	phStr := strings.Join(placeholders, ", ")

	var querySQL string
	if req.GroupByVM {
		querySQL = fmt.Sprintf(`
			SELECT
				month_start, host, vm_id, vm_name, resource_group,
				disk_logical_avg_bytes, disk_logical_max_bytes,
				disk_compressed_avg_bytes, disk_provisioned_max_bytes,
				network_tx_bytes, network_rx_bytes,
				cpu_seconds,
				io_read_bytes, io_write_bytes,
				memory_rss_max_bytes, memory_swap_max_bytes,
				days_with_data
			FROM vm_metrics_monthly
			WHERE resource_group IN (%s)
			  AND month_start >= ?
			  AND month_start < ?
			ORDER BY vm_name, month_start
		`, phStr)
	} else {
		querySQL = fmt.Sprintf(`
			SELECT
				month_start, '' AS host, '' AS vm_id, '' AS vm_name, '' AS resource_group,
				SUM(disk_logical_avg_bytes)::BIGINT,
				SUM(disk_logical_max_bytes)::BIGINT,
				SUM(disk_compressed_avg_bytes)::BIGINT,
				SUM(disk_provisioned_max_bytes)::BIGINT,
				SUM(network_tx_bytes)::BIGINT,
				SUM(network_rx_bytes)::BIGINT,
				SUM(cpu_seconds),
				SUM(io_read_bytes)::BIGINT,
				SUM(io_write_bytes)::BIGINT,
				MAX(memory_rss_max_bytes),
				MAX(memory_swap_max_bytes),
				MAX(days_with_data)
			FROM vm_metrics_monthly
			WHERE resource_group IN (%s)
			  AND month_start >= ?
			  AND month_start < ?
			GROUP BY month_start
			ORDER BY month_start
		`, phStr)
	}

	args := make([]interface{}, 0, len(req.ResourceGroups)+2)
	for _, rg := range req.ResourceGroups {
		args = append(args, rg)
	}
	args = append(args, req.Start, req.End)

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		slog.ErrorContext(ctx, "query monthly failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	months := make([]types.MonthlyMetric, 0)
	for rows.Next() {
		var m types.MonthlyMetric
		if err := rows.Scan(
			&m.MonthStart, &m.Host, &m.VMID, &m.VMName, &m.ResourceGroup,
			&m.DiskLogicalAvgBytes, &m.DiskLogicalMaxBytes,
			&m.DiskCompressedAvgBytes, &m.DiskProvisionedMaxBytes,
			&m.NetworkTXBytes, &m.NetworkRXBytes,
			&m.CPUSeconds,
			&m.IOReadBytes, &m.IOWriteBytes,
			&m.MemoryRSSMaxBytes, &m.MemorySwapMaxBytes,
			&m.DaysWithData,
		); err != nil {
			slog.ErrorContext(ctx, "scan monthly row failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		months = append(months, m)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "rows iteration error", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(types.QueryMonthlyResponse{Metrics: months})
}

func (s *Server) handleQueryVMsOverLimit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req types.QueryVMsOverLimitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if len(req.VMIDs) == 0 {
		http.Error(w, "vm_ids cannot be empty", http.StatusBadRequest)
		return
	}
	if len(req.VMIDs) > 500 {
		http.Error(w, "vm_ids cannot contain more than 500 entries", http.StatusBadRequest)
		return
	}

	// Current calendar month boundaries.
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := now

	placeholders := make([]string, len(req.VMIDs))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	phStr := strings.Join(placeholders, ", ")

	querySQL := fmt.Sprintf(`
		SELECT
			vm_id AS vm_key,
			COALESCE(LAST(vm_id ORDER BY day_start), '')      AS vm_id,
			LAST(vm_name ORDER BY day_start)    AS vm_name,
			AVG(disk_logical_avg_bytes)::BIGINT  AS disk_avg_bytes,
			SUM(network_tx_bytes + network_rx_bytes) AS bandwidth_bytes
		FROM vm_metrics_daily
		WHERE vm_id IN (%s)
		  AND day_start >= ?
		  AND day_start < ?
		GROUP BY vm_id
	`, phStr)

	args := make([]interface{}, 0, len(req.VMIDs)+2)
	for _, id := range req.VMIDs {
		args = append(args, id)
	}
	args = append(args, monthStart, monthEnd)

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		slog.ErrorContext(ctx, "query vms-over-limit failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	vms := make([]types.VMOverLimit, 0)
	for rows.Next() {
		var vmKey, vmID, vmName string
		var diskAvg, bandwidth int64
		if err := rows.Scan(&vmKey, &vmID, &vmName, &diskAvg, &bandwidth); err != nil {
			slog.ErrorContext(ctx, "scan vms-over-limit row failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		diskOver := diskAvg > req.DiskIncludedBytes
		bandwidthOver := bandwidth > req.BandwidthIncludedBytes
		if diskOver || bandwidthOver {
			vms = append(vms, types.VMOverLimit{
				VMID:           vmID,
				VMName:         vmName,
				DiskAvgBytes:   diskAvg,
				BandwidthBytes: bandwidth,
				DiskOver:       diskOver,
				BandwidthOver:  bandwidthOver,
			})
		}
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "rows iteration error", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(types.QueryVMsOverLimitResponse{VMs: vms})
}
