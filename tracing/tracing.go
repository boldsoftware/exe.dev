package tracing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
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

// traceIDContextKey is a typed key for storing trace_id in context.
// We use "trace_id" string for backward compatibility with existing code.
const traceIDContextKey = "trace_id"

// TraceIDHeader is the HTTP header used to propagate trace IDs between services.
const TraceIDHeader = "X-Trace-ID"

// ContextWithTraceID returns a new context with the given trace_id.
func ContextWithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDContextKey, traceID)
}

// TraceIDFromContext extracts the trace_id from context, if present.
func TraceIDFromContext(ctx context.Context) string {
	if traceID := ctx.Value(traceIDContextKey); traceID != nil {
		if tid, ok := traceID.(string); ok {
			return tid
		}
	}
	return ""
}

// HTTPMiddleware returns HTTP middleware that adds a trace_id to the request context.
// If the request already has a trace_id (from an incoming header), it uses that;
// otherwise it generates a new one.
func HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Check if trace_id already exists in context (e.g., from another middleware)
		traceID := TraceIDFromContext(ctx)
		if traceID == "" {
			// Check for trace_id in incoming header
			traceID = r.Header.Get(TraceIDHeader)
		}
		if traceID == "" {
			traceID = GenerateTraceID()
		}
		ctx = ContextWithTraceID(ctx, traceID)
		r = r.WithContext(ctx)

		next.ServeHTTP(w, r)
	})
}

// SetTraceIDHeader sets the trace_id header on an outgoing HTTP request.
// If the context has a trace_id, it will be used; otherwise nothing is set.
func SetTraceIDHeader(ctx context.Context, header http.Header) {
	if traceID := TraceIDFromContext(ctx); traceID != "" {
		header.Set(TraceIDHeader, traceID)
	}
}
