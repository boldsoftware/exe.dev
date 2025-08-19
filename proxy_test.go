package exe

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyRequestRouting(t *testing.T) {
	// Create a test server
	server := &Server{
		quietMode: true,
	}

	tests := []struct {
		name           string
		host           string
		expectedProxy  bool
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "production proxy request",
			host:           "myapp.myteam.exe.dev",
			expectedProxy:  true,
			expectedStatus: 200,
			expectedBody:   "Proxy handler called for host: myapp.myteam.exe.dev",
		},
		{
			name:           "development proxy request",
			host:           "myapp.myteam.localhost",
			expectedProxy:  true,
			expectedStatus: 200,
			expectedBody:   "Proxy handler called for host: myapp.myteam.localhost",
		},
		{
			name:           "production proxy request with port",
			host:           "myapp.myteam.exe.dev:8080",
			expectedProxy:  true,
			expectedStatus: 200,
			expectedBody:   "Proxy handler called for host: myapp.myteam.exe.dev",
		},
		{
			name:           "main domain request",
			host:           "exe.dev",
			expectedProxy:  false,
			expectedStatus: 404, // Test server doesn't have full routing
			expectedBody:   "",
		},
		{
			name:           "localhost main request",
			host:           "localhost:8080",
			expectedProxy:  false,
			expectedStatus: 404, // Test server doesn't have full routing
			expectedBody:   "",
		},
		{
			name:           "unrelated domain",
			host:           "example.com",
			expectedProxy:  false,
			expectedStatus: 404, // Test server doesn't have full routing
			expectedBody:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test isProxyRequest logic
			got := server.isProxyRequest(tt.host)
			if got != tt.expectedProxy {
				t.Errorf("isProxyRequest(%q) = %v, want %v", tt.host, got, tt.expectedProxy)
			}

			// Test actual HTTP routing
			req := httptest.NewRequest("GET", "/test", nil)
			req.Host = tt.host
			w := httptest.NewRecorder()

			server.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("ServeHTTP status = %d, want %d", w.Code, tt.expectedStatus)
			}

			if tt.expectedProxy && tt.expectedBody != "" {
				body := w.Body.String()
				if !strings.Contains(body, tt.expectedBody) {
					t.Errorf("ServeHTTP body = %q, want to contain %q", body, tt.expectedBody)
				}
			}
		})
	}
}

func TestProxyRequestDetails(t *testing.T) {
	// Create a test server
	server := &Server{
		quietMode: true,
	}

	// Test that the proxy handler shows request details
	req := httptest.NewRequest("POST", "/api/test?param=value", strings.NewReader("test body"))
	req.Host = "webapp.devteam.exe.dev"
	req.Header.Set("X-Custom-Header", "test-value")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("ServeHTTP status = %d, want %d", w.Code, 200)
	}

	body := w.Body.String()
	expected := []string{
		"Proxy handler called for host: webapp.devteam.exe.dev",
		"Request method: POST",
		"Request path: /api/test",
		"X-Custom-Header: test-value",
		"Content-Type: application/json",
	}

	for _, exp := range expected {
		if !strings.Contains(body, exp) {
			t.Errorf("ServeHTTP body missing %q\nGot: %s", exp, body)
		}
	}
}
