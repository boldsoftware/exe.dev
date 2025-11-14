package tracing

import (
	"bytes"
	"context"
	"encoding/hex"
	"log/slog"
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
	for i := 0; i < 100; i++ {
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
