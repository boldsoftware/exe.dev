package execore

import (
	"context"
	"fmt"
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
	trialsDesc = prometheus.NewDesc(
		"stripeless_trials_total",
		"Total number of stripeless trial accounts.",
		[]string{"kind", "status"}, nil,
	)
)

func (c *entityCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- usersDesc
	ch <- vmsDesc
	ch <- usersWithVMsDesc
	ch <- billingAccountsDesc
	ch <- trialsDesc
}

func (c *entityCollector) Collect(ch chan<- prometheus.Metric) {
	var users userTypeCounts
	var billing billingStatusCounts
	var trials []trialCount
	var vms, usersWithVMs int64

	err := c.db.Rx(context.Background(), func(ctx context.Context, rx *sqlite.Rx) error {
		q := exedb.New(rx.Conn())
		var err error

		if users, err = countUsersByType(q, ctx); err != nil {
			return err
		}
		if vms, err = q.CountBoxes(ctx); err != nil {
			return err
		}
		if usersWithVMs, err = q.CountUsersWithBoxes(ctx); err != nil {
			return err
		}
		if billing, err = countBillingStatuses(q, ctx); err != nil {
			return err
		}
		if trials, err = countTrials(rx); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		c.logger.Error("failed to collect entity metrics", "error", err)
		return
	}

	ch <- prometheus.MustNewConstMetric(usersDesc, prometheus.GaugeValue, float64(users.Login), "login")
	ch <- prometheus.MustNewConstMetric(usersDesc, prometheus.GaugeValue, float64(users.Dev), "dev")
	ch <- prometheus.MustNewConstMetric(vmsDesc, prometheus.GaugeValue, float64(vms))
	ch <- prometheus.MustNewConstMetric(usersWithVMsDesc, prometheus.GaugeValue, float64(usersWithVMs))
	ch <- prometheus.MustNewConstMetric(billingAccountsDesc, prometheus.GaugeValue, float64(billing.Active), "active")
	ch <- prometheus.MustNewConstMetric(billingAccountsDesc, prometheus.GaugeValue, float64(billing.Canceled), "canceled")
	ch <- prometheus.MustNewConstMetric(billingAccountsDesc, prometheus.GaugeValue, float64(billing.Pending), "pending")
	for _, t := range trials {
		ch <- prometheus.MustNewConstMetric(trialsDesc, prometheus.GaugeValue, float64(t.count), t.kind, t.status)
	}
}

type trialCount struct {
	kind, status string
	count        int64
}

// countTrials returns stripeless trial counts by kind and status.
//
// kind labels: "signup" (7-day self-serve, changed_by = system:stripeless_trial),
// "invite" (30-day invite codes, changed_by LIKE 'invite:%'). Historical
// system:backfill rows and one-off debug grants are excluded — they are not
// product-driven trial starts and would otherwise dominate the totals.
//
// status is "converted" if the account currently holds any non-trial paid
// plan, else "active" while the trial window is open, else "expired".
func countTrials(rx *sqlite.Rx) ([]trialCount, error) {
	const q = `
SELECT kind, status, COUNT(*) FROM (
    SELECT
        CASE
            WHEN ap.changed_by = 'system:stripeless_trial' THEN 'signup'
            WHEN ap.changed_by LIKE 'invite:%' THEN 'invite'
        END AS kind,
        CASE
            WHEN EXISTS (
                SELECT 1 FROM account_plans ap2
                WHERE ap2.account_id = a.id
                  AND ap2.ended_at IS NULL
                  AND ap2.plan_id NOT LIKE 'trial:%'
                  AND ap2.plan_id NOT LIKE 'basic:%'
                  AND ap2.plan_id != 'restricted'
            ) THEN 'converted'
            WHEN ap.ended_at IS NULL AND ap.trial_expires_at > datetime('now') THEN 'active'
            ELSE 'expired'
        END AS status
    FROM accounts a
    JOIN account_plans ap ON ap.account_id = a.id
    WHERE ap.plan_id LIKE 'trial:%'
      AND (ap.changed_by = 'system:stripeless_trial' OR ap.changed_by LIKE 'invite:%')
) GROUP BY kind, status`
	rows, err := rx.Query(q)
	if err != nil {
		return nil, fmt.Errorf("count trials: %w", err)
	}
	defer rows.Close()
	var out []trialCount
	for rows.Next() {
		var t trialCount
		if err := rows.Scan(&t.kind, &t.status, &t.count); err != nil {
			return nil, fmt.Errorf("count trials scan: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count trials rows: %w", err)
	}
	return out, nil
}

type billingStatusCounts struct {
	Active, Canceled, Pending int64
}

// countBillingStatuses returns account counts grouped by billing status.
func countBillingStatuses(q *exedb.Queries, ctx context.Context) (billingStatusCounts, error) {
	rows, err := q.CountAccountsBillingStatuses(ctx)
	if err != nil {
		return billingStatusCounts{}, err
	}
	var c billingStatusCounts
	for _, r := range rows {
		switch r.BillingStatus {
		case "active":
			c.Active = r.Count
		case "canceled":
			c.Canceled = r.Count
		case "pending":
			c.Pending = r.Count
		default:
			slog.WarnContext(ctx, "unknown billing status", "status", r.BillingStatus)
		}
	}
	return c, nil
}

type userTypeCounts struct {
	Login, Dev int64
}

// countUsersByType returns user counts grouped by login vs dev.
func countUsersByType(q *exedb.Queries, ctx context.Context) (userTypeCounts, error) {
	rows, err := q.CountUsersByType(ctx)
	if err != nil {
		return userTypeCounts{}, err
	}
	var c userTypeCounts
	for _, r := range rows {
		if r.CreatedForLoginWithExe {
			c.Login = r.Count
		} else {
			c.Dev = r.Count
		}
	}
	return c, nil
}
