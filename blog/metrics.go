package blog

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds Prometheus counters for the blog.
type Metrics struct {
	pageHits *prometheus.CounterVec
}

// NewMetrics registers blog metrics on the provided registry.
func NewMetrics(registry *prometheus.Registry) *Metrics {
	if registry == nil {
		panic("blog metrics require a Prometheus registry")
	}
	m := &Metrics{
		pageHits: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "blog_page_hits_total",
				Help: "Total number of blog page hits by path.",
			},
			[]string{"path"},
		),
	}
	registry.MustRegister(m.pageHits)
	return m
}

// RecordPageHit increments the hit counter for the given request path.
// Paths are normalized to lowercase, trailing slashes trimmed, and
// query strings removed.
func (m *Metrics) RecordPageHit(path string) {
	if m == nil || m.pageHits == nil {
		return
	}
	normalized := normalizeMetricsPath(path)
	m.pageHits.WithLabelValues(normalized).Inc()
}

func normalizeMetricsPath(path string) string {
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.ToLower(path)
	if path != "/" {
		path = strings.TrimRight(path, "/")
		if path == "" {
			return "/"
		}
	}
	return path
}
