package exelet

import "github.com/prometheus/client_golang/prometheus"

// ExeletMetrics holds exelet server metrics
type ExeletMetrics struct {
	// gRPC metrics are registered separately via grpcMetrics in grpc_interceptors.go
}

// NewExeletMetrics creates and registers exelet metrics
func NewExeletMetrics(registry *prometheus.Registry) *ExeletMetrics {
	return &ExeletMetrics{}
}
