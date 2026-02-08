// Package metricsd provides a metrics collection server that stores VM metrics in DuckDB.
package metricsd

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"tailscale.com/net/tsaddr"
)

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

// OpenDB opens a DuckDB database and initializes the schema.
// Returns the connector and sql.DB handle.
func OpenDB(ctx context.Context, path string) (*duckdb.Connector, *sql.DB, error) {
	connector, err := duckdb.NewConnector(path, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create connector: %w", err)
	}

	db := sql.OpenDB(connector)
	if _, err := db.ExecContext(ctx, Schema); err != nil {
		db.Close()
		connector.Close()
		return nil, nil, fmt.Errorf("initialize schema: %w", err)
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
		err := appender.AppendRow(
			ts, m.Host, m.VMName,
			m.DiskSizeBytes, m.DiskUsedBytes, m.DiskLogicalUsedBytes,
			m.MemoryNominalBytes, m.MemoryRSSBytes, m.MemorySwapBytes,
			m.CPUUsedCumulativeSecs, m.CPUNominal,
			m.NetworkTXBytes, m.NetworkRXBytes,
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
		if err := rows.Scan(
			&m.Timestamp, &m.Host, &m.VMName,
			&m.DiskSizeBytes, &m.DiskUsedBytes, &m.DiskLogicalUsedBytes,
			&m.MemoryNominalBytes, &m.MemoryRSSBytes, &m.MemorySwapBytes,
			&m.CPUUsedCumulativeSecs, &m.CPUNominal,
			&m.NetworkTXBytes, &m.NetworkRXBytes,
		); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		metrics = append(metrics, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}
	return metrics, nil
}

// QuerySparklineMetrics retrieves metrics for the sparkline dashboard within a time window.
func (s *Server) QuerySparklineMetrics(ctx context.Context, hours int) ([]Metric, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(SparklineSQL, hours))
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var metrics []Metric
	for rows.Next() {
		var m Metric
		if err := rows.Scan(
			&m.Timestamp, &m.Host, &m.VMName,
			&m.DiskSizeBytes, &m.DiskUsedBytes, &m.DiskLogicalUsedBytes,
			&m.MemoryNominalBytes, &m.MemoryRSSBytes, &m.MemorySwapBytes,
			&m.CPUUsedCumulativeSecs, &m.CPUNominal,
			&m.NetworkTXBytes, &m.NetworkRXBytes,
		); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		metrics = append(metrics, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}
	return metrics, nil
}

// counterMetrics lists metric fields that are cumulative counters and need rate calculation.
var counterMetrics = []string{
	"cpu_used_cumulative_seconds",
	"network_tx_bytes",
	"network_rx_bytes",
}

// SparklineResponse is the JSON response for the sparkline data endpoint.
type SparklineResponse struct {
	Metrics  []Metric `json:"metrics"`
	Counters []string `json:"counters"`
}

func (s *Server) handleSparklineData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hoursStr := r.URL.Query().Get("hours")
	if hoursStr == "" {
		hoursStr = "24"
	}
	hours, err := strconv.Atoi(hoursStr)
	if err != nil {
		http.Error(w, "invalid hours parameter", http.StatusBadRequest)
		return
	}

	metrics, err := s.QuerySparklineMetrics(ctx, hours)
	if err != nil {
		slog.ErrorContext(ctx, "failed to query sparkline metrics", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SparklineResponse{
		Metrics:  metrics,
		Counters: counterMetrics,
	})
}

func (s *Server) handleSparklines(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(sparklinesHTML))
}

const sparklinesHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>metricsd sparklines</title>
<script src="https://cdn.jsdelivr.net/npm/vega@5"></script>
<script src="https://cdn.jsdelivr.net/npm/vega-lite@5"></script>
<script src="https://cdn.jsdelivr.net/npm/vega-embed@6"></script>
<style>
body { font-family: system-ui, sans-serif; margin: 10px; background: #fff; }
h1 { font-size: 1.1em; margin: 4px 0 8px; }
#error { color: red; }
#loading { color: #666; padding: 20px; }
@keyframes spin { to { transform: rotate(360deg); } }
#loading::before { content: ''; display: inline-block; width: 14px; height: 14px;
  border: 2px solid #ccc; border-top-color: #333; border-radius: 50%;
  animation: spin 0.8s linear infinite; margin-right: 8px; vertical-align: middle; }
#controls { margin: 6px 0; display: flex; gap: 12px; align-items: center; flex-wrap: wrap; font-size: 0.85em; }
#controls label { font-weight: 600; }
#controls select, #controls input { font-size: 0.85em; padding: 2px 4px; }
#controls input[type=text] { width: 160px; }
</style>
</head>
<body>
<h1><a href="/">metricsd</a> / sparklines <span id="range" style="font-weight:normal;font-size:0.8em;color:#666"></span></h1>
<div id="controls">
  <label>Host: <select id="hostFilter"><option value="">All</option></select></label>
  <label>Name: <input type="text" id="nameFilter" placeholder="filter VMs..."></label>
  <label>Sort: <select id="sortBy">
    <option value="name">Name</option>
    <option value="disk_used_bytes">Disk Used</option>
    <option value="memory_rss_bytes">Memory RSS</option>
    <option value="memory_swap_bytes">Swap</option>
    <option value="_cpu_pct">CPU %</option>
    <option value="_net_tx_mbps">Network TX</option>
    <option value="_net_rx_mbps">Network RX</option>
  </select></label>
  <label><input type="checkbox" id="sortDesc" checked> Desc</label>
</div>
<div id="error"></div>
<div id="loading">Loading metrics...</div>
<div id="vis"></div>
<script>
(async function() {
  const errorEl = document.getElementById('error');
  const loadingEl = document.getElementById('loading');
  try {
    const resp = await fetch('/query/sparkline?hours=24');
    if (!resp.ok) throw new Error('fetch failed: ' + resp.status);
    const data = await resp.json();
    if (!data.metrics || data.metrics.length === 0) {
      loadingEl.hidden = true;
      errorEl.textContent = 'No metrics data available.';
      return;
    }

    // Show time range
    let minT = Infinity, maxT = -Infinity;
    for (const m of data.metrics) {
      const t = new Date(m.timestamp).getTime();
      if (t < minT) minT = t;
      if (t > maxT) maxT = t;
    }
    const tfmt = d => new Date(d).toLocaleString(undefined, {month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'});
    document.getElementById('range').textContent = tfmt(minT) + ' \u2013 ' + tfmt(maxT);

    // Group by vm_name
    const byVM = {};
    const vmHost = {}; // vm_name -> host
    for (const m of data.metrics) {
      if (!byVM[m.vm_name]) { byVM[m.vm_name] = []; vmHost[m.vm_name] = m.host; }
      byVM[m.vm_name].push(m);
    }

    // Compute derived fields per row
    for (const rows of Object.values(byVM)) {
      for (let i = 0; i < rows.length; i++) {
        if (i === 0) {
          rows[i]._cpu_pct = null;
          rows[i]._net_tx_mbps = null;
          rows[i]._net_rx_mbps = null;
          continue;
        }
        const prev = rows[i - 1];
        const dt = (new Date(rows[i].timestamp) - new Date(prev.timestamp)) / 1000;
        if (dt <= 0) {
          rows[i]._cpu_pct = null;
          rows[i]._net_tx_mbps = null;
          rows[i]._net_rx_mbps = null;
          continue;
        }
        const cpuD = rows[i].cpu_used_cumulative_seconds - prev.cpu_used_cumulative_seconds;
        rows[i]._cpu_pct = (cpuD >= 0 && rows[i].cpu_nominal > 0) ? (cpuD / dt / rows[i].cpu_nominal) * 100 : null;
        const txD = rows[i].network_tx_bytes - prev.network_tx_bytes;
        rows[i]._net_tx_mbps = txD >= 0 ? (txD * 8 / 1e6) / dt : null;
        const rxD = rows[i].network_rx_bytes - prev.network_rx_bytes;
        rows[i]._net_rx_mbps = rxD >= 0 ? (rxD * 8 / 1e6) / dt : null;
      }
    }

    // Populate host filter
    const hosts = [...new Set(Object.values(vmHost))].sort();
    const hostSel = document.getElementById('hostFilter');
    for (const h of hosts) {
      const o = document.createElement('option');
      o.value = h; o.textContent = h;
      hostSel.appendChild(o);
    }

    // Compute last values for sorting
    function lastVal(vm, key) {
      const rows = byVM[vm];
      for (let i = rows.length - 1; i >= 0; i--) {
        if (rows[i][key] != null) return rows[i][key];
      }
      return 0;
    }

    function fmtVal(v, chart) {
      if (chart === 'CPU %') return v.toFixed(1) + '%';
      if (chart === 'Network (Mbps)') return v.toFixed(2) + ' Mbps';
      if (v >= 1e12) return (v/1e12).toFixed(1) + 'T';
      if (v >= 1e9) return (v/1e9).toFixed(1) + 'G';
      if (v >= 1e6) return (v/1e6).toFixed(1) + 'M';
      if (v >= 1e3) return (v/1e3).toFixed(1) + 'K';
      return v.toFixed(0);
    }

    const chartDefs = [
      { title: 'Disk', series: [
        {key: 'disk_size_bytes', label: 'Provisioned', ref: true},
        {key: 'disk_logical_used_bytes', label: 'Uncompressed'},
        {key: 'disk_used_bytes', label: 'On-Disk'},
      ]},
      { title: 'Memory RSS', series: [
        {key: 'memory_nominal_bytes', label: 'Nominal', ref: true},
        {key: 'memory_rss_bytes', label: 'RSS'},
      ]},
      { title: 'Memory Swap', series: [
        {key: 'memory_nominal_bytes', label: 'Nominal', ref: true},
        {key: 'memory_swap_bytes', label: 'Swap'},
      ]},
      { title: 'CPU %', yFloor: 100, series: [
        {key: '_cpu_100', label: 'Nominal', ref: true, labelKey: 'cpu_nominal'},
        {key: '_cpu_pct', label: 'Used', fmtPct: true},
      ]},
      { title: 'Network (Mbps)', yFloor: 100, series: [
        {key: '_net_tx_mbps', label: 'TX'},
        {key: '_net_rx_mbps', label: 'RX'},
      ]},
    ];
    const chartNames = chartDefs.map(c => c.title);

    let currentView = null;

    function render() {
      const hostF = hostSel.value;
      const nameF = document.getElementById('nameFilter').value.toLowerCase();
      const sortKey = document.getElementById('sortBy').value;
      const sortDesc = document.getElementById('sortDesc').checked;

      let vms = Object.keys(byVM);
      if (hostF) vms = vms.filter(v => vmHost[v] === hostF);
      if (nameF) vms = vms.filter(v => v.toLowerCase().includes(nameF));

      if (sortKey === 'name') {
        vms.sort();
        if (sortDesc) vms.reverse();
      } else {
        vms.sort((a, b) => {
          const va = lastVal(a, sortKey), vb = lastVal(b, sortKey);
          return sortDesc ? vb - va : va - vb;
        });
      }

      // Build long-format data with vm_label (name + host)
      const long = [];
      for (const vm of vms) {
        const rows = byVM[vm];
        const label = vm + '\n' + vmHost[vm];
        for (const row of rows) {
          // Add _cpu_100 as constant 100% reference
          row._cpu_100 = 100;
          for (const cd of chartDefs) {
            for (const s of cd.series) {
              const v = row[s.key];
              if (v == null) continue;
              const lbl = s.labelKey ? s.label + ' ' + row[s.labelKey].toFixed(1)
                        : s.label + ' ' + fmtVal(v, cd.title);
              long.push({timestamp: row.timestamp, vm_label: label, chart: cd.title,
                         series: s.label, value: v, ref: s.ref ? 'ref' : 'metric',
                         yFloor: cd.yFloor || 0, _lbl: lbl,
                         _tip: s.label + ': ' + fmtVal(v, cd.title)});
            }
            // Add y-floor invisible point for charts that need a minimum y range
            if (cd.yFloor != null) {
              long.push({timestamp: row.timestamp, vm_label: label, chart: cd.title,
                         series: '_yfloor', value: cd.yFloor, ref: 'floor'});
            }
          }
        }
      }

      if (long.length === 0) {
        document.getElementById('vis').innerHTML = '<p style="color:#666">No matching VMs.</p>';
        return;
      }

      const vmLabels = vms.map(v => v + '\n' + vmHost[v]);

      const spec = {
        "$schema": "https://vega.github.io/schema/vega-lite/v5.json",
        data: {values: long},
        facet: {
          row: {field: "vm_label", type: "nominal", sort: vmLabels,
                header: {labelAngle: 0, labelAlign: "left", labelFontSize: 9, labelPadding: 4, labelLineHeight: 13}},
          column: {field: "chart", type: "nominal", sort: chartNames,
                   header: {labelFontSize: 10, labelPadding: 2}},
        },
        spec: {
          width: 150,
          height: 35,
          layer: [
            {
              transform: [{filter: "datum.series !== '_yfloor'"}],
              mark: {type: "line", strokeWidth: 1.5},
              encoding: {
                x: {field: "timestamp", type: "temporal", axis: null},
                y: {field: "value", type: "quantitative", axis: null, scale: {zero: true}},
                color: {field: "series", type: "nominal", legend: null},
                strokeDash: {field: "ref", type: "nominal",
                  scale: {domain: ["metric", "ref"], range: [[1,0], [6,3]]}, legend: null},
                strokeOpacity: {field: "ref", type: "nominal",
                  scale: {domain: ["metric", "ref"], range: [1, 0.45]}, legend: null},
              },
            },
            {
              transform: [{filter: "datum.series !== '_yfloor'"}],
              mark: {type: "point", opacity: 0, size: 100},
              encoding: {
                x: {field: "timestamp", type: "temporal"},
                y: {field: "value", type: "quantitative"},
                tooltip: [
                  {field: "_tip", type: "nominal", title: "Value"},
                  {field: "timestamp", type: "temporal", title: "Time", format: "%H:%M"},
                ],
              },
            },
            {
              transform: [
                {filter: "datum.series !== '_yfloor'"},
                {aggregate: [{op: "argmax", field: "timestamp", as: "last"}], groupby: ["vm_label", "chart", "series"]},
                {calculate: "datum.last.value", as: "lastValue"},
                {calculate: "datum.last.timestamp", as: "timestamp"},
                {calculate: "datum.last.yFloor || 0", as: "yFloor"},
                {calculate: "datum.last._lbl", as: "label"},
                {window: [{op: "row_number", as: "rank"}], groupby: ["vm_label", "chart"],
                 sort: [{field: "lastValue", order: "descending"}]},
                {joinaggregate: [{op: "max", field: "lastValue", as: "cellMax"}], groupby: ["vm_label", "chart"]},
                {calculate: "max(datum.cellMax, datum.yFloor) * max(1 - (datum.rank - 1) * 0.3, 0.05)", as: "labelY"},
              ],
              mark: {type: "text", align: "right", fontSize: 8, dx: -2, baseline: "middle"},
              encoding: {
                x: {field: "timestamp", type: "temporal"},
                y: {field: "labelY", type: "quantitative"},
                text: {field: "label", type: "nominal"},
                color: {field: "series", type: "nominal", legend: null},
              },
            },
            {
              transform: [{filter: "datum.series === '_yfloor'"}],
              mark: {type: "point", size: 0, opacity: 0},
              encoding: {
                x: {field: "timestamp", type: "temporal"},
                y: {field: "value", type: "quantitative"},
              },
            },
          ],
        },
        resolve: {scale: {y: "independent", color: "independent"}},
        config: {view: {stroke: "#ddd"}, padding: 2},
      };

      if (currentView) { currentView.finalize(); currentView = null; }
      vegaEmbed('#vis', spec, {actions: false}).then(r => { currentView = r.view; });
    }

    loadingEl.hidden = true;
    hostSel.addEventListener('change', render);
    document.getElementById('nameFilter').addEventListener('input', render);
    document.getElementById('sortBy').addEventListener('change', render);
    document.getElementById('sortDesc').addEventListener('change', render);
    render();
  } catch (e) {
    loadingEl.hidden = true;
    errorEl.textContent = 'Error: ' + e.message;
  }
})();
</script>
</body>
</html>
`
