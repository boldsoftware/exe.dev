package logging

import (
	"context"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
)

// LogMetrics holds counters for log messages by severity level.
type LogMetrics struct {
	logsTotal *prometheus.CounterVec
}

// NewLogMetrics creates and registers log metrics with the given registry.
func NewLogMetrics(registry *prometheus.Registry) *LogMetrics {
	m := &LogMetrics{
		logsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "logs_total",
				Help: "Total number of log messages emitted by severity level.",
			},
			[]string{"level"},
		),
	}
	registry.MustRegister(m.logsTotal)
	return m
}

// MetricsHandler wraps an slog.Handler to count log messages by level.
type MetricsHandler struct {
	handler slog.Handler
	metrics *LogMetrics
}

// NewMetricsHandler creates a new MetricsHandler that wraps the given handler
// and counts log messages using the provided metrics.
func NewMetricsHandler(h slog.Handler, m *LogMetrics) *MetricsHandler {
	return &MetricsHandler{handler: h, metrics: m}
}

// Enabled reports whether the handler handles records at the given level.
func (h *MetricsHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

// Handle counts the log message by level, then calls the underlying handler.
func (h *MetricsHandler) Handle(ctx context.Context, r slog.Record) error {
	h.metrics.logsTotal.WithLabelValues(r.Level.String()).Inc()
	return h.handler.Handle(ctx, r)
}

// WithAttrs returns a new MetricsHandler whose attributes consist of both
// the receiver's attributes and the arguments.
func (h *MetricsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &MetricsHandler{handler: h.handler.WithAttrs(attrs), metrics: h.metrics}
}

// WithGroup returns a new MetricsHandler with the given group appended to
// the receiver's existing groups.
func (h *MetricsHandler) WithGroup(name string) slog.Handler {
	return &MetricsHandler{handler: h.handler.WithGroup(name), metrics: h.metrics}
}
