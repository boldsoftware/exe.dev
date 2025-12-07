package exelet

import "github.com/prometheus/client_golang/prometheus"

type OptConfig struct {
	IsMaintenance   bool
	MetricsRegistry *prometheus.Registry
}

type ServerOpt func(cfg *OptConfig)

// WithMaintenance is an opt that sets the exelet state in maintenance
func WithMaintenance() ServerOpt {
	return func(cfg *OptConfig) {
		cfg.IsMaintenance = true
	}
}

// WithMetricsRegistry sets the prometheus registry to use for metrics.
func WithMetricsRegistry(registry *prometheus.Registry) ServerOpt {
	return func(cfg *OptConfig) {
		cfg.MetricsRegistry = registry
	}
}
