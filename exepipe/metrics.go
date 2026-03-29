package exepipe

import (
	"github.com/prometheus/client_golang/prometheus"
)

// metrics tracks data for execopy.
type metrics struct {
	registry         *prometheus.Registry
	sessionsTotal    *prometheus.CounterVec
	sessionsInFlight *prometheus.GaugeVec
	bytesTotal       *prometheus.CounterVec
}

// newMetrics creates and registers execopy metrics.
func newMetrics(registry *prometheus.Registry) *metrics {
	labels := []string{"type"}
	m := &metrics{
		registry: registry,
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
