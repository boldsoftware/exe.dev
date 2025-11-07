package execore

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
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

func TestProxyLogoutFlow(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.magicSecrets = make(map[string]*MagicSecret)

	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDtest..."
	email := "test-logout@example.com"

	_, err := server.createUser(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	user, err := server.getUserByPublicKey(t.Context(), publicKey)
	if err != nil {
		t.Fatalf("Failed to get test user: %v", err)
	}

	// Create a test box with a private route
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO boxes (ctrhost, name, status, image, container_id, created_by_user_id, routes,
							 ssh_server_identity_key, ssh_authorized_keys, ssh_client_private_key, ssh_port)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, "fake_ctrhost", "testbox", "running", "test-image", "test-container-id", user.UserID, `[
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

	// Test 1: Logout GET without authentication should show confirmation form
	t.Run("logout_get_without_auth", func(t *testing.T) {
		req := createTestRequestForServer("GET", "http://testbox.localhost/__exe.dev/logout", "testbox.localhost", server)
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		body := w.Body.String()
		if !strings.Contains(body, "Are you sure you want to log out?") {
			t.Error("Expected logout confirmation form")
		}
		if !strings.Contains(body, `<form method="POST"`) {
			t.Error("Expected POST form in confirmation page")
		}
	})

	// Test 1b: Logout POST without authentication should still work (redirect to logged-out page)
	t.Run("logout_post_without_auth", func(t *testing.T) {
		req := createTestRequestForServer("POST", "http://testbox.localhost/__exe.dev/logout", "testbox.localhost", server)
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		if w.Code != 307 {
			t.Errorf("Expected status 307, got %d", w.Code)
		}

		// Check redirect to logged-out page on main domain
		location := w.Header().Get("Location")
		if !strings.HasSuffix(location, "/logged-out") {
			t.Errorf("Expected redirect to /logged-out, got '%s'", location)
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

		if w1.Code != http.StatusSeeOther {
			t.Fatalf("Auth should succeed with 303 See Other, got %d", w1.Code)
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

		// Now logout (use POST)
		req2 := createTestRequestForServer("POST", "http://testbox.localhost/__exe.dev/logout", "testbox.localhost", server)
		req2.AddCookie(authCookie) // Send the auth cookie
		w2 := httptest.NewRecorder()

		server.ServeHTTP(w2, req2)

		if w2.Code != 307 {
			t.Errorf("Expected status 307, got %d", w2.Code)
		}

		// Check redirect to logged-out page
		location := w2.Header().Get("Location")
		if !strings.HasSuffix(location, "/logged-out") {
			t.Errorf("Expected redirect to /logged-out, got '%s'", location)
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

		if w1.Code != http.StatusSeeOther || w2.Code != http.StatusSeeOther {
			t.Fatalf("Both authentications should succeed with 303, got %d and %d", w1.Code, w2.Code)
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

		// Logout using only the first cookie (use POST)
		req3 := createTestRequestForServer("POST", "http://testbox.localhost/__exe.dev/logout", "testbox.localhost", server)
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
			expected: true,
			comment:  "Other domains should be proxy requests",
		},
		{
			name:     "subdomain of other domain",
			devMode:  "test",
			host:     "mybox.example.com",
			expected: true,
			comment:  "Subdomains of other domains should be proxy requests",
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

// TestProxyStreaming tests that the proxy doesn't buffer streaming responses
func TestProxyStreaming(t *testing.T) {
	t.Parallel()

	// This test verifies that FlushInterval is set on the reverse proxy
	// to avoid buffering responses. This is critical for:
	// - Server-Sent Events (SSE)
	// - Streaming responses
	// - WebSocket upgrades
	// - Any real-time data transfer

	// Create a mock streaming backend that sends data in chunks
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Enable flushing
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter doesn't support flushing")
		}

		// Send headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Send data in chunks with delays
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: chunk %d\n\n", i)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer backend.Close()

	// Parse backend URL
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}

	// Create a reverse proxy similar to what proxyViaSSHPortForward does
	proxy := httputil.NewSingleHostReverseProxy(backendURL)
	// This is the critical setting - FlushInterval = -1 means flush immediately
	proxy.FlushInterval = -1

	// Create test server with the proxy
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	// Make request to proxy
	req, err := http.NewRequest("GET", proxyServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Read the response body to verify streaming works
	// If buffering were enabled, we'd get all chunks at once after the delays
	// With flushing, we get them as they're sent
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	bodyStr := string(body)

	// Verify we got all chunks
	for i := 0; i < 3; i++ {
		expected := fmt.Sprintf("data: chunk %d\n\n", i)
		if !strings.Contains(bodyStr, expected) {
			t.Errorf("Expected to find %q in response, got: %q", expected, bodyStr)
		}
	}

	// Verify content type
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Expected Content-Type 'text/event-stream', got %q", ct)
	}
}

func TestSetForwardedHeaders(t *testing.T) {
	t.Parallel()

	t.Run("https request populates headers", func(t *testing.T) {
		incoming := httptest.NewRequest(http.MethodGet, "https://box.exe.dev/", nil)
		incoming.Host = "box.exe.dev"
		incoming.RemoteAddr = "203.0.113.5:45678"
		incoming.TLS = &tls.ConnectionState{}

		outgoing := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/", nil)

		setForwardedHeaders(outgoing, incoming)

		if got := outgoing.Header.Get("X-Forwarded-Proto"); got != "https" {
			t.Fatalf("expected proto https, got %q", got)
		}
		if got := outgoing.Header.Get("X-Forwarded-Host"); got != "box.exe.dev" {
			t.Fatalf("expected forwarded host box.exe.dev, got %q", got)
		}
		if got := outgoing.Header.Get("X-Forwarded-For"); got != "203.0.113.5" {
			t.Fatalf("expected forwarded for 203.0.113.5, got %q", got)
		}
	})

	t.Run("appends existing xff and preserves host port", func(t *testing.T) {
		incoming := httptest.NewRequest(http.MethodGet, "http://app.exe.dev/resource", nil)
		incoming.Host = "app.exe.dev:8443"
		incoming.RemoteAddr = "198.51.100.7:4444"
		incoming.Header.Set("X-Forwarded-For", "10.0.0.1")

		outgoing := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5000/", nil)

		setForwardedHeaders(outgoing, incoming)

		if got := outgoing.Header.Get("X-Forwarded-Proto"); got != "http" {
			t.Fatalf("expected proto http, got %q", got)
		}
		if got := outgoing.Header.Get("X-Forwarded-Host"); got != "app.exe.dev:8443" {
			t.Fatalf("expected forwarded host app.exe.dev:8443, got %q", got)
		}
		if got := outgoing.Header.Get("X-Forwarded-For"); got != "10.0.0.1, 198.51.100.7" {
			t.Fatalf("expected forwarded for '10.0.0.1, 198.51.100.7', got %q", got)
		}
	})
}

func TestAuthConfirmSkipForOwner(t *testing.T) {
	t.Parallel()

	boxReturnHost := func(s *Server, boxName string) string {
		host := fmt.Sprintf("%s.%s", boxName, s.getMainDomain())
		if s.httpLn != nil && s.httpLn.tcp != nil && s.httpLn.tcp.Port != 80 {
			host = fmt.Sprintf("%s:%d", host, s.httpLn.tcp.Port)
		}
		return host
	}

	doAuthConfirm := func(t *testing.T, s *Server, secret, returnHost string) *httptest.ResponseRecorder {
		t.Helper()
		params := url.Values{
			"secret":      {secret},
			"return_host": {returnHost},
		}
		authURL := fmt.Sprintf("http://%s/auth/confirm?%s", s.getMainDomainWithPort(), params.Encode())

		req := createTestRequestForServer(http.MethodGet, authURL, s.getMainDomainWithPort(), s)
		w := httptest.NewRecorder()

		s.ServeHTTP(w, req)
		return w
	}

	createUser := func(t *testing.T, s *Server, email string) string {
		t.Helper()
		keySuffix := strings.ReplaceAll(email, "@", "_")
		publicKey := fmt.Sprintf("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI%s_%s", keySuffix, t.Name())
		user, err := s.createUser(t.Context(), publicKey, email)
		if err != nil {
			t.Fatalf("createUser(%q): %v", email, err)
		}
		return user.UserID
	}

	createSecret := func(t *testing.T, s *Server, userID, boxName, redirect string) string {
		t.Helper()
		secret, err := s.createMagicSecret(userID, boxName, redirect)
		if err != nil {
			t.Fatalf("createMagicSecret: %v", err)
		}
		return secret
	}

	const redirectPath = "/after-login"

	t.Run("owner_skips_confirmation", func(t *testing.T) {
		t.Parallel()

		server := newTestServer(t)
		userID := createUser(t, server, "owner@example.com")

		const boxName = "ownedbox"
		server.createTestBox(t, userID, "fake_ctrhost", boxName, "container-owner", "busybox:latest")

		secret := createSecret(t, server, userID, boxName, redirectPath)
		returnHost := boxReturnHost(server, boxName)

		resp := doAuthConfirm(t, server, secret, returnHost)
		if resp.Code != http.StatusTemporaryRedirect {
			t.Fatalf("expected redirect for owner, got %d", resp.Code)
		}

		loc, err := resp.Result().Location()
		if err != nil {
			t.Fatalf("failed to get Location header: %v", err)
		}
		if loc.Scheme != "http" {
			t.Errorf("expected http scheme, got %q", loc.Scheme)
		}
		if loc.Host != returnHost {
			t.Errorf("expected redirect host %q, got %q", returnHost, loc.Host)
		}
		if loc.Path != "/__exe.dev/auth" {
			t.Errorf("expected redirect path /__exe.dev/auth, got %q", loc.Path)
		}

		q := loc.Query()
		if q.Get("secret") != secret {
			t.Errorf("expected secret %q, got %q", secret, q.Get("secret"))
		}
		if q.Get("redirect") != redirectPath {
			t.Errorf("expected redirect path %q, got %q", redirectPath, q.Get("redirect"))
		}
	})

	t.Run("non_owner_sees_confirmation", func(t *testing.T) {
		t.Parallel()

		server := newTestServer(t)
		ownerID := createUser(t, server, "owner@example.com")
		guestID := createUser(t, server, "guest@example.com")

		const boxName = "sharedbox"
		defaultRoute := exedb.DefaultRouteJSON()
		err := server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO boxes (ctrhost, name, status, image, container_id, created_by_user_id, routes,
				                   ssh_server_identity_key, ssh_authorized_keys, ssh_client_private_key, ssh_port)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, "fake_ctrhost", boxName, "running", "busybox:latest", "container-shared", ownerID, defaultRoute,
				"test-key", "test-keys", "test-client-key", 2222)
			return err
		})
		if err != nil {
			t.Fatalf("failed to insert shared box: %v", err)
		}

		// Ensure the guest user exists with their secrets can refer to them.
		if guestID == "" {
			t.Fatal("guest ID should not be empty")
		}

		secret := createSecret(t, server, guestID, boxName, redirectPath)
		returnHost := boxReturnHost(server, boxName)

		resp := doAuthConfirm(t, server, secret, returnHost)
		body := resp.Body.String()

		if resp.Code != http.StatusOK {
			t.Fatalf("expected confirmation page (200), got %d. Body: %s", resp.Code, body)
		}

		if !strings.Contains(body, "Confirm Login") {
			t.Errorf("expected confirmation heading, got %q", body)
		}
		if !strings.Contains(body, "guest@example.com") {
			t.Errorf("expected guest email on confirmation page, got %q", body)
		}
		if !strings.Contains(body, boxName) {
			t.Errorf("expected box name on confirmation page, got %q", body)
		}
	})

	t.Run("magic_auth_redirects_with_303", func(t *testing.T) {
		t.Parallel()

		server := newTestServer(t)
		userID := createUser(t, server, "user@example.com")

		const boxName = "testbox"
		server.createTestBox(t, userID, "fake_ctrhost", boxName, "container-test", "busybox:latest")

		secret := createSecret(t, server, userID, boxName, "/test-path")
		returnHost := boxReturnHost(server, boxName)

		// Make request to magic auth URL
		req := createTestRequestForServer("GET", "http://"+returnHost+"/__exe.dev/auth?secret="+secret+"&redirect=/test-path", returnHost, server)
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		if w.Code != http.StatusSeeOther {
			t.Errorf("expected StatusSeeOther (303) for magic auth redirect, got %d", w.Code)
		}

		loc, err := w.Result().Location()
		if err != nil {
			t.Fatalf("failed to get Location header: %v", err)
		}
		if loc.Path != "/test-path" {
			t.Errorf("expected redirect to /test-path, got %q", loc.Path)
		}
	})
}
