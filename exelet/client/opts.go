package client

import (
	"log/slog"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/prometheus/client_golang/prometheus"
)

type ClientOpt func(c *ClientConfig)

// WithInsecure is an opt that sets insecure for the client
func WithInsecure() ClientOpt {
	return func(c *ClientConfig) {
		c.Insecure = true
	}
}

// WithLogger sets the logger for gRPC request logging
func WithLogger(log *slog.Logger) ClientOpt {
	return func(c *ClientConfig) {
		c.Logger = log
	}
}

// WithMetrics sets up gRPC metrics using the provided registry
func WithMetrics(registry *prometheus.Registry) ClientOpt {
	return func(c *ClientConfig) {
		clientMetrics := grpcprom.NewClientMetrics(
			grpcprom.WithClientHandlingTimeHistogram(
				grpcprom.WithHistogramBuckets([]float64{0.01, 0.1, 0.3, 0.6, 1, 1.4, 2, 3, 6, 9, 20, 30, 60, 90}),
			),
		)
		registry.MustRegister(clientMetrics)
		c.Metrics = clientMetrics
	}
}
