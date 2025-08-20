package exe

import (
	"database/sql"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestProxyRequestRouting(t *testing.T) {
	// Create temporary database
	dbFile := "/tmp/test_proxy_routing.db"
	defer os.Remove(dbFile)

	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Create tables
	_, err = db.Exec(`
		CREATE TABLE machines (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			team_name TEXT NOT NULL,
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'stopped',
			image TEXT,
			container_id TEXT,
			created_by_fingerprint TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_started_at DATETIME,
			docker_host TEXT,
			routes TEXT
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create machines table: %v", err)
	}

	// Create a test server
	server := &Server{
		quietMode: true,
		db:        db,
		testMode:  true,
	}

	// Create a test machine with default routes
	err = server.createMachine("test-fingerprint", "myteam", "myapp", "container123", "nginx")
	if err != nil {
		t.Fatalf("Failed to create test machine: %v", err)
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
			expectedStatus: 307, // Should redirect to auth for private routes
			expectedBody:   "auth?redirect=",
		},
		{
			name:           "development proxy request",
			host:           "myapp.myteam.localhost",
			expectedProxy:  true,
			expectedStatus: 307, // Should redirect to auth for private routes
			expectedBody:   "auth?redirect=",
		},
		{
			name:           "production proxy request with port",
			host:           "myapp.myteam.exe.dev:8080",
			expectedProxy:  true,
			expectedStatus: 307, // Should redirect to auth for private routes
			expectedBody:   "auth?redirect=",
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
	// Create temporary database
	dbFile := "/tmp/test_proxy_details.db"
	defer os.Remove(dbFile)

	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Create tables
	_, err = db.Exec(`
		CREATE TABLE machines (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			team_name TEXT NOT NULL,
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'stopped',
			image TEXT,
			container_id TEXT,
			created_by_fingerprint TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_started_at DATETIME,
			docker_host TEXT,
			routes TEXT
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create machines table: %v", err)
	}

	// Create a test server
	server := &Server{
		quietMode: true,
		db:        db,
		testMode:  true,
	}

	// Create a test machine
	err = server.createMachine("test-fingerprint", "devteam", "webapp", "container456", "nginx")
	if err != nil {
		t.Fatalf("Failed to create test machine: %v", err)
	}

	// Test that the proxy handler shows request details
	req := httptest.NewRequest("POST", "/api/test?param=value", strings.NewReader("test body"))
	req.Host = "webapp.devteam.exe.dev"
	req.Header.Set("X-Custom-Header", "test-value")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should get 307 redirect to auth due to authentication requirement
	if w.Code != 307 {
		t.Errorf("ServeHTTP status = %d, want %d", w.Code, 307)
	}

	// Check the Location header for the redirect
	location := w.Header().Get("Location")
	if !strings.Contains(location, "auth?redirect=") {
		t.Errorf("Expected auth redirect in Location header, got: %s", location)
	}
}
