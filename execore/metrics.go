package execore

import "github.com/prometheus/client_golang/prometheus"

// SSHMetrics holds SSH server metrics
type SSHMetrics struct {
	connectionsTotal     *prometheus.CounterVec
	connectionsCurrent   prometheus.Gauge
	authAttempts         *prometheus.CounterVec
	sessionDuration      *prometheus.HistogramVec
	boxCreationDur       prometheus.Histogram
	letsencryptRequests  prometheus.Counter
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
		boxCreationDur: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "box_creation_time_seconds",
				Help:    "Time to create a box from the user's perspective.",
				Buckets: []float64{0.25, 0.5, 0.75, 1.0, 1.25, 1.5, 1.75, 2, 2.25, 2.5, 2.75, 3, 3.5, 4, 5, 8},
			},
		),
		letsencryptRequests: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "letsencrypt_cert_requests_total",
				Help: "Total number of certificate requests made to Let's Encrypt.",
			},
		),
	}

	registry.MustRegister(
		metrics.connectionsTotal,
		metrics.connectionsCurrent,
		metrics.authAttempts,
		metrics.sessionDuration,
		metrics.boxCreationDur,
		metrics.letsencryptRequests,
	)

	return metrics
}
