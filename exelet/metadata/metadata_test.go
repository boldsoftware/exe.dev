package metadata

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
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
