package tracing

import (
	"bytes"
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGenerateTraceID(t *testing.T) {
	// Generate a trace ID
	traceID := GenerateTraceID()

	// Check that it's a valid hex string
	if _, err := hex.DecodeString(traceID); err != nil {
		t.Fatalf("GenerateTraceID() returned invalid hex string: %v", err)
	}

	// Check that it's 32 characters (16 bytes in hex)
	if len(traceID) != 32 {
		t.Errorf("GenerateTraceID() returned wrong length: got %d, want 32", len(traceID))
	}

	// Generate multiple trace IDs and ensure they're unique
	seen := make(map[string]bool)
	for range 100 {
		id := GenerateTraceID()
		if seen[id] {
			t.Errorf("GenerateTraceID() generated duplicate ID: %s", id)
		}
		seen[id] = true
	}
}

func TestGenerateTraceIDFormat(t *testing.T) {
	traceID := GenerateTraceID()

	// Decode to ensure it's valid hex
	decoded, err := hex.DecodeString(traceID)
	if err != nil {
		t.Fatalf("GenerateTraceID() returned invalid hex: %v", err)
	}

	// Verify decoded length is 16 bytes
	if len(decoded) != 16 {
		t.Errorf("Decoded trace ID has wrong length: got %d bytes, want 16", len(decoded))
	}
}

func TestHandlerAddsTraceID(t *testing.T) {
	// Create a buffer to capture log output
	var buf bytes.Buffer

	// Create a JSON handler that writes to the buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	// Wrap it with our tracing handler
	handler := NewHandler(jsonHandler)
	logger := slog.New(handler)

	// Create a context with a trace_id
	traceID := "abc123def456"
	ctx := context.WithValue(context.Background(), "trace_id", traceID)

	// Log a message with context
	logger.DebugContext(ctx, "test message", "key", "value")

	// Check that the output contains the trace_id
	output := buf.String()
	if !strings.Contains(output, `"trace_id":"abc123def456"`) {
		t.Errorf("Log output missing trace_id. Got: %s", output)
	}
	if !strings.Contains(output, `"msg":"test message"`) {
		t.Errorf("Log output missing message. Got: %s", output)
	}
	if !strings.Contains(output, `"key":"value"`) {
		t.Errorf("Log output missing custom attribute. Got: %s", output)
	}
}

func TestHandlerWithoutTraceID(t *testing.T) {
	// Create a buffer to capture log output
	var buf bytes.Buffer

	// Create a JSON handler that writes to the buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	// Wrap it with our tracing handler
	handler := NewHandler(jsonHandler)
	logger := slog.New(handler)

	// Log a message without trace_id in context
	ctx := context.Background()
	logger.DebugContext(ctx, "test message")

	// Check that the output does not contain trace_id
	output := buf.String()
	if strings.Contains(output, "trace_id") {
		t.Errorf("Log output should not contain trace_id. Got: %s", output)
	}
	if !strings.Contains(output, `"msg":"test message"`) {
		t.Errorf("Log output missing message. Got: %s", output)
	}
}

func TestHandlerWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	handler := NewHandler(jsonHandler)
	logger := slog.New(handler).With("component", "test")

	traceID := "test-trace-id"
	ctx := context.WithValue(context.Background(), "trace_id", traceID)

	logger.DebugContext(ctx, "test message")

	output := buf.String()
	if !strings.Contains(output, `"trace_id":"test-trace-id"`) {
		t.Errorf("Log output missing trace_id. Got: %s", output)
	}
	if !strings.Contains(output, `"component":"test"`) {
		t.Errorf("Log output missing component attribute. Got: %s", output)
	}
}

func TestHandlerWithGroup(t *testing.T) {
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	handler := NewHandler(jsonHandler)
	logger := slog.New(handler).WithGroup("request")

	traceID := "test-trace-id"
	ctx := context.WithValue(context.Background(), "trace_id", traceID)

	logger.DebugContext(ctx, "test message", "method", "GET")

	output := buf.String()
	if !strings.Contains(output, `"trace_id":"test-trace-id"`) {
		t.Errorf("Log output missing trace_id. Got: %s", output)
	}
	if !strings.Contains(output, `"request":`) {
		t.Errorf("Log output missing request group. Got: %s", output)
	}
}

func TestContextWithTraceID(t *testing.T) {
	ctx := context.Background()
	traceID := "test-trace-123"

	ctx = ContextWithTraceID(ctx, traceID)

	got := TraceIDFromContext(ctx)
	if got != traceID {
		t.Errorf("TraceIDFromContext() = %q, want %q", got, traceID)
	}
}

func TestTraceIDFromContext_Empty(t *testing.T) {
	ctx := context.Background()

	got := TraceIDFromContext(ctx)
	if got != "" {
		t.Errorf("TraceIDFromContext() = %q, want empty string", got)
	}
}

func TestHTTPMiddleware_GeneratesTraceID(t *testing.T) {
	var capturedTraceID string

	handler := HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTraceID = TraceIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if capturedTraceID == "" {
		t.Error("HTTPMiddleware did not generate a trace_id")
	}

	// Verify the trace_id is a valid hex string of correct length
	if len(capturedTraceID) != 32 {
		t.Errorf("Generated trace_id has wrong length: got %d, want 32", len(capturedTraceID))
	}
	if _, err := hex.DecodeString(capturedTraceID); err != nil {
		t.Errorf("Generated trace_id is not valid hex: %v", err)
	}
}

func TestHTTPMiddleware_GeneratesUniqueTraceIDs(t *testing.T) {
	seen := make(map[string]bool)

	handler := HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := TraceIDFromContext(r.Context())
		if seen[traceID] {
			t.Errorf("HTTPMiddleware generated duplicate trace_id: %s", traceID)
		}
		seen[traceID] = true
		w.WriteHeader(http.StatusOK)
	}))

	for range 100 {
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}
}

func TestHTTPMiddleware_PreservesExistingTraceID(t *testing.T) {
	existingTraceID := "existing-trace-id-12345678"
	var capturedTraceID string

	// Outer middleware sets trace_id
	outerMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := ContextWithTraceID(r.Context(), existingTraceID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	handler := outerMiddleware(HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTraceID = TraceIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if capturedTraceID != existingTraceID {
		t.Errorf("HTTPMiddleware did not preserve existing trace_id: got %q, want %q", capturedTraceID, existingTraceID)
	}
}

func TestHTTPMiddleware_TraceIDAvailableForLogging(t *testing.T) {
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	handler := NewHandler(jsonHandler)
	logger := slog.New(handler)

	httpHandler := HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.DebugContext(r.Context(), "request received")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	httpHandler.ServeHTTP(w, req)

	output := buf.String()
	if !strings.Contains(output, `"trace_id":`) {
		t.Errorf("Log output missing trace_id. Got: %s", output)
	}
	if !strings.Contains(output, `"msg":"request received"`) {
		t.Errorf("Log output missing message. Got: %s", output)
	}
}

func TestHTTPMiddleware_ReadsTraceIDFromHeader(t *testing.T) {
	expectedTraceID := "header-trace-id-12345678"
	var capturedTraceID string

	handler := HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTraceID = TraceIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set(TraceIDHeader, expectedTraceID)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if capturedTraceID != expectedTraceID {
		t.Errorf("HTTPMiddleware did not read trace_id from header: got %q, want %q", capturedTraceID, expectedTraceID)
	}
}

func TestSetTraceIDHeader(t *testing.T) {
	traceID := "test-trace-id-for-header"
	ctx := ContextWithTraceID(context.Background(), traceID)

	header := make(http.Header)
	SetTraceIDHeader(ctx, header)

	got := header.Get(TraceIDHeader)
	if got != traceID {
		t.Errorf("SetTraceIDHeader() set header to %q, want %q", got, traceID)
	}
}

func TestSetTraceIDHeader_NoTraceID(t *testing.T) {
	ctx := context.Background()

	header := make(http.Header)
	SetTraceIDHeader(ctx, header)

	got := header.Get(TraceIDHeader)
	if got != "" {
		t.Errorf("SetTraceIDHeader() should not set header when no trace_id in context, got %q", got)
	}
}
