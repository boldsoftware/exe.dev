package tracing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
)

// GenerateTraceID generates a random 16-byte trace ID and returns it as a hex string.
// otel's standard for trace IDs is this format, so we follow it here.
func GenerateTraceID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Handler wraps an slog.Handler to automatically add trace_id from context.
type Handler struct {
	handler slog.Handler
}

// NewHandler creates a new Handler that wraps the given handler.
func NewHandler(h slog.Handler) *Handler {
	return &Handler{handler: h}
}

// Enabled reports whether the handler handles records at the given level.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

// Handle adds trace_id from context if present, then calls the underlying handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if traceID := ctx.Value("trace_id"); traceID != nil {
		if tid, ok := traceID.(string); ok {
			r.AddAttrs(slog.String("trace_id", tid))
		}
	}
	return h.handler.Handle(ctx, r)
}

// WithAttrs returns a new Handler whose attributes consist of both the receiver's
// attributes and the arguments.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{handler: h.handler.WithAttrs(attrs)}
}

// WithGroup returns a new Handler with the given group appended to the receiver's
// existing groups.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{handler: h.handler.WithGroup(name)}
}
