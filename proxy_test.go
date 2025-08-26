package exe

import (
	"database/sql"
	"net/http"
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

	// Create users table first
	_, err = db.Exec(`
		CREATE TABLE users (
			user_id TEXT PRIMARY KEY,
			
			email TEXT UNIQUE NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create users table: %v", err)
	}

	// Create allocs table
	_, err = db.Exec(`
		CREATE TABLE allocs (
			alloc_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			alloc_type TEXT NOT NULL DEFAULT 'medium',
			region TEXT NOT NULL DEFAULT 'aws-us-west-2',
			docker_host TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			stripe_customer_id TEXT,
			billing_email TEXT,
			FOREIGN KEY (user_id) REFERENCES users(user_id)
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create allocs table: %v", err)
	}

	// Create machines table with alloc_id instead of team_name
	_, err = db.Exec(`
		CREATE TABLE machines (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			alloc_id TEXT NOT NULL,
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'stopped',
			image TEXT,
			container_id TEXT,
			created_by_user_id TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_started_at DATETIME,
			docker_host TEXT,
			routes TEXT,
			ssh_server_identity_key TEXT,
			ssh_authorized_keys TEXT,
			ssh_ca_public_key TEXT,
			ssh_host_certificate TEXT,
			ssh_client_private_key TEXT,
			ssh_port INTEGER,
			UNIQUE(name),
			FOREIGN KEY (alloc_id) REFERENCES allocs(alloc_id)
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create machines table: %v", err)
	}

	// Create ssh_keys table without default_team
	_, err = db.Exec(`
		CREATE TABLE ssh_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			public_key TEXT UNIQUE NOT NULL,
			added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_used_at DATETIME,
			verified BOOLEAN DEFAULT FALSE,
			FOREIGN KEY (user_id) REFERENCES users(user_id)
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create ssh_keys table: %v", err)
	}

	// Create test user
	userID := "usr1234567890123" // test user ID
	_, err = db.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// Create alloc for test user
	allocID := "alloc_" + userID
	_, err = db.Exec(`INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email) VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test@example.com')`, allocID, userID)
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Add SSH key for test user
	_, err = db.Exec(`INSERT INTO ssh_keys (user_id, public_key, verified) VALUES (?, ?, 1)`, userID, "ssh-rsa dummy-test-key test@example.com")
	if err != nil {
		t.Fatalf("Failed to create SSH key: %v", err)
	}
	// Create a test server
	server := &Server{
		quietMode: true,
		db:        db,
		testMode:  true,
	}

	// Create a test machine with default routes
	err = server.createMachine(userID, allocID, "myapp", "container123", "nginx")
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
			host:           "myapp.exe.dev",
			expectedProxy:  true,
			expectedStatus: 307, // Should redirect to auth for private routes
			expectedBody:   "auth?redirect=",
		},
		{
			name:           "development proxy request",
			host:           "myapp.localhost",
			expectedProxy:  true,
			expectedStatus: 307, // Should redirect to auth for private routes
			expectedBody:   "auth?redirect=",
		},
		{
			name:           "production proxy request with port",
			host:           "myapp.exe.dev:8080",
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

	// Create users table first
	_, err = db.Exec(`
		CREATE TABLE users (
			user_id TEXT PRIMARY KEY,
			
			email TEXT UNIQUE NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create users table: %v", err)
	}

	// Create allocs table
	_, err = db.Exec(`
		CREATE TABLE allocs (
			alloc_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			alloc_type TEXT NOT NULL DEFAULT 'medium',
			region TEXT NOT NULL DEFAULT 'aws-us-west-2',
			docker_host TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			stripe_customer_id TEXT,
			billing_email TEXT,
			FOREIGN KEY (user_id) REFERENCES users(user_id)
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create allocs table: %v", err)
	}

	// Create machines table with alloc_id instead of team_name
	_, err = db.Exec(`
		CREATE TABLE machines (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			alloc_id TEXT NOT NULL,
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'stopped',
			image TEXT,
			container_id TEXT,
			created_by_user_id TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_started_at DATETIME,
			docker_host TEXT,
			routes TEXT,
			ssh_server_identity_key TEXT,
			ssh_authorized_keys TEXT,
			ssh_ca_public_key TEXT,
			ssh_host_certificate TEXT,
			ssh_client_private_key TEXT,
			ssh_port INTEGER,
			UNIQUE(name),
			FOREIGN KEY (alloc_id) REFERENCES allocs(alloc_id)
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create machines table: %v", err)
	}
	// Create ssh_keys table without default_team
	_, err = db.Exec(`
		CREATE TABLE ssh_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			public_key TEXT UNIQUE NOT NULL,
			added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_used_at DATETIME,
			verified BOOLEAN DEFAULT FALSE,
			FOREIGN KEY (user_id) REFERENCES users(user_id)
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create ssh_keys table: %v", err)
	}

	// Create test user
	userID := "usr2234567890123" // test user ID
	_, err = db.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// Create alloc for test user
	allocID := "alloc_" + userID
	_, err = db.Exec(`INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email) VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test@example.com')`, allocID, userID)
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Add SSH key for test user
	_, err = db.Exec(`INSERT INTO ssh_keys (user_id, public_key, verified) VALUES (?, ?, 1)`, userID, "ssh-rsa dummy-test-key test@example.com")
	if err != nil {
		t.Fatalf("Failed to create SSH key: %v", err)
	}

	// Create a test server
	server := &Server{
		quietMode: true,
		db:        db,
		testMode:  true,
	}

	// Create a test machine
	err = server.createMachine(userID, allocID, "webapp", "container456", "nginx")
	if err != nil {
		t.Fatalf("Failed to create test machine: %v", err)
	}

	// Test that the proxy handler shows request details
	req := httptest.NewRequest("POST", "/api/test?param=value", strings.NewReader("test body"))
	req.Host = "webapp.exe.dev"
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

	// Use proper migration system
	err = runMigrations(db)
	if err != nil {
		t.Fatalf("Failed to run migrations: %v", err)
	}

	// Create a test user
	userID, err := generateUserID()
	if err != nil {
		t.Fatalf("Failed to generate user ID: %v", err)
	}

	_, err = db.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// Create SSH key for the test user
	_, err = db.Exec(`INSERT INTO ssh_keys (user_id, public_key, verified) VALUES (?, ?, 1)`,
		userID, "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDtest...")
	if err != nil {
		t.Fatalf("Failed to create SSH key: %v", err)
	}

	// Create alloc for test user
	allocID := "test-alloc-" + userID
	_, err = db.Exec(`INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email) VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test@example.com')`, allocID, userID)
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Create a test machine with a private route
	_, err = db.Exec(`
		INSERT INTO machines (alloc_id, name, image, container_id, created_by_user_id, docker_host, routes, 
		                     ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key, 
		                     ssh_host_certificate, ssh_client_private_key, ssh_port) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, allocID, "testmachine", "test-image", "test-container-id", userID, "unix:///var/run/docker.sock", `[
		{
			"name": "default",
			"policy": "private",
			"methods": ["*"],
			"paths": {"prefix": "/"},
			"priority": 1,
			"ports": [80]
		}
	]`, "test-identity-key", "test-authorized-keys", "test-ca-key", "test-host-cert", "test-client-key", 2222)
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
		machine, err := server.getMachineByName("testmachine")
		if err != nil {
			t.Fatalf("Test machine not found: %v", err)
		}
		if machine == nil {
			t.Fatal("Machine is nil")
		}

		req := httptest.NewRequest("GET", "http://testmachine.localhost/", nil)
		req.Host = "testmachine.localhost"
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
		secret, err := server.createMagicSecret("test-user-id", "testmachine", "/original-path")
		if err != nil {
			t.Fatalf("Failed to create magic secret: %v", err)
		}

		// Request magic URL
		req := httptest.NewRequest("GET", "http://testmachine.localhost/__exe.dev/auth?secret="+secret+"&redirect=/custom-redirect", nil)
		req.Host = "testmachine.localhost"
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
		req := httptest.NewRequest("GET", "http://testmachine.localhost/__exe.dev/auth?secret=invalid-secret", nil)
		req.Host = "testmachine.localhost"
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		if w.Code != 401 { // StatusUnauthorized
			t.Errorf("Expected status 401, got %d", w.Code)
		}
	})

	// Test 4: Magic URL without secret should return error
	t.Run("missing_secret_returns_error", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://testmachine.localhost/__exe.dev/auth", nil)
		req.Host = "testmachine.localhost"
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		if w.Code != 400 { // StatusBadRequest
			t.Errorf("Expected status 400, got %d", w.Code)
		}
	})

	// Test 5: Magic secret should be consumed (single use)
	t.Run("magic_secret_single_use", func(t *testing.T) {
		// Create a magic secret
		secret, err := server.createMagicSecret("test-user-id", "testmachine", "/original-path")
		if err != nil {
			t.Fatalf("Failed to create magic secret: %v", err)
		}

		// First request should succeed
		req1 := httptest.NewRequest("GET", "http://testmachine.localhost/__exe.dev/auth?secret="+secret, nil)
		req1.Host = "testmachine.localhost"
		w1 := httptest.NewRecorder()

		server.ServeHTTP(w1, req1)

		if w1.Code != 307 {
			t.Errorf("First request should succeed with 307, got %d", w1.Code)
		}

		// Second request should fail (secret consumed)
		req2 := httptest.NewRequest("GET", "http://testmachine.localhost/__exe.dev/auth?secret="+secret, nil)
		req2.Host = "testmachine.localhost"
		w2 := httptest.NewRecorder()

		server.ServeHTTP(w2, req2)

		if w2.Code != 401 {
			t.Errorf("Second request should fail with 401, got %d", w2.Code)
		}
	})
}

// TestProxyDebugPath tests the debug path handling in dev mode
func TestProxyDebugPath(t *testing.T) {
	// Create temporary database
	dbFile := "/tmp/test_proxy_debug.db"
	defer os.Remove(dbFile)

	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Create allocs table
	_, err = db.Exec(`
		CREATE TABLE allocs (
			alloc_id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			alloc_type TEXT NOT NULL DEFAULT 'medium',
			region TEXT NOT NULL DEFAULT 'aws-us-west-2',
			docker_host TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			stripe_customer_id TEXT,
			billing_email TEXT
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create allocs table: %v", err)
	}

	// Create tables with alloc_id instead of team_name
	_, err = db.Exec(`
		CREATE TABLE machines (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			alloc_id TEXT NOT NULL,
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'stopped',
			image TEXT,
			container_id TEXT,
			created_by_user_id TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_started_at DATETIME,
			docker_host TEXT,
			routes TEXT,
			ssh_server_identity_key TEXT,
			ssh_authorized_keys TEXT,
			ssh_ca_public_key TEXT,
			ssh_host_certificate TEXT,
			ssh_client_private_key TEXT,
			ssh_port INTEGER,
			UNIQUE(name),
			FOREIGN KEY (alloc_id) REFERENCES allocs(alloc_id)
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create machines table: %v", err)
	}

	// Create users table
	_, err = db.Exec(`
		CREATE TABLE users (
			user_id TEXT PRIMARY KEY,
			email TEXT UNIQUE NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create users table: %v", err)
	}

	// Create test user
	_, err = db.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, "test-user", "test@example.com")
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// Create alloc for test
	allocID := "test-alloc-debug"
	_, err = db.Exec(`INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email) VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test@example.com')`, allocID, "test-user")
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Create a test machine
	_, err = db.Exec(`
		INSERT INTO machines (alloc_id, name, image, container_id, created_by_user_id, docker_host, routes, 
		                     ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key, 
		                     ssh_host_certificate, ssh_client_private_key, ssh_port) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, allocID, "testmachine", "test-image", "test-container-id", "test-user", "unix:///var/run/docker.sock", `[
		{
			"name": "default",
			"policy": "public",
			"methods": ["*"],
			"paths": {"prefix": "/"},
			"priority": 1,
			"ports": [80]
		}
	]`, "test-identity-key", "test-authorized-keys", "test-ca-key", "test-host-cert", "test-client-key", 2222)
	if err != nil {
		t.Fatalf("Failed to insert test machine: %v", err)
	}

	tests := []struct {
		name     string
		devMode  string
		path     string
		expected string
	}{
		{
			name:     "debug_path_in_dev_mode",
			devMode:  "local",
			path:     "/__exe.dev/debug",
			expected: "Proxy handler - Route matched!",
		},
		{
			name:     "debug_path_in_prod_mode",
			devMode:  "",
			path:     "/__exe.dev/debug",
			expected: "Test proxy response",
		},
		{
			name:     "regular_path_in_dev_mode",
			devMode:  "local",
			path:     "/",
			expected: "Test proxy response",
		},
		{
			name:     "regular_path_in_prod_mode",
			devMode:  "",
			path:     "/",
			expected: "Test proxy response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test server
			server := &Server{
				quietMode: true,
				db:        db,
				testMode:  true,
				devMode:   tt.devMode,
			}

			req := httptest.NewRequest("GET", "http://testmachine.localhost"+tt.path, nil)
			req.Host = "testmachine.localhost"
			w := httptest.NewRecorder()

			server.handleProxyRequest(w, req)

			if !strings.Contains(w.Body.String(), tt.expected) {
				t.Errorf("Expected body to contain '%s', got: %s", tt.expected, w.Body.String())
			}
		})
	}
}

func TestProxyLogoutFlow(t *testing.T) {
	// Create temporary database
	dbFile := "/tmp/test_proxy_logout_5dd277dc.db"
	defer os.Remove(dbFile)

	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Use proper migration system
	err = runMigrations(db)
	if err != nil {
		t.Fatalf("Failed to run migrations: %v", err)
	}

	// Create a test user
	userID, err := generateUserID()
	if err != nil {
		t.Fatalf("Failed to generate user ID: %v", err)
	}

	_, err = db.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test-logout@example.com")
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// Create SSH key for the test user
	_, err = db.Exec(`INSERT INTO ssh_keys (user_id, public_key, verified) VALUES (?, ?, 1)`,
		userID, "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDtest...")
	if err != nil {
		t.Fatalf("Failed to create SSH key: %v", err)
	}

	// Create alloc for test user
	allocID := "test-alloc-" + userID
	_, err = db.Exec(`INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email) VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test@example.com')`, allocID, userID)
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Create a test machine with a private route
	_, err = db.Exec(`
		INSERT INTO machines (alloc_id, name, image, container_id, created_by_user_id, docker_host, routes, 
						 ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key, 
						 ssh_host_certificate, ssh_client_private_key, ssh_port) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, allocID, "testmachine", "test-image", "test-container-id", userID, "unix:///var/run/docker.sock", `[
		{
			"name": "default",
			"port": 80,
			"methods": "*",
			"prefix": "/",
			"policy": "private",
			"priority": 100
		}
	]`, "test-key", "test-keys", "test-ca", "test-cert", "test-client-key", 2222)
	if err != nil {
		t.Fatalf("Failed to create test machine: %v", err)
	}

	server := &Server{
		quietMode:    true,
		db:           db,
		testMode:     true,
		magicSecrets: make(map[string]*MagicSecret),
	}

	// Test 1: Logout without authentication should still work (redirect to root)
	t.Run("logout_without_auth", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://testmachine.localhost/__exe.dev/logout", nil)
		req.Host = "testmachine.localhost"
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		if w.Code != 307 { // StatusTemporaryRedirect
			t.Errorf("Expected redirect status 307, got %d", w.Code)
		}

		// Check redirect location
		location := w.Header().Get("Location")
		if location != "/" {
			t.Errorf("Expected redirect to '/', got '%s'", location)
		}

		// Check that logout cookie was set
		cookieFound := false
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == "exe-proxy-auth" && cookie.Value == "" && cookie.MaxAge == -1 {
				cookieFound = true
				break
			}
		}
		if !cookieFound {
			t.Error("Expected logout cookie to be set")
		}
	})

	// Test 2: Logout after authentication should clear cookie and database entry
	t.Run("logout_after_auth", func(t *testing.T) {
		// First authenticate the user
		secret, err := server.createMagicSecret(userID, "testmachine", "")
		if err != nil {
			t.Fatalf("Failed to create magic secret: %v", err)
		}

		// Use magic URL to authenticate
		req1 := httptest.NewRequest("GET", "http://testmachine.localhost/__exe.dev/auth?secret="+secret, nil)
		req1.Host = "testmachine.localhost"
		w1 := httptest.NewRecorder()

		server.ServeHTTP(w1, req1)

		if w1.Code != 307 {
			t.Fatalf("Auth should succeed with 307, got %d", w1.Code)
		}

		// Get the auth cookie that was set
		var authCookie *http.Cookie
		for _, cookie := range w1.Result().Cookies() {
			if cookie.Name == "exe-proxy-auth" {
				authCookie = cookie
				break
			}
		}
		if authCookie == nil {
			t.Fatal("Auth cookie should have been set")
		}

		// Verify the cookie is valid by checking database
		var count int
		err = db.QueryRow("SELECT COUNT(*) FROM auth_cookies WHERE cookie_value = ? AND user_id = ?", authCookie.Value, userID).Scan(&count)
		if err != nil || count != 1 {
			t.Fatal("Auth cookie should exist in database")
		}

		// Now logout
		req2 := httptest.NewRequest("GET", "http://testmachine.localhost/__exe.dev/logout", nil)
		req2.Host = "testmachine.localhost"
		req2.AddCookie(authCookie) // Send the auth cookie
		w2 := httptest.NewRecorder()

		server.ServeHTTP(w2, req2)

		if w2.Code != 307 {
			t.Errorf("Expected redirect status 307, got %d", w2.Code)
		}

		// Check redirect location
		location := w2.Header().Get("Location")
		if location != "/" {
			t.Errorf("Expected redirect to '/', got '%s'", location)
		}

		// Check that logout cookie was set
		logoutCookieFound := false
		for _, cookie := range w2.Result().Cookies() {
			if cookie.Name == "exe-proxy-auth" && cookie.Value == "" && cookie.MaxAge == -1 {
				logoutCookieFound = true
				break
			}
		}
		if !logoutCookieFound {
			t.Error("Expected logout cookie to be set")
		}

		// Verify the auth cookie was deleted from database
		err = db.QueryRow("SELECT COUNT(*) FROM auth_cookies WHERE cookie_value = ? AND user_id = ?", authCookie.Value, userID).Scan(&count)
		if err != nil || count != 0 {
			t.Error("Auth cookie should have been deleted from database")
		}
	})

	// Test 3: Logout should only delete the specific cookie, not other cookies for the same user
	t.Run("logout_preserves_other_sessions", func(t *testing.T) {
		// Create two separate auth sessions for the same user
		secret1, err := server.createMagicSecret(userID, "testmachine", "")
		if err != nil {
			t.Fatalf("Failed to create magic secret 1: %v", err)
		}

		secret2, err := server.createMagicSecret(userID, "testmachine", "")
		if err != nil {
			t.Fatalf("Failed to create magic secret 2: %v", err)
		}

		// Auth with first secret to create first session
		req1 := httptest.NewRequest("GET", "http://testmachine.localhost/__exe.dev/auth?secret="+secret1, nil)
		req1.Host = "testmachine.localhost"
		w1 := httptest.NewRecorder()
		server.ServeHTTP(w1, req1)

		// Auth with second secret to create second session
		req2 := httptest.NewRequest("GET", "http://testmachine.localhost/__exe.dev/auth?secret="+secret2, nil)
		req2.Host = "testmachine.localhost"
		w2 := httptest.NewRecorder()
		server.ServeHTTP(w2, req2)

		if w1.Code != 307 || w2.Code != 307 {
			t.Fatal("Both authentications should succeed")
		}

		// Get both auth cookies
		var cookie1, cookie2 *http.Cookie
		for _, cookie := range w1.Result().Cookies() {
			if cookie.Name == "exe-proxy-auth" {
				cookie1 = cookie
				break
			}
		}
		for _, cookie := range w2.Result().Cookies() {
			if cookie.Name == "exe-proxy-auth" {
				cookie2 = cookie
				break
			}
		}

		if cookie1 == nil || cookie2 == nil {
			t.Fatal("Both auth cookies should have been set")
		}

		// Verify we have 2 auth cookies in database for this user
		var count int
		err = db.QueryRow("SELECT COUNT(*) FROM auth_cookies WHERE user_id = ?", userID).Scan(&count)
		if err != nil || count != 2 {
			t.Fatalf("Should have 2 auth cookies for user, got %d", count)
		}

		// Logout using only the first cookie
		req3 := httptest.NewRequest("GET", "http://testmachine.localhost/__exe.dev/logout", nil)
		req3.Host = "testmachine.localhost"
		req3.AddCookie(cookie1) // Only send the first cookie
		w3 := httptest.NewRecorder()
		server.ServeHTTP(w3, req3)

		if w3.Code != 307 {
			t.Errorf("Logout should succeed with 307, got %d", w3.Code)
		}

		// Verify only the first cookie was deleted, second one remains
		err = db.QueryRow("SELECT COUNT(*) FROM auth_cookies WHERE user_id = ?", userID).Scan(&count)
		if err != nil || count != 1 {
			t.Errorf("Should have 1 auth cookie remaining for user, got %d", count)
		}

		// Verify the remaining cookie is the second one
		var remainingCookie string
		err = db.QueryRow("SELECT cookie_value FROM auth_cookies WHERE user_id = ?", userID).Scan(&remainingCookie)
		if err != nil {
			t.Fatal("Failed to get remaining cookie")
		}
		if remainingCookie != cookie2.Value {
			t.Errorf("Expected remaining cookie to be cookie2, but got different value")
		}

		// Verify the first cookie was deleted
		err = db.QueryRow("SELECT COUNT(*) FROM auth_cookies WHERE cookie_value = ?", cookie1.Value).Scan(&count)
		if err != nil || count != 0 {
			t.Error("First cookie should have been deleted from database")
		}
	})
}
