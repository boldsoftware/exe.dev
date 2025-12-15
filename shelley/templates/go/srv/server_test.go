package srv

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerSetupAndHandlers(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test_server.sqlite3")
	t.Cleanup(func() { os.Remove(tempDB) })

	server, err := New(tempDB, "test-hostname")
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	// Test root endpoint without auth
	t.Run("root endpoint unauthenticated", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()

		server.HandleRoot(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		body := w.Body.String()
		if !strings.Contains(body, "test-hostname") {
			t.Errorf("expected page to show hostname, got body: %s", body)
		}
		if !strings.Contains(body, "Go Template Project") {
			t.Errorf("expected page to contain headline, got body: %s", body)
		}
		if strings.Contains(body, "Signed in as") {
			t.Errorf("expected page to not be logged in, got body: %s", body)
		}
		if !strings.Contains(body, "Not signed in") {
			t.Errorf("expected page to show 'Not signed in', got body: %s", body)
		}
	})

	// Test root endpoint with auth headers
	t.Run("root endpoint authenticated", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-ExeDev-UserID", "user123")
		req.Header.Set("X-ExeDev-Email", "test@example.com")
		w := httptest.NewRecorder()

		server.HandleRoot(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		body := w.Body.String()
		if !strings.Contains(body, "Signed in as") {
			t.Errorf("expected page to show logged in state, got body: %s", body)
		}
		if !strings.Contains(body, "test@example.com") {
			t.Error("expected page to show user email")
		}
	})

	// Test view counter functionality
	t.Run("view counter increments", func(t *testing.T) {
		// Make first request
		req1 := httptest.NewRequest(http.MethodGet, "/", nil)
		req1.Header.Set("X-ExeDev-UserID", "counter-test")
		req1.RemoteAddr = "192.168.1.100:12345"
		w1 := httptest.NewRecorder()
		server.HandleRoot(w1, req1)

		// Should show "1 times" or similar
		body1 := w1.Body.String()
		if !strings.Contains(body1, "1</strong> times") {
			t.Error("expected first visit to show 1 time")
		}

		// Make second request with same user
		req2 := httptest.NewRequest(http.MethodGet, "/", nil)
		req2.Header.Set("X-ExeDev-UserID", "counter-test")
		req2.RemoteAddr = "192.168.1.100:12345"
		w2 := httptest.NewRecorder()
		server.HandleRoot(w2, req2)

		// Should show "2 times" or similar
		body2 := w2.Body.String()
		if !strings.Contains(body2, "2</strong> times") {
			t.Error("expected second visit to show 2 times")
		}
	})
}

func TestUtilityFunctions(t *testing.T) {
	t.Run("mainDomainFromHost function", func(t *testing.T) {
		tests := []struct {
			input    string
			expected string
		}{
			{"example.exe.cloud:8080", "exe.cloud:8080"},
			{"example.exe.dev", "exe.dev"},
			{"example.exe.cloud", "exe.cloud"},
		}

		for _, test := range tests {
			result := mainDomainFromHost(test.input)
			if result != test.expected {
				t.Errorf("mainDomainFromHost(%q) = %q, expected %q", test.input, result, test.expected)
			}
		}
	})
}
