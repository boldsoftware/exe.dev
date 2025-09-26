package exe

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"exe.dev/sqlite"
)

// createTestRequest creates an http.Request with proper context for proxy tests
// For hosts without explicit ports, adds the server's HTTP port
func createTestRequestForServer(method, url, host string, server *Server) *http.Request {
	req := httptest.NewRequest(method, url, nil)

	// If host doesn't have a port, add the server's HTTP port
	finalHost := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		// No port in host, add server's HTTP port
		if server.httpLn != nil && server.httpLn.tcp != nil {
			finalHost = net.JoinHostPort(host, strconv.Itoa(server.httpLn.tcp.Port))
		} else {
			// Fallback to port 80 for test
			finalHost = net.JoinHostPort(host, "80")
		}
	}

	req.Host = finalHost

	// Set up mock local address context that the proxy handler expects
	// Parse the host to determine what port to mock
	var mockPort int
	if _, portStr, err := net.SplitHostPort(finalHost); err == nil {
		if port, err := strconv.Atoi(portStr); err == nil {
			mockPort = port
		} else {
			mockPort = 80 // fallback
		}
	} else {
		// No port specified, assume default
		mockPort = 80
	}

	// Create a mock net.Addr that represents the local address
	mockAddr := &net.TCPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: mockPort,
	}

	// Add the local address to the request context
	ctx := context.WithValue(req.Context(), http.LocalAddrContextKey, mockAddr)
	req = req.WithContext(ctx)

	return req
}

func TestProxyRequestRouting(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	email := "test@example.com"
	publicKey := "ssh-rsa dummy-test-key test@example.com"

	if err := server.createUser(t.Context(), publicKey, email); err != nil {
		t.Fatal(err)
	}
	user, err := server.getUserByPublicKey(t.Context(), publicKey)
	if err != nil {
		t.Fatalf("Failed to get user by public key: %v", err)
	}

	alloc, err := server.getUserAlloc(t.Context(), user.UserID)
	if err != nil {
		t.Fatalf("Failed to get alloc by user ID: %v", err)
	}

	// Create a test box with default routes
	server.createTestBox(t, user.UserID, alloc.AllocID, "myapp", "container123", "nginx")

	mainDomain := server.getMainDomain()
	mainHost := fmt.Sprintf("myapp.%s", mainDomain)

	tests := []struct {
		name           string
		host           string
		expectedProxy  bool
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "proxy request on main domain",
			host:           mainHost,
			expectedProxy:  true,
			expectedStatus: 307, // Should redirect to auth for private routes
			expectedBody:   "auth?redirect=",
		},
		{
			name:           "proxy request on main domain with explicit port",
			host:           fmt.Sprintf("%s:%d", mainHost, 8080),
			expectedProxy:  true,
			expectedStatus: 307, // Should redirect to auth for private routes
			expectedBody:   "auth?redirect=",
		},
		{
			name:           "main domain request",
			host:           mainDomain,
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
			req := createTestRequestForServer("GET", "/test", tt.host, server)
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

func TestMagicAuthFlow(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)
	server.magicSecrets = make(map[string]*MagicSecret)

	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDtest..."
	email := "test@example.com"
	err := server.createUser(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	user, err := server.getUserByPublicKey(t.Context(), publicKey)
	if err != nil {
		t.Fatalf("Failed to get test user: %v", err)
	}
	alloc, err := server.getUserAlloc(t.Context(), user.UserID)
	if err != nil {
		t.Fatalf("Failed to get user alloc: %v", err)
	}

	// Create a test box with a private route
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO boxes (alloc_id, name, status, image, container_id, created_by_user_id, routes,
			                     ssh_server_identity_key, ssh_authorized_keys, ssh_client_private_key, ssh_port)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, alloc.AllocID, "testbox", "running", "test-image", "test-container-id", user.UserID, `[
			{
				"name": "default",
				"policy": "private",
				"methods": ["*"],
				"paths": {"prefix": "/"},
				"priority": 1,
				"ports": [80]
			}
		]`, "test-identity-key", "test-authorized-keys", "test-client-key", 2222)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to insert test box: %v", err)
	}

	// Test 1: Request to private route without auth should redirect to auth
	t.Run("unauthenticated_request_redirects_to_auth", func(t *testing.T) {
		// First verify the box exists
		if server.isBoxNameAvailable(t.Context(), "testbox") {
			t.Fatalf("test box not found")
		}

		req := createTestRequestForServer("GET", "http://testbox.localhost/", "testbox.localhost", server)
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
		if !strings.Contains(location, "/auth?") {
			t.Errorf("Expected redirect to auth, got %s", location)
		}
		if !strings.Contains(location, "return_host=") {
			t.Errorf("Expected return_host in redirect URL, got %s", location)
		}
	})

	// Test 2: Magic URL with valid secret should set cookie and redirect
	t.Run("valid_magic_secret_sets_cookie", func(t *testing.T) {
		// Create a magic secret
		secret, err := server.createMagicSecret("test-user-id", "testbox", "/original-path")
		if err != nil {
			t.Fatalf("Failed to create magic secret: %v", err)
		}

		// Request magic URL
		req := createTestRequestForServer("GET", "http://testbox.localhost/__exe.dev/auth?secret="+secret+"&redirect=/custom-redirect", "testbox.localhost", server)
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
		req := createTestRequestForServer("GET", "http://testbox.localhost/__exe.dev/auth?secret=invalid-secret", "testbox.localhost", server)
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		if w.Code != 401 { // StatusUnauthorized
			t.Errorf("Expected status 401, got %d", w.Code)
		}
	})

	// Test 4: Magic URL without secret should return error
	t.Run("missing_secret_returns_error", func(t *testing.T) {
		req := createTestRequestForServer("GET", "http://testbox.localhost/__exe.dev/auth", "testbox.localhost", server)
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		if w.Code != 400 { // StatusBadRequest
			t.Errorf("Expected status 400, got %d", w.Code)
		}
	})

	// Test 5: Magic secret should be consumed (single use)
	t.Run("magic_secret_single_use", func(t *testing.T) {
		// Create a magic secret
		secret, err := server.createMagicSecret("test-user-id", "testbox", "/original-path")
		if err != nil {
			t.Fatalf("Failed to create magic secret: %v", err)
		}

		// First request should succeed
		req1 := createTestRequestForServer("GET", "http://testbox.localhost/__exe.dev/auth?secret="+secret, "testbox.localhost", server)
		w1 := httptest.NewRecorder()

		server.ServeHTTP(w1, req1)

		if w1.Code != 307 {
			t.Errorf("First request should succeed with 307, got %d", w1.Code)
		}

		// Second request should fail (secret consumed)
		req2 := createTestRequestForServer("GET", "http://testbox.localhost/__exe.dev/auth?secret="+secret, "testbox.localhost", server)
		w2 := httptest.NewRecorder()

		server.ServeHTTP(w2, req2)

		if w2.Code != 401 {
			t.Errorf("Second request should fail with 401, got %d", w2.Code)
		}
	})
}

func TestProxyLogoutFlow(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)
	server.magicSecrets = make(map[string]*MagicSecret)

	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDtest..."
	email := "test-logout@example.com"

	err := server.createUser(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	user, err := server.getUserByPublicKey(t.Context(), publicKey)
	if err != nil {
		t.Fatalf("Failed to get test user: %v", err)
	}

	alloc, err := server.getUserAlloc(t.Context(), user.UserID)
	if err != nil {
		t.Fatalf("Failed to get test user alloc: %v", err)
	}

	// Create a test box with a private route
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO boxes (alloc_id, name, status, image, container_id, created_by_user_id, routes,
							 ssh_server_identity_key, ssh_authorized_keys, ssh_client_private_key, ssh_port)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, alloc.AllocID, "testbox", "running", "test-image", "test-container-id", user.UserID, `[
			{
				"name": "default",
				"port": 80,
				"methods": "*",
				"prefix": "/",
				"policy": "private",
				"priority": 100
			}
		]`, "test-key", "test-keys", "test-client-key", 2222)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create test box: %v", err)
	}

	// Test 1: Logout without authentication should still work (redirect to root)
	t.Run("logout_without_auth", func(t *testing.T) {
		req := createTestRequestForServer("GET", "http://testbox.localhost/__exe.dev/logout", "testbox.localhost", server)
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
		secret, err := server.createMagicSecret(user.UserID, "testbox", "")
		if err != nil {
			t.Fatalf("Failed to create magic secret: %v", err)
		}

		// Use magic URL to authenticate
		req1 := createTestRequestForServer("GET", "http://testbox.localhost/__exe.dev/auth?secret="+secret, "testbox.localhost", server)
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
		err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
			return rx.QueryRow("SELECT COUNT(*) FROM auth_cookies WHERE cookie_value = ? AND user_id = ?", authCookie.Value, user.UserID).Scan(&count)
		})
		if err != nil || count != 1 {
			t.Fatal("Auth cookie should exist in database")
		}

		// Now logout
		req2 := createTestRequestForServer("GET", "http://testbox.localhost/__exe.dev/logout", "testbox.localhost", server)
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
		err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
			return rx.QueryRow("SELECT COUNT(*) FROM auth_cookies WHERE cookie_value = ? AND user_id = ?", authCookie.Value, user.UserID).Scan(&count)
		})
		if err != nil || count != 0 {
			t.Error("Auth cookie should have been deleted from database")
		}
	})

	// Test 3: Logout should only delete the specific cookie, not other cookies for the same user
	t.Run("logout_preserves_other_sessions", func(t *testing.T) {
		// Create two separate auth sessions for the same user
		secret1, err := server.createMagicSecret(user.UserID, "testbox", "")
		if err != nil {
			t.Fatalf("Failed to create magic secret 1: %v", err)
		}

		secret2, err := server.createMagicSecret(user.UserID, "testbox", "")
		if err != nil {
			t.Fatalf("Failed to create magic secret 2: %v", err)
		}

		// Auth with first secret to create first session
		req1 := createTestRequestForServer("GET", "http://testbox.localhost/__exe.dev/auth?secret="+secret1, "testbox.localhost", server)
		w1 := httptest.NewRecorder()
		server.ServeHTTP(w1, req1)

		// Auth with second secret to create second session
		req2 := createTestRequestForServer("GET", "http://testbox.localhost/__exe.dev/auth?secret="+secret2, "testbox.localhost", server)
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
		err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
			return rx.QueryRow("SELECT COUNT(*) FROM auth_cookies WHERE user_id = ?", user.UserID).Scan(&count)
		})
		if err != nil || count != 2 {
			t.Fatalf("Should have 2 auth cookies for user, got %d", count)
		}

		// Logout using only the first cookie
		req3 := createTestRequestForServer("GET", "http://testbox.localhost/__exe.dev/logout", "testbox.localhost", server)
		req3.AddCookie(cookie1) // Only send the first cookie
		w3 := httptest.NewRecorder()
		server.ServeHTTP(w3, req3)

		if w3.Code != 307 {
			t.Errorf("Logout should succeed with 307, got %d", w3.Code)
		}

		// Verify only the first cookie was deleted, second one remains
		err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
			return rx.QueryRow("SELECT COUNT(*) FROM auth_cookies WHERE user_id = ?", user.UserID).Scan(&count)
		})
		if err != nil || count != 1 {
			t.Errorf("Should have 1 auth cookie remaining for user, got %d", count)
		}

		// Verify the remaining cookie is the second one
		var remainingCookie string
		err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
			return rx.QueryRow("SELECT cookie_value FROM auth_cookies WHERE user_id = ?", user.UserID).Scan(&remainingCookie)
		})
		if err != nil {
			t.Fatal("Failed to get remaining cookie")
		}
		if remainingCookie != cookie2.Value {
			t.Errorf("Expected remaining cookie to be cookie2, but got different value")
		}

		// Verify the first cookie was deleted
		err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
			return rx.QueryRow("SELECT COUNT(*) FROM auth_cookies WHERE cookie_value = ?", cookie1.Value).Scan(&count)
		})
		if err != nil || count != 0 {
			t.Error("First cookie should have been deleted from database")
		}
	})
}

// TestIsProxyRequest tests the isProxyRequest function with comprehensive cases
func TestIsProxyRequest(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		devMode  string
		host     string
		expected bool
		comment  string
	}{
		// Box:port format cases
		{
			name:     "valid box:port format",
			devMode:  "test",
			host:     "mybox:8080",
			expected: true,
			comment:  "Should recognize box:port format for multi-port proxying",
		},
		{
			name:     "valid box:port with high port",
			devMode:  "test",
			host:     "testbox:9999",
			expected: true,
			comment:  "Should work with any valid port number",
		},
		{
			name:     "invalid box:port (bad port)",
			devMode:  "test",
			host:     "mybox:abc",
			expected: false,
			comment:  "Should reject non-numeric ports",
		},
		{
			name:     "localhost:port should not be proxy",
			devMode:  "test",
			host:     "localhost:8080",
			expected: false,
			comment:  "localhost with port is the main domain, not a proxy request",
		},
		{
			name:     "exe.dev:port should not be proxy",
			devMode:  "",
			host:     "exe.dev:443",
			expected: false,
			comment:  "exe.dev with port is the main domain, not a proxy request",
		},

		// Subdomain format cases (dev mode)
		{
			name:     "dev subdomain format",
			devMode:  "test",
			host:     "mybox.localhost",
			expected: true,
			comment:  "Should recognize *.localhost pattern in dev mode",
		},
		{
			name:     "dev subdomain with server port",
			devMode:  "test",
			host:     "mybox.localhost:8080",
			expected: true,
			comment:  "Should recognize *.localhost even with server port",
		},
		{
			name:     "localhost alone in dev mode",
			devMode:  "test",
			host:     "localhost",
			expected: false,
			comment:  "Plain localhost should not be proxy request",
		},
		{
			name:     "deep subdomain in dev mode",
			devMode:  "test",
			host:     "box.team.localhost",
			expected: true,
			comment:  "Should work with deeper subdomains",
		},

		// Subdomain format cases (production mode)
		{
			name:     "prod subdomain format",
			devMode:  "",
			host:     "mybox.exe.dev",
			expected: true,
			comment:  "Should recognize *.exe.dev pattern in production",
		},
		{
			name:     "prod subdomain with server port",
			devMode:  "",
			host:     "mybox.exe.dev:443",
			expected: true,
			comment:  "Should recognize *.exe.dev even with server port",
		},
		{
			name:     "exe.dev alone in prod mode",
			devMode:  "",
			host:     "exe.dev",
			expected: false,
			comment:  "Plain exe.dev should not be proxy request",
		},
		{
			name:     "deep subdomain in prod mode",
			devMode:  "",
			host:     "box.team.exe.dev",
			expected: true,
			comment:  "Should work with deeper subdomains in production",
		},

		// Cross-mode cases (testing flexibility)
		{
			name:     "prod domain in dev mode",
			devMode:  "test",
			host:     "mybox.exe.dev",
			expected: true,
			comment:  "Should still work with production domain in dev mode for flexibility",
		},
		{
			name:     "dev domain in prod mode",
			devMode:  "",
			host:     "mybox.localhost",
			expected: true,
			comment:  "Should still work with dev domain in production for flexibility",
		},

		// Edge cases
		{
			name:     "empty host",
			devMode:  "test",
			host:     "",
			expected: false,
			comment:  "Empty host should not be proxy request",
		},
		{
			name:     "just colon",
			devMode:  "test",
			host:     ":",
			expected: false,
			comment:  "Invalid format should be rejected",
		},
		{
			name:     "box with multiple colons",
			devMode:  "test",
			host:     "my:box:8080",
			expected: false,
			comment:  "Multiple colons should be rejected for box:port format",
		},
		{
			name:     "other domain",
			devMode:  "test",
			host:     "example.com",
			expected: false,
			comment:  "Other domains should not be proxy requests",
		},
		{
			name:     "subdomain of other domain",
			devMode:  "test",
			host:     "mybox.example.com",
			expected: false,
			comment:  "Subdomains of other domains should not be proxy requests",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create server with specified dev mode
			s := &Server{devMode: tc.devMode}

			result := s.isProxyRequest(tc.host)
			if result != tc.expected {
				t.Errorf("Expected %v for host %q (devMode=%q), got %v\nComment: %s",
					tc.expected, tc.host, tc.devMode, result, tc.comment)
			} else {
				t.Logf("✓ %s: host=%q devMode=%q -> %v", tc.comment, tc.host, tc.devMode, result)
			}
		})
	}
}

// TestIsDefaultServerPort tests the isDefaultServerPort function
func TestIsDefaultServerPort(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		serverPort int // simulated server HTTP port
		testPort   int // port to test
		expected   bool
		comment    string
	}{
		{
			name:       "port 443 is always default",
			serverPort: 8080,
			testPort:   443,
			expected:   true,
			comment:    "Port 443 (HTTPS) should always use default route",
		},
		{
			name:       "server HTTP port is default",
			serverPort: 8080,
			testPort:   8080,
			expected:   true,
			comment:    "Request to server's own HTTP port should use default route",
		},
		{
			name:       "different port is not default",
			serverPort: 8080,
			testPort:   9000,
			expected:   false,
			comment:    "Different port should use multi-port routing",
		},
		{
			name:       "port 80 not default when server on 8080",
			serverPort: 8080,
			testPort:   80,
			expected:   false,
			comment:    "Port 80 should not be default when server runs on different port",
		},
		{
			name:       "port 80 is default when server on 80",
			serverPort: 80,
			testPort:   80,
			expected:   true,
			comment:    "Port 80 should be default when server runs on port 80",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a mock TCP listener to simulate the server port
			mockTCP := &net.TCPAddr{Port: tc.serverPort}
			mockListener := &listener{tcp: mockTCP}
			s := &Server{httpLn: mockListener}

			result := s.isDefaultServerPort(tc.testPort)
			if result != tc.expected {
				t.Errorf("Expected %v for port %d (server on %d), got %v\nComment: %s",
					tc.expected, tc.testPort, tc.serverPort, result, tc.comment)
			} else {
				t.Logf("✓ %s: port=%d serverPort=%d -> %v", tc.comment, tc.testPort, tc.serverPort, result)
			}
		})
	}

	// Test case where httpLn is nil
	t.Run("nil httpLn", func(t *testing.T) {
		s := &Server{httpLn: nil}
		// Should only return true for 443
		if !s.isDefaultServerPort(443) {
			t.Error("Expected true for port 443 even with nil httpLn")
		}
		if s.isDefaultServerPort(8080) {
			t.Error("Expected false for port 8080 with nil httpLn")
		}
	})

	// Test case where tcp is nil
	t.Run("nil tcp", func(t *testing.T) {
		s := &Server{httpLn: &listener{tcp: nil}}
		// Should only return true for 443
		if !s.isDefaultServerPort(443) {
			t.Error("Expected true for port 443 even with nil tcp")
		}
		if s.isDefaultServerPort(8080) {
			t.Error("Expected false for port 8080 with nil tcp")
		}
	})
}
