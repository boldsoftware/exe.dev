package execore

import (
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"exe.dev/metricsbag"
)

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
		path := normalizePath(r.URL.Path)
		box := ""

		if m.isProxyHost != nil && m.isProxyHost(r.Host) {
			proxy = "true"
			path = ""
			if m.boxFromHost != nil {
				box = m.boxFromHost(r.Host)
			}
		}

		g := m.requestsInFlight.WithLabelValues(proxy, path, box)
		g.Inc()
		defer g.Dec()

		counter.ServeHTTP(w, r)
	})
}

// normalizePath strips trailing slashes from paths (except for root "/").
func normalizePath(path string) string {
	if path == "/" || path == "" {
		return "/"
	}
	return strings.TrimSuffix(path, "/")
}
