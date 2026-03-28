package execore

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"exe.dev/tracing"
)

func TestLoggerMiddlewareStatusCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		handlerFunc    http.HandlerFunc
		expectedStatus int
	}{
		{
			name: "explicit 200",
			handlerFunc: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("ok"))
			},
			expectedStatus: 200,
		},
		{
			name: "implicit 200 from Write",
			handlerFunc: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("ok"))
			},
			expectedStatus: 200,
		},
		{
			name: "404 not found",
			handlerFunc: func(w http.ResponseWriter, r *http.Request) {
				http.NotFound(w, r)
			},
			expectedStatus: 404,
		},
		{
			name: "500 internal error",
			handlerFunc: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "internal error", http.StatusInternalServerError)
			},
			expectedStatus: 500,
		},
		{
			name: "redirect",
			handlerFunc: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
			},
			expectedStatus: 307,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture log output
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))

			// Create handler with middleware
			handler := LoggerMiddleware(logger)(tt.handlerFunc)

			// Make request
			req := httptest.NewRequest("GET", "/test", nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			// Parse log output
			var logEntry map[string]any
			if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
				t.Fatalf("failed to parse log output: %v\nLog output: %s", err, buf.String())
			}

			// Check status in log (slog-http nests it under response.status)
			response, ok := logEntry["response"].(map[string]any)
			if !ok {
				t.Fatalf("response not found in log or wrong type. Log: %v", logEntry)
			}
			status, ok := response["status"].(float64)
			if !ok {
				t.Fatalf("status not found in response or wrong type. Log: %v", logEntry)
			}

			if int(status) != tt.expectedStatus {
				t.Errorf("expected status %d in log, got %d", tt.expectedStatus, int(status))
			}

			// Verify actual response status matches
			if w.Code != tt.expectedStatus {
				t.Errorf("expected response status %d, got %d", tt.expectedStatus, w.Code)
			}
		})
	}
}

func TestLoggerMiddleware_SkipsMetrics200(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	handler := LoggerMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(httptest.NewRecorder(), metricsReq)

	if buf.Len() != 0 {
		t.Fatalf("expected GET /metrics 200 to be skipped, got log: %s", buf.String())
	}

	otherReq := httptest.NewRequest(http.MethodGet, "/other", nil)
	handler.ServeHTTP(httptest.NewRecorder(), otherReq)

	if buf.Len() == 0 {
		t.Fatal("expected non-metrics request to be logged")
	}
	if !bytes.Contains(buf.Bytes(), []byte("/other")) {
		t.Fatalf("expected log to contain non-metrics path, got: %s", buf.String())
	}
}

func TestLoggerMiddleware_AddsTraceID(t *testing.T) {
	t.Parallel()
	var capturedTraceID string

	// Capture log output using our tracing handler
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, nil)
	tracingHandler := tracing.NewHandler(jsonHandler)
	logger := slog.New(tracingHandler)

	// Create handler with middleware that logs with context
	innerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTraceID = tracing.TraceIDFromContext(r.Context())
		logger.InfoContext(r.Context(), "inner handler")
		w.WriteHeader(http.StatusOK)
	})
	handler := LoggerMiddleware(logger)(innerHandler)

	// Make request
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Verify trace_id was set on context
	if capturedTraceID == "" {
		t.Error("LoggerMiddleware did not set trace_id on context")
	}

	// Verify trace_id appears in log output
	if !bytes.Contains(buf.Bytes(), []byte(`"trace_id"`)) {
		t.Errorf("Log output missing trace_id. Got: %s", buf.String())
	}
}

func TestLoggerMiddleware_TraceIDIsUnique(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool)

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	innerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := tracing.TraceIDFromContext(r.Context())
		if seen[traceID] {
			t.Errorf("LoggerMiddleware generated duplicate trace_id: %s", traceID)
		}
		seen[traceID] = true
		w.WriteHeader(http.StatusOK)
	})
	handler := LoggerMiddleware(logger)(innerHandler)

	for range 100 {
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}
}
