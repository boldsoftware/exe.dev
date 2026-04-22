package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const prometheusBaseURL = "http://mon:9090"

// HostMetrics is the per-host metrics returned by the hosts API.
type HostMetrics struct {
	Instance       string   `json:"instance"`
	Hostname       string   `json:"hostname"`
	Stage          string   `json:"stage"`
	Role           string   `json:"role"`
	Region         string   `json:"region"`
	Up             *bool    `json:"up"`              // nil = unknown
	CPUPercent     *float64 `json:"cpu_percent"`     // nil = unknown
	CPUPressure    *float64 `json:"cpu_pressure"`    // PSI some%, nil = unknown
	MemoryPressure *float64 `json:"memory_pressure"` // PSI some%, nil = unknown
	IOPressure     *float64 `json:"io_pressure"`     // PSI some%, nil = unknown
}

// HandleHosts handles GET /api/v1/hosts — returns host-level metrics from Prometheus.
func (h *Handlers) HandleHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Five constant queries, regardless of host count.
	type queryResult struct {
		name string
		data map[string]promValue // keyed by instance
		err  error
	}

	queries := []struct {
		name  string
		query string
	}{
		{"up", `up{job="node"}`},
		{"cpu", `100 * (1 - avg by (instance, job, stage, role) (rate(node_cpu_seconds_total{job="node",mode="idle"}[5m])))`},
		{"cpu_pressure", `rate(node_pressure_cpu_waiting_seconds_total{job="node"}[5m]) * 100`},
		{"memory_pressure", `rate(node_pressure_memory_waiting_seconds_total{job="node"}[5m]) * 100`},
		{"io_pressure", `rate(node_pressure_io_waiting_seconds_total{job="node"}[5m]) * 100`},
	}

	results := make([]queryResult, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func(i int, name, query string) {
			defer wg.Done()
			data, err := promQuery(ctx, h.log, query)
			results[i] = queryResult{name: name, data: data, err: err}
		}(i, q.name, q.query)
	}
	wg.Wait()

	// Check for errors.
	for _, r := range results {
		if r.err != nil {
			h.log.Error("prometheus query failed", "query", r.name, "error", r.err)
		}
	}

	// Build host map from the "up" query (which has all instances).
	hosts := make(map[string]*HostMetrics)
	if results[0].err == nil {
		for inst, pv := range results[0].data {
			hm := &HostMetrics{
				Instance: inst,
				Stage:    pv.labels["stage"],
				Role:     pv.labels["role"],
			}
			hm.Hostname, hm.Region = instanceToHost(inst)
			v := pv.value == 1
			hm.Up = &v
			hosts[inst] = hm
		}
	}

	// Merge CPU%.
	if results[1].err == nil {
		for inst, pv := range results[1].data {
			hm := getOrCreate(hosts, inst, pv.labels)
			hm.CPUPercent = &pv.value
		}
	}

	// Merge CPU pressure.
	if results[2].err == nil {
		for inst, pv := range results[2].data {
			hm := getOrCreate(hosts, inst, pv.labels)
			hm.CPUPressure = &pv.value
		}
	}

	// Merge memory pressure.
	if results[3].err == nil {
		for inst, pv := range results[3].data {
			hm := getOrCreate(hosts, inst, pv.labels)
			hm.MemoryPressure = &pv.value
		}
	}

	// Merge IO pressure.
	if results[4].err == nil {
		for inst, pv := range results[4].data {
			hm := getOrCreate(hosts, inst, pv.labels)
			hm.IOPressure = &pv.value
		}
	}

	// Filter by configured Tailscale tag (e.g. "staging"/"prod"). The
	// Prometheus `stage` label values match the stripped tags.
	stageFilter := make(map[string]bool, len(h.inventory.TagFilter()))
	for _, t := range h.inventory.TagFilter() {
		stageFilter[t] = true
	}

	// Convert to slice.
	out := make([]HostMetrics, 0, len(hosts))
	for _, hm := range hosts {
		if len(stageFilter) > 0 && !stageFilter[hm.Stage] {
			continue
		}
		out = append(out, *hm)
	}

	writeJSON(w, out)
}

func getOrCreate(hosts map[string]*HostMetrics, inst string, labels map[string]string) *HostMetrics {
	hm, ok := hosts[inst]
	if !ok {
		hm = &HostMetrics{
			Instance: inst,
			Stage:    labels["stage"],
			Role:     labels["role"],
		}
		hm.Hostname, hm.Region = instanceToHost(inst)
		hosts[inst] = hm
	}
	return hm
}

// instanceToHost extracts hostname and region from a Prometheus instance label
// like "exelet-fra2-prod-01:9100".
func instanceToHost(instance string) (hostname, region string) {
	hostname = instance
	if i := strings.LastIndex(instance, ":"); i >= 0 {
		hostname = instance[:i]
	}
	// Strip .crocodile-vector.ts.net suffix if present.
	hostname = strings.TrimSuffix(hostname, ".crocodile-vector.ts.net")

	// Extract region from hostname patterns like exelet-<region>-<stage>-N.
	region = extractRegion(hostname)
	return hostname, region
}

func extractRegion(hostname string) string {
	// exelet-<region>-<stage>-N
	if strings.HasPrefix(hostname, "exelet-") {
		parts := strings.Split(hostname, "-")
		if len(parts) >= 4 {
			return parts[1]
		}
	}
	// exeprox-na-<region>-N or exeprox-<region>-na-N
	if strings.HasPrefix(hostname, "exeprox-") {
		parts := strings.Split(hostname, "-")
		if len(parts) >= 4 {
			if parts[1] == "na" {
				return parts[2]
			}
			return parts[1]
		}
	}
	// exe-ctr-NN hosts are in PDX.
	if strings.HasPrefix(hostname, "exe-ctr-") {
		return "pdx"
	}
	return ""
}

// HandleHostSparklines handles GET /api/v1/hosts/sparklines — returns 1h of
// pressure time-series data for all hosts.
func (h *Handlers) HandleHostSparklines(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	type queryResult struct {
		name string
		data map[string][][2]float64 // instance -> [[unix_ts, value], ...]
		err  error
	}

	queries := []struct {
		name  string
		query string
	}{
		{"cpu_pressure", `rate(node_pressure_cpu_waiting_seconds_total{job="node"}[5m]) * 100`},
		{"memory_pressure", `rate(node_pressure_memory_waiting_seconds_total{job="node"}[5m]) * 100`},
		{"io_pressure", `rate(node_pressure_io_waiting_seconds_total{job="node"}[5m]) * 100`},
	}

	results := make([]queryResult, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func(i int, name, query string) {
			defer wg.Done()
			data, err := promQueryRange(ctx, h.log, query, time.Hour, time.Minute)
			results[i] = queryResult{name: name, data: data, err: err}
		}(i, q.name, q.query)
	}
	wg.Wait()

	for _, r := range results {
		if r.err != nil {
			h.log.Error("prometheus range query failed", "query", r.name, "error", r.err)
		}
	}

	// Build response: map[instance] -> {cpu_pressure: [...], ...}
	type sparklineData struct {
		CPUPressure    [][2]float64 `json:"cpu_pressure,omitempty"`
		MemoryPressure [][2]float64 `json:"memory_pressure,omitempty"`
		IOPressure     [][2]float64 `json:"io_pressure,omitempty"`
	}

	out := make(map[string]*sparklineData)
	for i, r := range results {
		if r.err != nil {
			continue
		}
		for inst, points := range r.data {
			sd, ok := out[inst]
			if !ok {
				sd = &sparklineData{}
				out[inst] = sd
			}
			switch i {
			case 0:
				sd.CPUPressure = points
			case 1:
				sd.MemoryPressure = points
			case 2:
				sd.IOPressure = points
			}
		}
	}

	writeJSON(w, out)
}

// promValue holds a single instant query result.
type promValue struct {
	labels map[string]string
	value  float64
}

// promQuery runs an instant query against Prometheus and returns results keyed by instance.
func promQuery(ctx context.Context, _ *slog.Logger, query string) (map[string]promValue, error) {
	u := prometheusBaseURL + "/api/v1/query?query=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query prometheus: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string  `json:"metric"`
				Value  [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if result.Status != "success" {
		return nil, fmt.Errorf("prometheus query status: %s", result.Status)
	}

	out := make(map[string]promValue, len(result.Data.Result))
	for _, r := range result.Data.Result {
		inst := r.Metric["instance"]
		if inst == "" {
			continue
		}
		var valStr string
		if err := json.Unmarshal(r.Value[1], &valStr); err != nil {
			continue
		}
		var val float64
		if _, err := fmt.Sscanf(valStr, "%f", &val); err != nil {
			continue
		}
		out[inst] = promValue{
			labels: r.Metric,
			value:  val,
		}
	}
	return out, nil
}

// promQueryRange runs a range query against Prometheus and returns results keyed by instance.
func promQueryRange(ctx context.Context, _ *slog.Logger, query string, window, step time.Duration) (map[string][][2]float64, error) {
	now := time.Now()
	params := url.Values{
		"query": {query},
		"start": {fmt.Sprintf("%d", now.Add(-window).Unix())},
		"end":   {fmt.Sprintf("%d", now.Unix())},
		"step":  {fmt.Sprintf("%d", int(step.Seconds()))},
	}
	u := prometheusBaseURL + "/api/v1/query_range?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query prometheus: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string    `json:"metric"`
				Values [][2]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if result.Status != "success" {
		return nil, fmt.Errorf("prometheus query status: %s", result.Status)
	}

	out := make(map[string][][2]float64, len(result.Data.Result))
	for _, r := range result.Data.Result {
		inst := r.Metric["instance"]
		if inst == "" {
			continue
		}
		points := make([][2]float64, 0, len(r.Values))
		for _, v := range r.Values {
			var ts float64
			if err := json.Unmarshal(v[0], &ts); err != nil {
				continue
			}
			var valStr string
			if err := json.Unmarshal(v[1], &valStr); err != nil {
				continue
			}
			var val float64
			if _, err := fmt.Sscanf(valStr, "%f", &val); err != nil {
				continue
			}
			points = append(points, [2]float64{ts, val})
		}
		out[inst] = points
	}
	return out, nil
}
