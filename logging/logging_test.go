package logging

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"exe.dev/stage"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	collectorlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

func TestSetupLogger_WithOTEL(t *testing.T) {
	// Create a mock OTLP HTTP server that captures log requests
	var mu sync.Mutex
	var receivedLogs []*collectorlogs.ExportLogsServiceRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Accept only POST to /v1/logs
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/v1/logs") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusInternalServerError)
			return
		}

		// Parse the protobuf request
		req := &collectorlogs.ExportLogsServiceRequest{}
		if err := proto.Unmarshal(body, req); err != nil {
			// Try JSON fallback
			if jsonErr := json.Unmarshal(body, req); jsonErr != nil {
				http.Error(w, "failed to parse request", http.StatusBadRequest)
				return
			}
		}

		mu.Lock()
		receivedLogs = append(receivedLogs, req)
		mu.Unlock()

		// Return success response
		resp := &collectorlogs.ExportLogsServiceResponse{}
		respBytes, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		w.Write(respBytes)
	}))
	defer server.Close()

	// Set environment variables for OTEL
	originalEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	originalServiceName := os.Getenv("OTEL_SERVICE_NAME")
	originalLogFormat := os.Getenv("LOG_FORMAT")
	defer func() {
		os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", originalEndpoint)
		os.Setenv("OTEL_SERVICE_NAME", originalServiceName)
		os.Setenv("LOG_FORMAT", originalLogFormat)
	}()

	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
	os.Setenv("OTEL_SERVICE_NAME", "test-service")
	os.Setenv("LOG_FORMAT", "json")

	// Setup the logger
	SetupLogger(stage.Prod(), nil)

	// Log some messages
	slog.Info("test message 1", "key1", "value1")
	slog.Warn("test message 2", "key2", "value2")
	slog.Error("test message 3", "key3", "value3")

	// Wait for logs to be batched and sent (batch processor has a delay)
	// Poll for logs with a timeout
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(receivedLogs)
		mu.Unlock()
		if count > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Verify we received logs
	mu.Lock()
	defer mu.Unlock()

	require.NotEmpty(t, receivedLogs, "expected to receive OTLP log requests")

	// Count total log records received
	var totalRecords int
	for _, req := range receivedLogs {
		for _, rl := range req.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				totalRecords += len(sl.LogRecords)
			}
		}
	}

	require.GreaterOrEqual(t, totalRecords, 1, "expected at least 1 log record to be received")
}

func TestSetupLogger_WithoutOTEL(t *testing.T) {
	// Ensure OTEL endpoint is not set
	originalEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	originalLogFormat := os.Getenv("LOG_FORMAT")
	defer func() {
		os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", originalEndpoint)
		os.Setenv("LOG_FORMAT", originalLogFormat)
	}()

	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	os.Setenv("LOG_FORMAT", "json")

	// This should not panic or error
	SetupLogger(stage.Test(), nil)

	// Should be able to log without issues
	slog.Info("test without OTEL")
}

func TestLogMetrics(t *testing.T) {
	// Create a prometheus registry for testing
	registry := prometheus.NewRegistry()
	metrics := NewLogMetrics(registry)

	// Create a base handler that discards output
	baseHandler := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	// Wrap with metrics handler
	handler := NewMetricsHandler(baseHandler, metrics)
	logger := slog.New(handler)

	// Log messages at different levels
	ctx := context.Background()
	logger.DebugContext(ctx, "debug message")
	logger.InfoContext(ctx, "info message 1")
	logger.InfoContext(ctx, "info message 2")
	logger.WarnContext(ctx, "warn message")
	logger.ErrorContext(ctx, "error message 1")
	logger.ErrorContext(ctx, "error message 2")
	logger.ErrorContext(ctx, "error message 3")

	// Gather metrics
	mfs, err := registry.Gather()
	require.NoError(t, err)

	// Check counts by level
	counts := make(map[string]float64)
	found := false
	for _, mf := range mfs {
		if mf.GetName() == "logs_total" {
			found = true
			for _, m := range mf.GetMetric() {
				for _, label := range m.GetLabel() {
					if label.GetName() == "level" {
						counts[label.GetValue()] = m.GetCounter().GetValue()
					}
				}
			}
			break
		}
	}
	require.True(t, found, "logs_total metric not found")

	require.Equal(t, float64(1), counts["DEBUG"], "expected 1 debug log")
	require.Equal(t, float64(2), counts["INFO"], "expected 2 info logs")
	require.Equal(t, float64(1), counts["WARN"], "expected 1 warn log")
	require.Equal(t, float64(3), counts["ERROR"], "expected 3 error logs")
}

func TestLogMetricsWithAttrsAndGroup(t *testing.T) {
	// Create a prometheus registry for testing
	registry := prometheus.NewRegistry()
	metrics := NewLogMetrics(registry)

	// Create a base handler that discards output
	baseHandler := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	// Wrap with metrics handler
	handler := NewMetricsHandler(baseHandler, metrics)

	// Test WithAttrs returns a MetricsHandler
	handlerWithAttrs := handler.WithAttrs([]slog.Attr{slog.String("key", "value")})
	require.IsType(t, &MetricsHandler{}, handlerWithAttrs)

	// Test WithGroup returns a MetricsHandler
	handlerWithGroup := handler.WithGroup("group")
	require.IsType(t, &MetricsHandler{}, handlerWithGroup)

	// Log with the derived handlers
	logger1 := slog.New(handlerWithAttrs)
	logger2 := slog.New(handlerWithGroup)

	logger1.Info("message with attrs")
	logger2.Info("message with group")

	// Verify counts still work
	mfs, err := registry.Gather()
	require.NoError(t, err)

	// Find INFO count
	found := false
	for _, mf := range mfs {
		if mf.GetName() == "logs_total" {
			for _, m := range mf.GetMetric() {
				for _, label := range m.GetLabel() {
					if label.GetName() == "level" && label.GetValue() == "INFO" {
						found = true
						require.Equal(t, float64(2), m.GetCounter().GetValue(), "expected 2 info logs")
					}
				}
			}
			break
		}
	}
	require.True(t, found, "logs_total INFO metric not found")
}

func TestSetupLoggerWithMetrics(t *testing.T) {
	// Create a prometheus registry for testing
	registry := prometheus.NewRegistry()

	// Save and restore environment
	originalLogFormat := os.Getenv("LOG_FORMAT")
	originalOTEL := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	defer func() {
		os.Setenv("LOG_FORMAT", originalLogFormat)
		os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", originalOTEL)
	}()

	os.Setenv("LOG_FORMAT", "json")
	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	// Setup logger with registry
	SetupLogger(stage.Test(), registry)

	// Log some messages
	slog.Info("test info")
	slog.Error("test error")

	// Verify metrics are collected
	mfs, err := registry.Gather()
	require.NoError(t, err)

	found := false
	for _, mf := range mfs {
		if mf.GetName() == "logs_total" {
			found = true
			require.GreaterOrEqual(t, len(mf.GetMetric()), 1, "should have at least one level metric")
			break
		}
	}
	require.True(t, found, "logs_total metric should be registered")
}
