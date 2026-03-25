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

	for _, m := range metrics {
		rowStart := time.Now()
		ts := m.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		// Store as UTC
		ts = ts.UTC()
		// Column order must match the physical table layout.
		// resource_group is added by migration 002 and appears last.
		err := appender.AppendRow(
			ts, m.Host, m.VMName,
			m.DiskSizeBytes, m.DiskUsedBytes, m.DiskLogicalUsedBytes,
			m.MemoryNominalBytes, m.MemoryRSSBytes, m.MemorySwapBytes,
			m.CPUUsedCumulativeSecs, m.CPUNominal,
			m.NetworkTXBytes, m.NetworkRXBytes,
			m.ResourceGroup,
			m.IOReadBytes, m.IOWriteBytes,
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
			network_tx_bytes, network_rx_bytes, resource_group
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
