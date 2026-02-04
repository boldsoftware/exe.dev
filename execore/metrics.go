package execore

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"exe.dev/exedb"
	"exe.dev/metricsbag"
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

// Label names for HTTP metrics
const (
	LabelProxy = "proxy"
	LabelPath  = "path"
	LabelBox   = "box"
)

// HTTPMetrics holds HTTP request metrics.
type HTTPMetrics struct {
	requestsTotal    *prometheus.CounterVec
	requestsInFlight *prometheus.GaugeVec
	proxyBytesTotal  *prometheus.CounterVec

	// isProxyHost returns true if the host should be treated as a proxy request.
	// This is set by the Server when creating HTTPMetrics.
	isProxyHost func(host string) bool
	// boxFromHost extracts the box name from a proxy host.
	boxFromHost func(host string) string
}

// NewHTTPMetrics creates and registers HTTP metrics.
// Labels: code, proxy ("true"/"false"), path (when proxy=false), box (when proxy=true)
//
// Prometheus will eventually be unhappy about the sheer number of metrics.
// For now, we deem per-box metrics acceptable, but we will likely grow out
// of them, or, grow into a new system. In the meanwhile, it's worthwhile.
// The path metrics are generally fine, unless we start having paths with
// id's in them.
func NewHTTPMetrics(registry *prometheus.Registry) *HTTPMetrics {
	counterLabels := []string{"code", LabelProxy, LabelPath, LabelBox}
	inFlightLabels := []string{LabelProxy, LabelPath, LabelBox}
	metrics := &HTTPMetrics{
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests.",
		}, counterLabels),
		requestsInFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Current number of HTTP requests being served.",
		}, inFlightLabels),
		proxyBytesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "proxy_bytes_total",
			Help: "Total number of bytes proxied.",
		}, []string{"direction"}),
	}
	registry.MustRegister(metrics.requestsTotal, metrics.requestsInFlight, metrics.proxyBytesTotal)
	return metrics
}

// SetHostFuncs sets the functions used to determine proxy status and box name from hostname.
func (m *HTTPMetrics) SetHostFuncs(isProxyHost func(string) bool, boxFromHost func(string) string) {
	m.isProxyHost = isProxyHost
	m.boxFromHost = boxFromHost
}

// AddProxyBytes increments the proxy bytes counter.
// direction should be "in" for bytes received from the backend or "out" for bytes sent to the backend.
func (m *HTTPMetrics) AddProxyBytes(direction string, n int) {
	m.proxyBytesTotal.WithLabelValues(direction).Add(float64(n))
}

// Wrap wraps a handler with HTTP metrics instrumentation.
func (m *HTTPMetrics) Wrap(next http.Handler) http.Handler {
	counter := promhttp.InstrumentHandlerCounter(m.requestsTotal, next,
		promhttp.WithLabelFromCtx(LabelProxy, metricsbag.LabelFromCtx(LabelProxy)),
		promhttp.WithLabelFromCtx(LabelPath, metricsbag.LabelFromCtx(LabelPath)),
		promhttp.WithLabelFromCtx(LabelBox, metricsbag.LabelFromCtx(LabelBox)))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Determine labels from request for in-flight tracking.
		// These are determined at request start, before the handler runs.
		proxy := "false"
		path := sanitizePath(r.URL.Path)
		box := ""

		if m.isProxyHost != nil && m.isProxyHost(r.Host) {
			proxy = "true"
			path = ""
			if m.boxFromHost != nil {
				box = m.boxFromHost(r.Host)
			}
		}

		// TODO: in-flight gauge may still have cardinality explosion from arbitrary
		// paths since we don't know the status code yet. These are transient (cleared
		// when request ends) but could still be problematic under attack.
		g := m.requestsInFlight.WithLabelValues(proxy, path, box)
		g.Inc()
		defer g.Dec()

		// Wrap response to clear path label on 404 (avoids cardinality explosion)
		sw := &statusClearingWriter{
			ResponseWriter: w,
			ctx:            r.Context(),
		}
		counter.ServeHTTP(sw, r)
	})
}

// statusClearingWriter clears the path label when a 404 is written.
type statusClearingWriter struct {
	http.ResponseWriter
	ctx     context.Context
	cleared bool
}

func (w *statusClearingWriter) WriteHeader(code int) {
	if code == http.StatusNotFound && !w.cleared {
		metricsbag.SetLabel(w.ctx, LabelPath, "")
		w.cleared = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusClearingWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// normalizePath strips trailing slashes from paths (except for root "/").
func normalizePath(path string) string {
	if path == "/" || path == "" {
		return "/"
	}
	return strings.TrimSuffix(path, "/")
}

// sanitizePath normalizes a path and ensures it's valid UTF-8.
// Returns empty string for invalid UTF-8.
func sanitizePath(path string) string {
	if !utf8.ValidString(path) {
		return ""
	}
	return normalizePath(path)
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
