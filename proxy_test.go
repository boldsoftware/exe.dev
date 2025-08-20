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

func TestMagicAuthFlow(t *testing.T) {
	// Create temporary database
	dbFile := "/tmp/test_magic_auth.db"
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

	// Create auth_cookies table for cookie creation
	_, err = db.Exec(`
		CREATE TABLE auth_cookies (
			cookie_value TEXT PRIMARY KEY,
			user_fingerprint TEXT NOT NULL,
			domain TEXT NOT NULL,
			expires_at DATETIME NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_used_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create auth_cookies table: %v", err)
	}

	// Create a test machine with a private route
	_, err = db.Exec(`
		INSERT INTO machines (team_name, name, image, container_id, created_by_fingerprint, docker_host, routes) 
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "testteam", "testmachine", "test-image", "test-container-id", "test-fingerprint", "unix:///var/run/docker.sock", `[
		{
			"name": "default",
			"policy": "private",
			"methods": ["*"],
			"paths": {"prefix": "/"},
			"priority": 1,
			"ports": [80]
		}
	]`)
	if err != nil {
		t.Fatalf("Failed to insert test machine: %v", err)
	}

	// Create a test server
	server := &Server{
		quietMode:    true,
		db:           db,
		testMode:     true,
		devMode:      "local",
		magicSecrets: make(map[string]*MagicSecret),
	}

	// Test 1: Request to private route without auth should redirect to auth
	t.Run("unauthenticated_request_redirects_to_auth", func(t *testing.T) {
		// First verify the machine exists
		machine, err := server.getMachineByName("testteam", "testmachine")
		if err != nil {
			t.Fatalf("Test machine not found: %v", err)
		}
		if machine == nil {
			t.Fatal("Machine is nil")
		}

		req := httptest.NewRequest("GET", "http://testmachine.testteam.localhost/", nil)
		req.Host = "testmachine.testteam.localhost"
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		// Debug output
		if w.Code != 307 {
			t.Logf("Response body: %s", w.Body.String())
			t.Logf("Response headers: %+v", w.Header())
		}

		if w.Code != 307 { // StatusTemporaryRedirect
			t.Errorf("Expected redirect status 307, got %d", w.Code)
		}

		location := w.Header().Get("Location")
		if !strings.Contains(location, "localhost/auth?") {
			t.Errorf("Expected redirect to auth, got %s", location)
		}
		if !strings.Contains(location, "return_host=") {
			t.Errorf("Expected return_host in redirect URL, got %s", location)
		}
	})

	// Test 2: Magic URL with valid secret should set cookie and redirect
	t.Run("valid_magic_secret_sets_cookie", func(t *testing.T) {
		// Create a magic secret
		secret, err := server.createMagicSecret("test-fingerprint", "testteam", "/original-path")
		if err != nil {
			t.Fatalf("Failed to create magic secret: %v", err)
		}

		// Request magic URL
		req := httptest.NewRequest("GET", "http://testmachine.testteam.localhost/__exe.dev/auth?secret="+secret+"&redirect=/custom-redirect", nil)
		req.Host = "testmachine.testteam.localhost"
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		if w.Code != 307 { // StatusTemporaryRedirect
			t.Errorf("Expected redirect status 307, got %d", w.Code)
		}

		// Check that cookie was set
		cookieFound := false
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == "exe-proxy-auth" {
				cookieFound = true
				if cookie.Value == "" {
					t.Error("Cookie value should not be empty")
				}
				if cookie.MaxAge != 30*24*60*60 {
					t.Errorf("Expected cookie MaxAge %d, got %d", 30*24*60*60, cookie.MaxAge)
				}
				if cookie.Path != "/" {
					t.Errorf("Expected cookie Path '/', got '%s'", cookie.Path)
				}
				if !cookie.HttpOnly {
					t.Error("Cookie should be HttpOnly")
				}
			}
		}
		if !cookieFound {
			t.Error("Expected exe-proxy-auth cookie to be set")
		}

		// Check redirect URL (should use query param redirect over secret redirect)
		location := w.Header().Get("Location")
		if location != "/custom-redirect" {
			t.Errorf("Expected redirect to /custom-redirect, got %s", location)
		}
	})

	// Test 3: Magic URL with invalid secret should return error
	t.Run("invalid_magic_secret_returns_error", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://testmachine.testteam.localhost/__exe.dev/auth?secret=invalid-secret", nil)
		req.Host = "testmachine.testteam.localhost"
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		if w.Code != 401 { // StatusUnauthorized
			t.Errorf("Expected status 401, got %d", w.Code)
		}
	})

	// Test 4: Magic URL without secret should return error
	t.Run("missing_secret_returns_error", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://testmachine.testteam.localhost/__exe.dev/auth", nil)
		req.Host = "testmachine.testteam.localhost"
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		if w.Code != 400 { // StatusBadRequest
			t.Errorf("Expected status 400, got %d", w.Code)
		}
	})

	// Test 5: Magic secret should be consumed (single use)
	t.Run("magic_secret_single_use", func(t *testing.T) {
		// Create a magic secret
		secret, err := server.createMagicSecret("test-fingerprint", "testteam", "/original-path")
		if err != nil {
			t.Fatalf("Failed to create magic secret: %v", err)
		}

		// First request should succeed
		req1 := httptest.NewRequest("GET", "http://testmachine.testteam.localhost/__exe.dev/auth?secret="+secret, nil)
		req1.Host = "testmachine.testteam.localhost"
		w1 := httptest.NewRecorder()

		server.ServeHTTP(w1, req1)

		if w1.Code != 307 {
			t.Errorf("First request should succeed with 307, got %d", w1.Code)
		}

		// Second request should fail (secret consumed)
		req2 := httptest.NewRequest("GET", "http://testmachine.testteam.localhost/__exe.dev/auth?secret="+secret, nil)
		req2.Host = "testmachine.testteam.localhost"
		w2 := httptest.NewRecorder()

		server.ServeHTTP(w2, req2)

		if w2.Code != 401 {
			t.Errorf("Second request should fail with 401, got %d", w2.Code)
		}
	})
}
