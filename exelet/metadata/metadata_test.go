package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"exe.dev/tracing"
)

// mockInstanceLookup is a simple mock for testing
type mockInstanceLookup struct{}

func (m *mockInstanceLookup) GetInstanceByIP(ctx context.Context, ip string) (id, name string, err error) {
	return "test-id", "test-box", nil
}

func TestMetadataService404(t *testing.T) {
	log := slog.Default()

	svc, err := NewService(log, &mockInstanceLookup{}, "http://localhost:8080", "127.0.0.1:18080", nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	// Create test server using the service's handler setup
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", svc.handleRoot)
	mux.HandleFunc("/gateway/llm/", svc.handleGatewayProxy)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{
			name:       "root returns 200",
			path:       "/",
			wantStatus: http.StatusOK,
		},
		{
			name:       "unknown path returns 404",
			path:       "/does-not-exist",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "another unknown path returns 404",
			path:       "/foo/bar/baz",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "gateway llm ready returns 200 or proxy error",
			path:       "/gateway/llm/ready",
			wantStatus: http.StatusBadGateway, // proxy will fail since there's no backend
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := http.Get(ts.URL + tt.path)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("got status %d, want %d, body: %s", resp.StatusCode, tt.wantStatus, string(body))
			}
		})
	}
}

func TestMetadataServiceRootResponse(t *testing.T) {
	log := slog.Default()

	svc, err := NewService(log, &mockInstanceLookup{}, "http://localhost:8080", "127.0.0.1:18080", nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", svc.handleRoot)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var meta MetadataResponse
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if meta.Name != "test-box" {
		t.Errorf("got name %q, want %q", meta.Name, "test-box")
	}
}

func TestMetadataServiceLoggingMiddleware(t *testing.T) {
	// Capture log output using our tracing handler
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, nil)
	tracingHandler := tracing.NewHandler(jsonHandler)
	log := slog.New(tracingHandler)

	svc, err := NewService(log, &mockInstanceLookup{}, "http://localhost:8080", "127.0.0.1:18080", nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	// Create a request using the middleware directly (not starting the actual server)
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", svc.handleRoot)
	handler := svc.loggerMiddleware(mux)

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", w.Code, http.StatusOK)
	}

	// Parse log output (should be a single JSON line)
	var logEntry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("failed to parse log output: %v\nLog output: %s", err, buf.String())
	}

	// Verify trace_id is present
	if _, ok := logEntry["trace_id"]; !ok {
		t.Errorf("trace_id not found in log. Log: %v", logEntry)
	}

	// Verify log_type is present
	if logType, ok := logEntry["log_type"]; !ok {
		t.Errorf("log_type not found in log. Log: %v", logEntry)
	} else if logType != "http_request" {
		t.Errorf("expected log_type=http_request, got %v", logType)
	}

	// Verify method is present
	if method, ok := logEntry["method"]; !ok {
		t.Errorf("method not found in log. Log: %v", logEntry)
	} else if method != "GET" {
		t.Errorf("expected method=GET, got %v", method)
	}

	// Verify uri is present
	if uri, ok := logEntry["uri"]; !ok {
		t.Errorf("uri not found in log. Log: %v", logEntry)
	} else if uri != "/" {
		t.Errorf("expected uri=/, got %v", uri)
	}

	// Verify remote_ip is present
	if remoteIP, ok := logEntry["remote_ip"]; !ok {
		t.Errorf("remote_ip not found in log. Log: %v", logEntry)
	} else if remoteIP != "10.0.0.1" {
		t.Errorf("expected remote_ip=10.0.0.1, got %v", remoteIP)
	}

	// Verify vm_name is present (from mockInstanceLookup)
	if vmName, ok := logEntry["vm_name"]; !ok {
		t.Errorf("vm_name not found in log. Log: %v", logEntry)
	} else if vmName != "test-box" {
		t.Errorf("expected vm_name=test-box, got %v", vmName)
	}
}

func TestMetadataServiceEmailProxyHandler(t *testing.T) {
	log := slog.Default()

	// Start a mock backend that receives the proxied request
	var receivedBoxHeader string
	var receivedPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBoxHeader = r.Header.Get("X-Exedev-Box")
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true}`))
	}))
	defer backend.Close()

	svc, err := NewService(log, &mockInstanceLookup{}, backend.URL, "127.0.0.1:18080", nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	// Test the handler directly using httptest.NewRequest
	t.Run("POST email send proxies with correct headers", func(t *testing.T) {
		body := bytes.NewBufferString(`{"to":"test@example.com","subject":"Test","body":"Hello"}`)
		req := httptest.NewRequest("POST", "/gateway/email/send", body)
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "10.0.0.1:12345"
		w := httptest.NewRecorder()

		svc.handleEmailProxy(w, req)

		// Expect 200 from the mock backend
		if w.Code != http.StatusOK {
			t.Errorf("got status %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
		}

		// Verify the proxy set the correct headers
		if receivedBoxHeader != "test-box" {
			t.Errorf("got X-Exedev-Box header %q, want %q", receivedBoxHeader, "test-box")
		}

		// Verify the path was rewritten correctly
		if receivedPath != "/_/gateway/email/send" {
			t.Errorf("got path %q, want %q", receivedPath, "/_/gateway/email/send")
		}
	})
}

func TestMetadataServiceEmailProxyNoBox(t *testing.T) {
	log := slog.Default()

	// Mock that returns empty box name (box not found)
	emptyLookup := &emptyInstanceLookup{}

	svc, err := NewService(log, emptyLookup, "http://localhost:8080", "127.0.0.1:18080", nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	body := bytes.NewBufferString(`{"to":"test@example.com","subject":"Test","body":"Hello"}`)
	req := httptest.NewRequest("POST", "/gateway/email/send", body)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()

	svc.handleEmailProxy(w, req)

	// Expect Forbidden because no box was found for the IP
	if w.Code != http.StatusForbidden {
		t.Errorf("got status %d, want %d, body: %s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

type emptyInstanceLookup struct{}

func (m *emptyInstanceLookup) GetInstanceByIP(ctx context.Context, ip string) (id, name string, err error) {
	return "", "", nil // No box found
}

func TestMetadataServiceTraceIDIsUnique(t *testing.T) {
	seen := make(map[string]bool)

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	svc, err := NewService(log, &mockInstanceLookup{}, "http://localhost:8080", "127.0.0.1:18080", nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	// Create an inner handler that captures trace_id
	captureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := tracing.TraceIDFromContext(r.Context())
		if seen[traceID] {
			t.Errorf("loggerMiddleware generated duplicate trace_id: %s", traceID)
		}
		seen[traceID] = true
		svc.handleRoot(w, r)
	})
	testHandler := svc.loggerMiddleware(captureHandler)

	for range 100 {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		testHandler.ServeHTTP(w, req)
	}

	if len(seen) != 100 {
		t.Errorf("expected 100 unique trace_ids, got %d", len(seen))
	}
}
