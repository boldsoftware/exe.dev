package srv

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestServerSetupAndHandlers(t *testing.T) {
	// Create a temporary database
	tempDB := t.TempDir() + "/test_server.sqlite3"
	defer os.Remove(tempDB)

	// Create and setup server
	server := New(nil, "test-hostname")
	if err := server.SetupDatabase(tempDB); err != nil {
		t.Fatalf("failed to setup database: %v", err)
	}
	defer server.DB.Close()

	// Test health endpoint
	t.Run("health endpoint", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		w := httptest.NewRecorder()

		server.HandleHealth(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		body := w.Body.String()
		if body != "OK\n" {
			t.Errorf("expected body 'OK\\n', got %q", body)
		}
	})

	// Test root endpoint without auth
	t.Run("root endpoint unauthenticated", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()

		server.HandleRoot(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		body := w.Body.String()
		if !strings.Contains(body, "Welcome to exe.dev") {
			t.Errorf("expected page to contain 'Welcome to exe.dev', got body: %s", body)
		}
		if !strings.Contains(body, "not logged in") {
			t.Errorf("expected page to show not logged in state, got body: %s", body)
		}
		if !strings.Contains(body, "test-hostname") {
			t.Errorf("expected page to show hostname, got body: %s", body)
		}
	})

	// Test root endpoint with auth headers
	t.Run("root endpoint authenticated", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Exedev-Userid", "user123")
		req.Header.Set("X-Exedev-Email", "test@example.com")
		w := httptest.NewRecorder()

		server.HandleRoot(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		body := w.Body.String()
		if !strings.Contains(body, "logged in as") {
			t.Errorf("expected page to show logged in state, got body: %s", body)
		}
		if !strings.Contains(body, "test@example.com") {
			t.Error("expected page to show user email")
		}
		if !strings.Contains(body, "user123") {
			t.Error("expected page to show user ID")
		}
	})

	// Test view counter functionality
	t.Run("view counter increments", func(t *testing.T) {
		// Make first request
		req1 := httptest.NewRequest(http.MethodGet, "/", nil)
		req1.Header.Set("X-Exedev-Userid", "counter-test")
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
		req2.Header.Set("X-Exedev-Userid", "counter-test")
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

func TestServerDatabaseSetup(t *testing.T) {
	server := New(nil, "test")

	// Test with empty path (should use default)
	tempDir := t.TempDir()
	originalWd, _ := os.Getwd()
	os.Chdir(tempDir)
	defer os.Chdir(originalWd)

	if err := server.SetupDatabase(""); err != nil {
		t.Errorf("expected empty path to work with default, got error: %v", err)
	}
	if server.DB != nil {
		server.DB.Close()
	}

	// Test with invalid path (should fail)
	server = New(nil, "test")
	if err := server.SetupDatabase("/invalid/path/that/does/not/exist.db"); err == nil {
		t.Error("expected invalid database path to return error")
	}
}

func TestUtilityFunctions(t *testing.T) {
	t.Run("sqlNull function", func(t *testing.T) {
		// Test empty string returns nil
		result := sqlNull("")
		if result != nil {
			t.Errorf("expected nil for empty string, got %v", result)
		}

		// Test non-empty string returns pointer to string
		test := "test value"
		result = sqlNull(test)
		if result == nil {
			t.Error("expected non-nil for non-empty string")
		} else if *result != test {
			t.Errorf("expected %q, got %q", test, *result)
		}
	})

	t.Run("mainDomainFromHost function", func(t *testing.T) {
		tests := []struct {
			input    string
			expected string
		}{
			{"example.localhost:8080", "localhost:8080"},
			{"example.exe.dev", "exe.dev"},
			{"example.localhost", "localhost"},
			{"other.domain.com:9000", "other.domain.com"},
			{"plain.com", "plain.com"},
		}

		for _, test := range tests {
			result := mainDomainFromHost(test.input)
			if result != test.expected {
				t.Errorf("mainDomainFromHost(%q) = %q, expected %q", test.input, result, test.expected)
			}
		}
	})
}
