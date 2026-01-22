package replication

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds Prometheus metrics for the replication service
type Metrics struct {
	bytesTotal           *prometheus.CounterVec
	operationsTotal      *prometheus.CounterVec
	durationSeconds      *prometheus.HistogramVec
	queueSize            prometheus.Gauge
	lastSuccessTimestamp prometheus.Gauge
}

// NewMetrics creates and registers Prometheus metrics
func NewMetrics(registry *prometheus.Registry) *Metrics {
	m := &Metrics{
		bytesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "exelet_replication_bytes_total",
				Help: "Total bytes transferred during replication",
			},
			[]string{"volume_id", "target_type"},
		),
		operationsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "exelet_replication_operations_total",
				Help: "Total replication operations by status",
			},
			[]string{"status", "target_type"}, // status: success, failed
		),
		durationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "exelet_replication_duration_seconds",
				Help:    "Time taken per replication operation",
				Buckets: prometheus.ExponentialBuckets(1, 2, 15), // 1s to ~9 hours
			},
			[]string{"volume_id", "target_type"},
		),
		queueSize: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "exelet_replication_queue_size",
				Help: "Current number of volumes in replication queue",
			},
		),
		lastSuccessTimestamp: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "exelet_replication_last_success_timestamp",
				Help: "Unix timestamp of last successful replication cycle",
			},
		),
	}

	if registry != nil {
		registry.MustRegister(
			m.bytesTotal,
			m.operationsTotal,
			m.durationSeconds,
			m.queueSize,
			m.lastSuccessTimestamp,
		)
	}

	return m
}

// RecordSuccess records a successful replication operation
func (m *Metrics) RecordSuccess(volumeID, targetType string, bytes int64, durationSeconds float64) {
	m.bytesTotal.WithLabelValues(volumeID, targetType).Add(float64(bytes))
	m.operationsTotal.WithLabelValues("success", targetType).Inc()
	m.durationSeconds.WithLabelValues(volumeID, targetType).Observe(durationSeconds)
}

// RecordFailure records a failed replication operation
func (m *Metrics) RecordFailure(targetType string) {
	m.operationsTotal.WithLabelValues("failed", targetType).Inc()
}

// SetQueueSize sets the current queue size
func (m *Metrics) SetQueueSize(size int) {
	m.queueSize.Set(float64(size))
}

// SetLastSuccessTimestamp sets the timestamp of the last successful cycle
func (m *Metrics) SetLastSuccessTimestamp(ts float64) {
	m.lastSuccessTimestamp.Set(ts)
}
