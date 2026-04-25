package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// DaemonMetric describes a single metric for a daemon.
type DaemonMetric struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Query       string       `json:"-"`                     // PromQL used to fetch the data; not exposed to the client.
	Sparkline   [][2]float64 `json:"sparkline,omitempty"`   // [[unix_ts, value], ...]
	Current     *float64     `json:"current"`               // latest value, nil = unknown
	FloorValue  *float64     `json:"floor_value,omitempty"` // suggested alert floor, nil = no suggestion
	Unit        string       `json:"unit"`                  // human label: "req/s", "bytes/s", "%", "count"
}

// DaemonHealth describes the top metrics for one daemon type.
type DaemonHealth struct {
	Daemon  string         `json:"daemon"`
	Metrics []DaemonMetric `json:"metrics"`
}

// daemonDefs returns the metric definitions for each daemon.
// The PromQL queries are designed to produce a single aggregate
// time-series per daemon type (summed across instances).
// When stage is non-empty, queries are filtered to that Prometheus
// stage label (e.g. "production").
func daemonDefs(stage string) []DaemonHealth {
	sf := ""
	if stage != "" {
		sf = `,stage="` + stage + `"`
	}
	return []DaemonHealth{
		{
			Daemon: "exeprox",
			Metrics: []DaemonMetric{
				{
					Name:        "HTTP Request Rate",
					Description: "Total HTTP requests/s across all exeprox instances",
					Query:       `sum(rate(http_requests_total{job="exeprox"` + sf + `}[5m]))`,
					Unit:        "req/s",
				},
				{
					Name:        "Proxy Bytes Rate",
					Description: "Bytes/s proxied (in+out) across all instances",
					Query:       `sum(rate(proxy_bytes_total{job="exeprox"` + sf + `}[5m]))`,
					Unit:        "bytes/s",
				},
				{
					Name:        "Active Copy Sessions",
					Description: "In-flight exepipe copy sessions (SSH/port-forward)",
					Query:       `sum(copy_sessions_in_flight{job="exeprox"` + sf + `})`,
					Unit:        "count",
				},
			},
		},
		{
			Daemon: "exed",
			Metrics: []DaemonMetric{
				{
					Name:        "SSH Connections",
					Description: "Current active SSH connections to exed",
					Query:       `sum(ssh_connections_current{job="exed"` + sf + `})`,
					Unit:        "count",
				},
				{
					Name:        "SSH Connection Rate",
					Description: "New SSH connections/s (success+rejected)",
					Query:       `sum(rate(ssh_connections_total{job="exed"` + sf + `}[5m]))`,
					Unit:        "conn/s",
				},
				{
					Name:        "HTTP Request Rate",
					Description: "HTTP requests/s to exed (API, health, webhooks)",
					Query:       `sum(rate(http_requests_total{job="exed"` + sf + `}[5m]))`,
					Unit:        "req/s",
				},
			},
		},
		{
			Daemon: "exelet",
			Metrics: []DaemonMetric{
				{
					Name:        "gRPC Request Rate",
					Description: "gRPC requests/s handled across all exelets",
					Query:       `sum(rate(grpc_server_handled_total{job="exelet"` + sf + `}[5m]))`,
					Unit:        "req/s",
				},
				{
					Name:        "Gateway Requests",
					Description: "LLM gateway proxy requests/s across all exelets",
					Query:       `sum(rate(exelet_metadata_gateway_requests_total{job="exelet"` + sf + `}[5m]))`,
					Unit:        "req/s",
				},
				{
					Name:        "Ready Instances",
					Description: "Number of exelets reporting ready=1",
					Query:       `sum(exelet_ready{job="exelet"` + sf + `})`,
					Unit:        "count",
				},
			},
		},
		{
			Daemon: "metricsd",
			Metrics: []DaemonMetric{
				{
					Name:        "Rows Inserted Rate",
					Description: "Metric rows inserted/s into DuckDB",
					Query:       `sum(rate(metricsd_rows_inserted_total{job="metricsd"` + sf + `}[5m]))`,
					Unit:        "rows/s",
				},
			},
		},
	}
}

// floatPtr returns a pointer to v. Useful for optional DaemonMetric fields.
// Currently unused — floor values are not yet configured — but retained for
// when we enable floor-based alerting.
//
//nolint:unused
func floatPtr(v float64) *float64 { return &v }

// promStage returns the Prometheus stage label for an environment string.
// Prometheus uses "production" while --environment uses "prod".
func promStage(env string) string {
	if env == "prod" {
		return "production"
	}
	return env
}

// HandleDaemonHealth handles GET /api/v1/daemons/health — returns the top
// metrics for each daemon with 1h sparkline data.
func (h *Handlers) HandleDaemonHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	defs := daemonDefs(promStage(h.environment))

	// Flatten all queries to run in parallel.
	type qResult struct {
		di, mi  int // daemon index, metric index
		points  [][2]float64
		current *float64
		err     error
	}

	var totalQueries int
	for _, d := range defs {
		totalQueries += len(d.Metrics)
	}

	results := make([]qResult, totalQueries)
	var wg sync.WaitGroup
	idx := 0
	for di, d := range defs {
		for mi, m := range d.Metrics {
			wg.Add(1)
			go func(idx, di, mi int, query string) {
				defer wg.Done()
				// Use aggregate range query (not keyed by instance).
				merged, err := promAggregateQueryRange(ctx, h.log, query, time.Hour, time.Minute)
				if err != nil {
					results[idx] = qResult{di: di, mi: mi, err: err}
					return
				}
				var cur *float64
				if len(merged) > 0 {
					v := merged[len(merged)-1][1]
					cur = &v
				}
				results[idx] = qResult{di: di, mi: mi, points: merged, current: cur}
			}(idx, di, mi, m.Query)
			idx++
		}
	}
	wg.Wait()

	// Apply results back.
	for _, res := range results {
		if res.err != nil {
			h.log.Error("daemon metric query failed",
				"daemon", defs[res.di].Daemon,
				"metric", defs[res.di].Metrics[res.mi].Name,
				"error", res.err)
			continue
		}
		defs[res.di].Metrics[res.mi].Sparkline = res.points
		defs[res.di].Metrics[res.mi].Current = res.current
	}

	writeJSON(w, defs)
}

// HandleDaemonHealthSummary handles GET /api/v1/daemons/summary — returns
// a compact current-values-only view (no sparklines) suitable for stat cards.
func (h *Handlers) HandleDaemonHealthSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	defs := daemonDefs(promStage(h.environment))

	type qResult struct {
		di, mi  int
		current *float64
		err     error
	}

	var totalQueries int
	for _, d := range defs {
		totalQueries += len(d.Metrics)
	}

	results := make([]qResult, totalQueries)
	var wg sync.WaitGroup
	idx := 0
	for di, d := range defs {
		for mi, m := range d.Metrics {
			wg.Add(1)
			go func(idx, di, mi int, query string) {
				defer wg.Done()
				data, err := promAggregateQuery(ctx, h.log, query)
				if err != nil {
					results[idx] = qResult{di: di, mi: mi, err: err}
					return
				}
				results[idx] = qResult{di: di, mi: mi, current: data}
			}(idx, di, mi, m.Query)
			idx++
		}
	}
	wg.Wait()

	for _, res := range results {
		if res.err != nil {
			h.log.Error("daemon summary query failed",
				"daemon", defs[res.di].Daemon,
				"metric", defs[res.di].Metrics[res.mi].Name,
				"error", res.err)
			continue
		}
		defs[res.di].Metrics[res.mi].Current = res.current
		defs[res.di].Metrics[res.mi].Sparkline = nil // omit from summary
	}

	// Build compact response: daemon + metric name/current/unit.
	type metricSummary struct {
		Name    string   `json:"name"`
		Current *float64 `json:"current"`
		Unit    string   `json:"unit"`
	}
	type daemonSummary struct {
		Daemon  string          `json:"daemon"`
		Metrics []metricSummary `json:"metrics"`
	}

	out := make([]daemonSummary, len(defs))
	for i, d := range defs {
		ds := daemonSummary{Daemon: d.Daemon}
		for _, m := range d.Metrics {
			ds.Metrics = append(ds.Metrics, metricSummary{
				Name:    m.Name,
				Current: m.Current,
				Unit:    m.Unit,
			})
		}
		out[i] = ds
	}

	writeJSON(w, out)
}

// formatMetricValue returns a human-friendly string for a metric value.
func formatMetricValue(v float64, unit string) string {
	switch unit {
	case "bytes/s":
		if v >= 1e9 {
			return fmt.Sprintf("%.1f GB/s", v/1e9)
		}
		if v >= 1e6 {
			return fmt.Sprintf("%.1f MB/s", v/1e6)
		}
		if v >= 1e3 {
			return fmt.Sprintf("%.1f KB/s", v/1e3)
		}
		return fmt.Sprintf("%.0f B/s", v)
	case "req/s", "conn/s", "rows/s", "ops/s":
		if v >= 1000 {
			return fmt.Sprintf("%.1fk %s", v/1000, unit)
		}
		return fmt.Sprintf("%.2f %s", v, unit)
	case "seconds":
		if v >= 1 {
			return fmt.Sprintf("%.2fs", v)
		}
		return fmt.Sprintf("%.0fms", v*1000)
	case "cores":
		return fmt.Sprintf("%.2f cores", v)
	case "count":
		return fmt.Sprintf("%.0f", v)
	default:
		return fmt.Sprintf("%.2f %s", v, unit)
	}
}
