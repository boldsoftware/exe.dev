package resourcemanager

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"exe.dev/metricsd/types"
)

// MetricsDaemonReporter periodically collects VM metrics and sends them to the metrics daemon.
type MetricsDaemonReporter struct {
	url      string
	host     string // exelet host name (container host where VMs run)
	interval time.Duration
	client   *http.Client
	log      *slog.Logger

	// collectFn is called to collect metrics from all VMs
	collectFn func(host string) []types.Metric

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewMetricsDaemonReporter creates a new MetricsDaemonReporter.
func NewMetricsDaemonReporter(
	url string,
	host string,
	interval time.Duration,
	log *slog.Logger,
	collectFn func(host string) []types.Metric,
) *MetricsDaemonReporter {
	return &MetricsDaemonReporter{
		url:      url,
		host:     host,
		interval: interval,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		log:       log,
		collectFn: collectFn,
	}
}

// Start begins the periodic metrics reporting goroutine.
func (r *MetricsDaemonReporter) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cancel != nil {
		return // already started
	}

	reporterCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.run(reporterCtx)
	}()

	r.log.InfoContext(ctx, "metrics daemon reporter started",
		"url", r.url,
		"interval", r.interval)
}

// Stop stops the periodic metrics reporting goroutine.
func (r *MetricsDaemonReporter) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()

	if cancel == nil {
		return
	}

	cancel()
	r.wg.Wait()
}

func (r *MetricsDaemonReporter) run(ctx context.Context) {
	// Initial jittered delay to spread out reporters from multiple exelets
	initialJitter := r.jitteredInterval()
	select {
	case <-ctx.Done():
		return
	case <-time.After(initialJitter):
	}

	// Send initial metrics
	r.sendMetrics(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.interval):
			r.sendMetrics(ctx)
		}
	}
}

// jitteredInterval returns the base interval with ±20% random jitter.
func (r *MetricsDaemonReporter) jitteredInterval() time.Duration {
	// Random value in range [-0.2, 0.2]
	jitterFactor := (rand.Float64() - 0.5) * 0.4
	jitter := time.Duration(float64(r.interval) * jitterFactor)
	return r.interval + jitter
}

func (r *MetricsDaemonReporter) sendMetrics(ctx context.Context) {
	start := time.Now()
	metrics := r.collectFn(r.host)
	if len(metrics) == 0 {
		return
	}

	batch := types.MetricsBatch{Metrics: metrics}

	body, err := json.Marshal(batch)
	if err != nil {
		r.log.ErrorContext(ctx, "metrics daemon reporter: failed to marshal metrics", "error", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url+"/write", bytes.NewReader(body))
	if err != nil {
		r.log.ErrorContext(ctx, "metrics daemon reporter: failed to create request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		r.log.WarnContext(ctx, "metrics daemon reporter: failed to send metrics", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.log.WarnContext(ctx, "metrics daemon reporter: unexpected response",
			"status", resp.StatusCode)
		return
	}

	r.log.DebugContext(ctx, "metrics daemon reporter: sent metrics",
		"count", len(metrics),
		"duration", time.Since(start))
}

// collectMetricsFromRM collects metrics from the ResourceManager's usageState.
// This is used as the collectFn for the MetricsDaemonReporter.
func (m *ResourceManager) collectMetricsFromRM(host string) []types.Metric {
	now := time.Now().UTC()

	m.usageMu.Lock()
	defer m.usageMu.Unlock()

	if len(m.usageState) == 0 {
		return nil
	}

	// Get VM configs for nominal values (CPUs, memory)
	ctx := context.Background()
	instances, err := m.context.ComputeService.Instances(ctx)
	if err != nil {
		m.log.ErrorContext(ctx, "metrics daemon reporter: failed to list instances", "error", err)
		return nil
	}

	// Build a map of instance ID -> VMConfig for efficient lookup
	type vmConfig struct {
		cpus   uint64
		memory uint64
	}
	configMap := make(map[string]vmConfig, len(instances))
	for _, inst := range instances {
		if cfg := inst.GetVMConfig(); cfg != nil {
			configMap[inst.GetID()] = vmConfig{
				cpus:   cfg.GetCPUs(),
				memory: cfg.GetMemory(),
			}
		}
	}

	metrics := make([]types.Metric, 0, len(m.usageState))
	for id, state := range m.usageState {
		var nominalCPUs float64
		var nominalMemory int64

		if cfg, ok := configMap[id]; ok {
			nominalCPUs = float64(cfg.cpus)
			nominalMemory = int64(cfg.memory)
		}

		// Disk metrics come from ZFS:
		// - DiskSizeBytes: volsize (provisioned size)
		// - DiskUsedBytes: used (actual compressed bytes on disk)
		// - DiskLogicalUsedBytes: logicalused (uncompressed logical usage)
		metric := types.Metric{
			Timestamp:             now,
			Host:                  host,
			VMName:                state.name,
			ResourceGroup:         state.groupID,
			DiskSizeBytes:         int64(state.diskVolsizeBytes),
			DiskUsedBytes:         int64(state.diskBytes),
			DiskLogicalUsedBytes:  int64(state.diskLogicalBytes),
			MemoryNominalBytes:    nominalMemory,
			MemoryRSSBytes:        int64(state.memoryBytes),
			MemorySwapBytes:       int64(state.swapBytes),
			CPUUsedCumulativeSecs: state.cpuSeconds,
			CPUNominal:            nominalCPUs,
			NetworkTXBytes:        int64(state.netTxBytes),
			NetworkRXBytes:        int64(state.netRxBytes),
		}

		metrics = append(metrics, metric)
	}

	return metrics
}
