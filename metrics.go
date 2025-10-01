package exe

import "github.com/prometheus/client_golang/prometheus"

// SSHMetrics holds SSH server metrics
// (moved from exe.go to metrics.go to keep main server file lean)
type SSHMetrics struct {
	connectionsTotal   *prometheus.CounterVec
	connectionsCurrent prometheus.Gauge
	authAttempts       *prometheus.CounterVec
	sessionDuration    *prometheus.HistogramVec
}

// NewSSHMetrics creates and registers SSH metrics
func NewSSHMetrics(registry *prometheus.Registry) *SSHMetrics {
	metrics := &SSHMetrics{
		connectionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ssh_connections_total",
				Help: "Total number of SSH connections.",
			},
			[]string{"result"},
		),
		connectionsCurrent: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "ssh_connections_current",
				Help: "Current number of active SSH connections.",
			},
		),
		authAttempts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ssh_auth_attempts_total",
				Help: "Total number of SSH authentication attempts.",
			},
			[]string{"result", "method"},
		),
		sessionDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "ssh_session_duration_seconds",
				Help:    "Duration of SSH sessions in seconds.",
				Buckets: []float64{1, 10, 60, 300, 600, 1800, 3600, 7200}, // 1s to 2h
			},
			[]string{"reason"},
		),
	}

	registry.MustRegister(
		metrics.connectionsTotal,
		metrics.connectionsCurrent,
		metrics.authAttempts,
		metrics.sessionDuration,
	)

	return metrics
}
