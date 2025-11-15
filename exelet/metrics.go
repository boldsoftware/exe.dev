package exelet

import "github.com/prometheus/client_golang/prometheus"

// ExeletMetrics holds exelet server metrics
type ExeletMetrics struct {
	// TODO: add gRPC metrics using https://github.com/grpc-ecosystem/go-grpc-middleware
}

// NewExeletMetrics creates and registers exelet metrics
func NewExeletMetrics(registry *prometheus.Registry) *ExeletMetrics {
	return &ExeletMetrics{}
}
