package execore

import (
	"context"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"exe.dev/exedb"
	"exe.dev/sqlite"
)

// SignupMetrics holds metrics for user signup operations.
type SignupMetrics struct {
	blockedTotal *prometheus.CounterVec
}

// NewSignupMetrics creates and registers signup metrics.
func NewSignupMetrics(registry *prometheus.Registry) *SignupMetrics {
	m := &SignupMetrics{
		blockedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "signups_blocked_total",
				Help: "Total number of blocked signup attempts.",
			},
			[]string{"reason", "source"},
		),
	}
	registry.MustRegister(m.blockedTotal)
	return m
}

// IncBlocked increments the blocked signup counter for the given reason and source.
// Source should be "web", "mobile", or "ssh".
func (m *SignupMetrics) IncBlocked(reason, source string) {
	m.blockedTotal.WithLabelValues(reason, source).Inc()
}

// SSHMetrics holds SSH server metrics
type SSHMetrics struct {
	connectionsTotal    *prometheus.CounterVec
	connectionsCurrent  prometheus.Gauge
	authAttempts        *prometheus.CounterVec
	sessionDuration     *prometheus.HistogramVec
	boxCreationDur      prometheus.Histogram
	letsencryptRequests prometheus.Counter
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

// RegisterEntityMetrics registers gauge metrics for user and VM counts.
// The gauges query the database on each Prometheus scrape.
func RegisterEntityMetrics(registry *prometheus.Registry, db *sqlite.DB, logger *slog.Logger) {
	registry.MustRegister(&entityCollector{db: db, logger: logger})
}

// entityCollector implements prometheus.Collector for entity count metrics.
type entityCollector struct {
	db     *sqlite.DB
	logger *slog.Logger
}

var (
	usersDesc = prometheus.NewDesc(
		"users_total",
		"Total number of users.",
		[]string{"type"}, nil,
	)
	vmsDesc = prometheus.NewDesc(
		"vms_total",
		"Total number of VMs.",
		nil, nil,
	)
	usersWithVMsDesc = prometheus.NewDesc(
		"users_with_vms_total",
		"Total number of users with at least one VM.",
		nil, nil,
	)
	billingAccountsDesc = prometheus.NewDesc(
		"billing_accounts_total",
		"Total number of billing accounts.",
		[]string{"status"}, nil,
	)
)

func (c *entityCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- usersDesc
	ch <- vmsDesc
	ch <- usersWithVMsDesc
	ch <- billingAccountsDesc
}

func (c *entityCollector) Collect(ch chan<- prometheus.Metric) {
	var loginUsers, devUsers, vms, usersWithVMs int64
	var accountsActive, accountsPending int64

	err := c.db.Rx(context.Background(), func(ctx context.Context, rx *sqlite.Rx) error {
		q := exedb.New(rx.Conn())
		var err error
		if loginUsers, err = q.CountLoginUsers(ctx); err != nil {
			return err
		}
		if devUsers, err = q.CountDevUsers(ctx); err != nil {
			return err
		}
		if vms, err = q.CountBoxes(ctx); err != nil {
			return err
		}
		if usersWithVMs, err = q.CountUsersWithBoxes(ctx); err != nil {
			return err
		}
		if accountsActive, err = q.CountAccountsByBillingStatus(ctx, "active"); err != nil {
			return err
		}
		if accountsPending, err = q.CountAccountsByBillingStatus(ctx, "pending"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		c.logger.Error("failed to collect entity metrics", "error", err)
		return
	}

	ch <- prometheus.MustNewConstMetric(usersDesc, prometheus.GaugeValue, float64(loginUsers), "login")
	ch <- prometheus.MustNewConstMetric(usersDesc, prometheus.GaugeValue, float64(devUsers), "dev")
	ch <- prometheus.MustNewConstMetric(vmsDesc, prometheus.GaugeValue, float64(vms))
	ch <- prometheus.MustNewConstMetric(usersWithVMsDesc, prometheus.GaugeValue, float64(usersWithVMs))
	ch <- prometheus.MustNewConstMetric(billingAccountsDesc, prometheus.GaugeValue, float64(accountsActive), "active")
	ch <- prometheus.MustNewConstMetric(billingAccountsDesc, prometheus.GaugeValue, float64(accountsPending), "pending")
}
