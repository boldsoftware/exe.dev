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
	"sync"
	"time"

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

// OpenDB opens a DuckDB database and runs migrations.
// Returns the connector and sql.DB handle.
func OpenDB(ctx context.Context, path string) (*duckdb.Connector, *sql.DB, error) {
	connector, err := duckdb.NewConnector(path, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create connector: %w", err)
	}

	db := sql.OpenDB(connector)
	if err := RunMigrations(ctx, db); err != nil {
		db.Close()
		connector.Close()
		return nil, nil, fmt.Errorf("run migrations: %w", err)
	}
	return connector, db, nil
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
	staticFS, _ := fs.Sub(staticFiles, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("GET /health", s.handleHealth)

	// Prometheus metrics endpoint
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))

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
