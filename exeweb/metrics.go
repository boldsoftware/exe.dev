package exeweb

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"exe.dev/metricsbag"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

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
	ProxyBytesTotal  *prometheus.CounterVec // TODO: unexport field

	// isProxyHost reports whether host should be
	// treated as a proxy request.
	isProxyHost func(host string) bool
	// boxFromHost extracts the box name from a proxy host.
	boxFromHost func(host string) string
}

// NewHTTPMetrics creates and registers HTTP metrics.
// Labels: code, proxy ("true"/"false"), path (when proxy=false),
// box (when proxy=true).
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
		ProxyBytesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "proxy_bytes_total",
			Help: "Total number of bytes proxied.",
		}, []string{"direction"}),
	}
	registry.MustRegister(metrics.requestsTotal, metrics.requestsInFlight, metrics.ProxyBytesTotal)
	return metrics
}

// SetHostFuncs sets the functions used to determine
// proxy status and box name from hostname.
func (m *HTTPMetrics) SetHostFuncs(isProxyHost func(string) bool, boxFromHost func(string) string) {
	m.isProxyHost = isProxyHost
	m.boxFromHost = boxFromHost
}

// AddProxyBytes increments the proxy bytes counter.
// direction should be "in" for bytes received from the backend
// or "out" for bytes sent to the backend.
func (m *HTTPMetrics) AddProxyBytes(direction string, n int) {
	m.ProxyBytesTotal.WithLabelValues(direction).Add(float64(n))
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
		path := SanitizePath(r.URL.Path)
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

// SanitizePath normalizes a path and ensures it's valid UTF-8.
// Returns empty string for invalid UTF-8.
func SanitizePath(path string) string {
	if !utf8.ValidString(path) {
		return ""
	}
	return normalizePath(path)
}

// normalizePath strips trailing slashes from paths (except for root "/").
func normalizePath(path string) string {
	if path == "/" || path == "" {
		return "/"
	}
	return strings.TrimSuffix(path, "/")
}
