package resourcemanager

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// metricsState tracks what has been reported to Prometheus counters.
// This is needed because Prometheus counters only accept Add() operations,
// so we must calculate deltas from the cumulative values in vmUsageState.
type metricsState struct {
	name               string  // VM name for label cleanup on name change
	reportedCPUSeconds float64 // last value added to CPU counter
	reportedNetRxBytes uint64  // last value added to network RX counter
	reportedNetTxBytes uint64  // last value added to network TX counter
}

// prometheusMetrics holds all Prometheus metrics for the resource manager.
type prometheusMetrics struct {
	cpuCounter   *prometheus.CounterVec
	netRxCounter *prometheus.CounterVec
	netTxCounter *prometheus.CounterVec
	diskGauge    *prometheus.GaugeVec
	memoryGauge  *prometheus.GaugeVec
	swapGauge    *prometheus.GaugeVec

	mu    sync.Mutex
	state map[string]*metricsState // vm_id -> state
}

// newPrometheusMetrics creates and registers Prometheus metrics with the registry.
func newPrometheusMetrics(registry *prometheus.Registry) *prometheusMetrics {
	cpuCounter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "exelet",
			Subsystem: "vm",
			Name:      "cpu_seconds_total",
			Help:      "Total CPU seconds consumed by the VM process.",
		},
		[]string{"vm_id", "vm_name"},
	)
	registry.MustRegister(cpuCounter)

	netRxCounter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "exelet",
			Subsystem: "vm",
			Name:      "net_rx_bytes_total",
			Help:      "Total network bytes received by the VM.",
		},
		[]string{"vm_id", "vm_name"},
	)
	registry.MustRegister(netRxCounter)

	netTxCounter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "exelet",
			Subsystem: "vm",
			Name:      "net_tx_bytes_total",
			Help:      "Total network bytes transmitted by the VM.",
		},
		[]string{"vm_id", "vm_name"},
	)
	registry.MustRegister(netTxCounter)

	diskGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "exelet",
			Subsystem: "vm",
			Name:      "disk_used_bytes",
			Help:      "Disk space used by the VM in bytes.",
		},
		[]string{"vm_id", "vm_name"},
	)
	registry.MustRegister(diskGauge)

	memoryGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "exelet",
			Subsystem: "vm",
			Name:      "memory_bytes",
			Help:      "Current memory usage (cgroup memory.current) of the VM in bytes.",
		},
		[]string{"vm_id", "vm_name"},
	)
	registry.MustRegister(memoryGauge)

	swapGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "exelet",
			Subsystem: "vm",
			Name:      "swap_bytes",
			Help:      "Current swap usage (cgroup memory.swap.current) of the VM in bytes.",
		},
		[]string{"vm_id", "vm_name"},
	)
	registry.MustRegister(swapGauge)

	return &prometheusMetrics{
		cpuCounter:   cpuCounter,
		netRxCounter: netRxCounter,
		netTxCounter: netTxCounter,
		diskGauge:    diskGauge,
		memoryGauge:  memoryGauge,
		swapGauge:    swapGauge,
		state:        make(map[string]*metricsState),
	}
}

// update updates Prometheus metrics for a VM based on the current usage state.
func (p *prometheusMetrics) update(id, name string, usage *vmUsageState) {
	p.mu.Lock()
	defer p.mu.Unlock()

	state, exists := p.state[id]
	if !exists || state.name != name {
		// First observation or name changed - delete old metrics if name changed
		if exists && state.name != name {
			p.cpuCounter.DeleteLabelValues(id, state.name)
			p.netRxCounter.DeleteLabelValues(id, state.name)
			p.netTxCounter.DeleteLabelValues(id, state.name)
			p.diskGauge.DeleteLabelValues(id, state.name)
			p.memoryGauge.DeleteLabelValues(id, state.name)
			p.swapGauge.DeleteLabelValues(id, state.name)
		}
		// Initialize with current values - add full amount on first observation
		p.state[id] = &metricsState{
			name:               name,
			reportedCPUSeconds: usage.cpuSeconds,
			reportedNetRxBytes: usage.netRxBytes,
			reportedNetTxBytes: usage.netTxBytes,
		}
		// Add the initial values to counters
		if usage.cpuSeconds > 0 {
			p.cpuCounter.WithLabelValues(id, name).Add(usage.cpuSeconds)
		}
		if usage.netRxBytes > 0 {
			p.netRxCounter.WithLabelValues(id, name).Add(float64(usage.netRxBytes))
		}
		if usage.netTxBytes > 0 {
			p.netTxCounter.WithLabelValues(id, name).Add(float64(usage.netTxBytes))
		}
		// Set gauges directly
		p.diskGauge.WithLabelValues(id, name).Set(float64(usage.diskBytes))
		p.memoryGauge.WithLabelValues(id, name).Set(float64(usage.memoryBytes))
		p.swapGauge.WithLabelValues(id, name).Set(float64(usage.swapBytes))
		return
	}

	// Calculate and add deltas for counters
	cpuDelta := usage.cpuSeconds - state.reportedCPUSeconds
	if cpuDelta < 0 {
		// Counter reset - use full value
		cpuDelta = usage.cpuSeconds
	}
	if cpuDelta > 0 {
		p.cpuCounter.WithLabelValues(id, name).Add(cpuDelta)
		state.reportedCPUSeconds = usage.cpuSeconds
	}

	// Network RX delta
	var rxDelta uint64
	if usage.netRxBytes >= state.reportedNetRxBytes {
		rxDelta = usage.netRxBytes - state.reportedNetRxBytes
	} else {
		// Counter wrapped or reset
		rxDelta = usage.netRxBytes
	}
	if rxDelta > 0 {
		p.netRxCounter.WithLabelValues(id, name).Add(float64(rxDelta))
		state.reportedNetRxBytes = usage.netRxBytes
	}

	// Network TX delta
	var txDelta uint64
	if usage.netTxBytes >= state.reportedNetTxBytes {
		txDelta = usage.netTxBytes - state.reportedNetTxBytes
	} else {
		// Counter wrapped or reset
		txDelta = usage.netTxBytes
	}
	if txDelta > 0 {
		p.netTxCounter.WithLabelValues(id, name).Add(float64(txDelta))
		state.reportedNetTxBytes = usage.netTxBytes
	}

	// Set gauges directly (they represent current state, not cumulative)
	p.diskGauge.WithLabelValues(id, name).Set(float64(usage.diskBytes))
	p.memoryGauge.WithLabelValues(id, name).Set(float64(usage.memoryBytes))
	p.swapGauge.WithLabelValues(id, name).Set(float64(usage.swapBytes))
}

// delete removes metrics for a VM.
func (p *prometheusMetrics) delete(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	state, exists := p.state[id]
	if !exists {
		return
	}

	p.cpuCounter.DeleteLabelValues(id, state.name)
	p.netRxCounter.DeleteLabelValues(id, state.name)
	p.netTxCounter.DeleteLabelValues(id, state.name)
	p.diskGauge.DeleteLabelValues(id, state.name)
	p.memoryGauge.DeleteLabelValues(id, state.name)
	p.swapGauge.DeleteLabelValues(id, state.name)
	delete(p.state, id)
}
