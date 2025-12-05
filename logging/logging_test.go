package logging

import (
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
	SetupLogger("")

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
	SetupLogger("dev")

	// Should be able to log without issues
	slog.Info("test without OTEL")
}
