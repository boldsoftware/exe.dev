package execore

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTerminalRouting(t *testing.T) {
	t.Parallel()
	s := newUnstartedServer(t)
	// Test that terminal subdomains are detected correctly
	tests := []struct {
		name     string
		host     string
		expected bool
	}{
		{"localhost terminal", "machine.xterm.exe.cloud", true},
		{"localhost terminal with port", "machine.xterm.exe.cloud:8080", true},
		{"production terminal", "machine.xterm.exe.dev", false}, // testing in dev mode
		{"regular proxy", "machine.exe.cloud", false},
		{"main domain", "localhost", false},
		{"invalid", "xterm.exe.cloud", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.isTerminalRequest(tt.host)
			if result != tt.expected {
				t.Errorf("isTerminalRequest(%q) = %v, want %v", tt.host, result, tt.expected)
			}
		})
	}
}

func TestTerminalStaticFiles(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Create a test user and auth them
	userID := "test-user-id"
	authCookie, err := server.createAuthCookie(t.Context(), userID, "testmachine.xterm.exe.cloud")
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Test static file serving
	req := httptest.NewRequest("GET", "/static/xterm.js", nil)
	req.Host = "testmachine.xterm.exe.cloud"
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: authCookie})
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200 for xterm.js, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Terminal") {
		t.Errorf("Expected xterm.js content, got response that doesn't contain 'Terminal'")
	}
}
