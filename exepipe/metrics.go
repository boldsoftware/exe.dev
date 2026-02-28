package exepipe

import (
	"github.com/prometheus/client_golang/prometheus"
)

// metrics tracks data for execopy.
type metrics struct {
	sessionsTotal    *prometheus.CounterVec
	sessionsInFlight *prometheus.GaugeVec
	bytesTotal       *prometheus.CounterVec
}

// newMetrics creates and registers execopy metrics.
func newMetrics(registry *prometheus.Registry) *metrics {
	labels := []string{"type"}
	m := &metrics{
		sessionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "copy_sessions_total",
				Help: "Total number of execopy sessions.",
			},
			labels,
		),
		sessionsInFlight: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "copy_sessions_in_flight",
				Help: "Current number of copy sessions.",
			},
			labels,
		),
		bytesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "copy_bytes_total",
				Help: "total number of bytes copied.",
			},
			labels,
		),
	}
	registry.MustRegister(m.sessionsTotal, m.sessionsInFlight, m.bytesTotal)
	return m
}

// StartSession records a new session.
func (m *metrics) StartSession(typ string) {
	m.sessionsTotal.WithLabelValues(typ).Inc()
	m.sessionsInFlight.WithLabelValues(typ).Inc()
}

// StopSession records that a session has stopped.
func (m *metrics) StopSession(typ string) {
	m.sessionsInFlight.WithLabelValues(typ).Dec()
}

// AddBytes increments the bytes counter.
func (m *metrics) AddBytes(typ string, n int) {
	m.bytesTotal.WithLabelValues(typ).Add(float64(n))
}
