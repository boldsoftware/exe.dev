package server

import (
	"context"
	"log/slog"
)

// BufferedLogHandler wraps another slog.Handler and captures logs to a buffer
type BufferedLogHandler struct {
	next   slog.Handler
	buffer *LogBuffer
}

// NewBufferedLogHandler creates a new handler that captures logs to the buffer
func NewBufferedLogHandler(next slog.Handler, buffer *LogBuffer) *BufferedLogHandler {
	return &BufferedLogHandler{
		next:   next,
		buffer: buffer,
	}
}

// Enabled reports whether the handler handles records at the given level
func (h *BufferedLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle handles the Record by both passing it to the next handler and adding to buffer
func (h *BufferedLogHandler) Handle(ctx context.Context, record slog.Record) error {
	// First, handle with the original handler
	err := h.next.Handle(ctx, record)

	// Extract fields from the record
	fields := make(map[string]interface{})
	record.Attrs(func(attr slog.Attr) bool {
		fields[attr.Key] = attr.Value.Any()
		return true
	})

	// Add to buffer
	entry := LogEntry{
		Timestamp: record.Time,
		Level:     record.Level.String(),
		Message:   record.Message,
		Fields:    fields,
	}
	h.buffer.Add(entry)

	return err
}

// WithAttrs returns a new Handler whose attributes consist of both the receiver's attributes and the arguments
func (h *BufferedLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &BufferedLogHandler{
		next:   h.next.WithAttrs(attrs),
		buffer: h.buffer,
	}
}

// WithGroup returns a new Handler with the given group appended to the receiver's existing groups
func (h *BufferedLogHandler) WithGroup(name string) slog.Handler {
	return &BufferedLogHandler{
		next:   h.next.WithGroup(name),
		buffer: h.buffer,
	}
}
