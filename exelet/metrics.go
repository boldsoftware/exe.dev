package exelet

import "github.com/prometheus/client_golang/prometheus"

// ExeletMetrics holds exelet server metrics
type ExeletMetrics struct {
	ready prometheus.Gauge
}

// NewExeletMetrics creates and registers exelet metrics
func NewExeletMetrics(registry *prometheus.Registry) *ExeletMetrics {
	ready := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "exelet",
		Name:      "ready",
		Help:      "1 when the exelet has finished starting all services, 0 during startup.",
	})
	registry.MustRegister(ready)
	return &ExeletMetrics{
		ready: ready,
	}
}
