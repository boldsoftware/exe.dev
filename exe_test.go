package exe

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"exe.dev/container"
	"exe.dev/sqlite"
	"golang.org/x/crypto/ssh"
)

func TestPublicKeyAuthentication(t *testing.T) {
	server := NewTestServer(t)

	// Generate a test key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate RSA key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}

	// Test authentication with unregistered key
	permissions, err := server.AuthenticatePublicKey(nil, signer.PublicKey())
	if err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}

	if permissions.Extensions["registered"] != "false" {
		t.Error("Unregistered key should have registered=false")
	}

	// Fingerprints have been eliminated - no longer included in permissions

	// Register the user with alloc in the database
	publicKeyStr := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	if _, err := server.createUser(t.Context(), publicKeyStr, "test@example.com"); err != nil {
		t.Fatalf("Failed to create user with alloc: %v", err)
	}

	// Test authentication with registered key
	permissions2, err := server.AuthenticatePublicKey(nil, signer.PublicKey())
	if err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}

	if permissions2.Extensions["registered"] != "true" {
		t.Error("Registered key should have registered=true")
	}

	if permissions2.Extensions["email"] != "test@example.com" {
		t.Error("Registered user should have email in extensions")
	}
}

func TestEmailVerificationHTTP(t *testing.T) {
	server := NewTestServer(t)
	verification := server.addEmailVerification("ssh-rsa test-key", "test@example.com", true)

	// Test GET request shows form
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/verify-email?token=%s", server.httpLn.tcp.Port, verification.Token))
	if err != nil {
		t.Fatalf("Failed to GET verify-email: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("GET: Expected status 200, got %d", resp.StatusCode)
	}

	// Test POST request completes verification
	form := url.Values{}
	form.Add("token", verification.Token)
	resp, err = http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/verify-email", server.httpLn.tcp.Port),
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatalf("Failed to POST verify-email: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("POST: Expected status 200, got %d", resp.StatusCode)
	}

	// Test invalid token
	resp2, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/verify-email?token=invalid", server.httpLn.tcp.Port))
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp2.Body.Close()

	if resp2.StatusCode != 404 {
		t.Errorf("Expected status 404 for invalid token, got %d", resp2.StatusCode)
	}

	// Test missing token
	resp3, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/verify-email", server.httpLn.tcp.Port))
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp3.Body.Close()

	if resp3.StatusCode != 400 {
		t.Errorf("Expected status 400 for missing token, got %d", resp3.StatusCode)
	}
}

func TestPostmarkClientInitialization(t *testing.T) {
	// Test without API key (should be nil since POSTMARK_API_KEY is not set)
	server1 := NewTestServer(t)
	if server1.postmarkClient != nil {
		t.Log("Warning: Postmark client was initialized, POSTMARK_API_KEY might be set in environment")
	}
}

func TestTokenGeneration(t *testing.T) {
	server := NewTestServer(t)

	token1 := server.generateRegistrationToken()
	token2 := server.generateRegistrationToken()

	if token1 == token2 {
		t.Error("Generated tokens should be unique")
	}

	if len(token1) == 0 {
		t.Error("Token should not be empty")
	}
}

func TestEmailValidation(t *testing.T) {
	server := NewTestServer(t)

	tests := []struct {
		email string
		valid bool
	}{
		{"test@example.com", true},
		{"user@domain.co.uk", true},
		{"", false},
		{"invalid", false},
		{"@example.com", false},
		{"test@", false},
		{"test@domain", false},
	}

	for _, tt := range tests {
		result := server.isValidEmail(tt.email)
		if result != tt.valid {
			t.Errorf("isValidEmail(%q) = %v, want %v", tt.email, result, tt.valid)
		}
	}
}

// TestEmailVerificationRequiresPOST tests that email verification requires POST confirmation
func TestEmailVerificationRequiresPOST(t *testing.T) {
	// Create server
	server := NewTestServer(t)

	// Create a test user
	email := "test@example.com"
	// Create user with generated user_id
	publicKey := "ssh-rsa dummy-test-key test@example.com"
	_, err := server.createUser(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user : %v", err)
	}

	user, err := server.GetUserByEmail(t.Context(), email)
	if err != nil {
		t.Fatalf("Failed to get user by email: %v", err)
	}

	// Create an email verification token
	token := "test-token-" + time.Now().Format("20060102150405")
	expires := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	verificationCode := "112233"
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO email_verifications (token, email, user_id, expires_at, verification_code)
			VALUES (?, ?, ?, ?, ?)`,
			token, email, user.UserID, expires, verificationCode)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create verification token: %v", err)
	}

	// Test 1: GET request should show form, not complete verification
	req := httptest.NewRequest("GET", "/verify-email?token="+token, nil)
	w := httptest.NewRecorder()
	server.handleEmailVerificationHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET request failed: got status %d, want %d", w.Code, http.StatusOK)
	}

	// Verify token is still valid (not consumed by GET)
	var count int
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT COUNT(*) FROM email_verifications WHERE token = ?`, token).Scan(&count)
	})
	if err != nil {
		t.Errorf("Error checking token after GET: %v", err)
	}
	if count != 1 {
		t.Errorf("GET request should not consume the verification token, count = %d", count)
	}

	// Test 2: POST request should complete verification
	form := url.Values{}
	form.Add("token", token)
	req = httptest.NewRequest("POST", "/verify-email", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	server.handleEmailVerificationHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST request failed: got status %d, want %d", w.Code, http.StatusOK)
	}

	// Verify token is consumed
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT COUNT(*) FROM email_verifications WHERE token = ?`, token).Scan(&count)
	})
	if err != nil || count != 0 {
		t.Error("POST request should consume the verification token")
	}

	// Test 3: Invalid token should show error
	req = httptest.NewRequest("GET", "/verify-email?token=invalid", nil)
	w = httptest.NewRecorder()
	server.handleEmailVerificationHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Invalid token should return 404: got status %d", w.Code)
	}
}

// TestMetricsEndpoint tests that the /metrics endpoint returns Prometheus metrics
func TestMetricsEndpoint(t *testing.T) {
	server := NewTestServer(t)

	// Use httptest.Server for testing
	testServer := httptest.NewServer(server)
	defer testServer.Close()

	// Make a request to the health endpoint first to trigger HTTP metrics
	healthResp, err := http.Get(testServer.URL + "/health")
	if err != nil {
		t.Fatalf("Failed to make health request: %v", err)
	}
	healthResp.Body.Close()

	// Make request to metrics endpoint
	resp, err := http.Get(testServer.URL + "/metrics")
	if err != nil {
		t.Fatalf("Failed to fetch metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	bodyStr := string(body)

	// Debug: print the actual response
	t.Logf("Metrics response body: %s", bodyStr)

	// Check for standard promhttp metrics
	expectedMetrics := []string{
		"promhttp_metric_handler_requests_total",
		"ssh_connections_current", // This should always be present as a gauge
	}

	for _, metric := range expectedMetrics {
		if !strings.Contains(bodyStr, metric) {
			t.Errorf("Expected to find metric %s in response", metric)
		}
	}

	// Verify the response is in Prometheus format
	if !strings.Contains(bodyStr, "# HELP") {
		t.Error("Expected Prometheus format with HELP comments")
	}
	if !strings.Contains(bodyStr, "# TYPE") {
		t.Error("Expected Prometheus format with TYPE comments")
	}
}

// TestHTTPMetricsInstrumentation tests that HTTP requests are being instrumented
func TestHTTPMetricsInstrumentation(t *testing.T) {
	server := NewTestServer(t)

	// Use httptest.Server for testing
	testServer := httptest.NewServer(server)
	defer testServer.Close()

	// Make a request to the health endpoint
	resp, err := http.Get(testServer.URL + "/health")
	if err != nil {
		t.Fatalf("Failed to make health check request: %v", err)
	}
	resp.Body.Close()

	// Now fetch metrics to see if the request was recorded
	resp, err = http.Get(testServer.URL + "/metrics")
	if err != nil {
		t.Fatalf("Failed to fetch metrics: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read metrics response: %v", err)
	}

	bodyStr := string(body)

	// Check that we have standard promhttp metrics
	if !strings.Contains(bodyStr, "promhttp_metric_handler_requests_total") {
		t.Error("Expected to find promhttp_metric_handler_requests_total metric")
	}
}

// createTestBox is a test helper that generates SSH keys and stores box info in database
func (s *Server) createTestBox(t *testing.T, userID, allocID, name, containerID, image string) {
	// Generate SSH keys for testing
	sshKeys, err := container.GenerateContainerSSHKeys()
	if err != nil {
		t.Fatalf("failed to generate SSH keys: %v", err)
	}

	id, err := s.preCreateBox(t.Context(), userID, allocID, name, image)
	if err != nil {
		t.Fatalf("failed to create box with test SSH keys: %v", err)
	}

	err = s.updateBoxWithContainer(t.Context(), id, containerID, "root", sshKeys, s.piperdPort)
	if err != nil {
		t.Fatalf("failed to update box with container ID: %v", err)
	}
}

func TestSSHIdentityKeyForBox(t *testing.T) {
	server := NewTestServer(t)

	// Create a test user and alloc
	publicKeyStr := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDummy-test-key test@example.com"
	if _, err := server.createUser(t.Context(), publicKeyStr, "test@example.com"); err != nil {
		t.Fatalf("Failed to create user with alloc: %v", err)
	}

	// Get the user to find their alloc
	var userID, allocID string
	err := server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT u.user_id, a.alloc_id FROM users u JOIN allocs a ON u.user_id = a.user_id WHERE u.email = ?`, "test@example.com").Scan(&userID, &allocID)
	})
	if err != nil {
		t.Fatalf("Failed to get user and alloc: %v", err)
	}

	boxName := "test-box"
	containerID := "container-123"
	image := "ubuntu:latest"

	t.Run("box exists and has SSH keys", func(t *testing.T) {
		server.createTestBox(t, userID, allocID, boxName, containerID, image)

		// Test successful retrieval
		publicKey, err := server.SSHIdentityKeyForBox(t.Context(), boxName)
		if err != nil {
			t.Fatalf("SSHIdentityKeyForBox failed: %v", err)
		}

		if publicKey == nil {
			t.Fatal("Expected non-nil public key")
		}

		// Verify the public key format
		if publicKey.Type() != "ssh-ed25519" {
			t.Errorf("Expected public key type to be 'ssh-ed25519', got: %q", publicKey.Type())
		}
	})

	t.Run("box does not exist", func(t *testing.T) {
		_, err = server.SSHIdentityKeyForBox(t.Context(), "nonexistent-box")
		if err == nil {
			t.Error("Expected error for nonexistent box")
		}
		if !strings.Contains(err.Error(), "failed to find box nonexistent-box") {
			t.Errorf("Expected 'failed to find box' error, got: %v", err)
		}
	})

	t.Run("box exists but has no SSH key", func(t *testing.T) {
		boxNameNoSSH := "box-no-ssh"
		id, err := server.preCreateBox(t.Context(), userID, allocID, boxNameNoSSH, image)
		if err != nil {
			t.Fatalf("Failed to create box without SSH: %v", err)
		}

		// Update box with container but no SSH keys
		err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`UPDATE boxes SET container_id = ? WHERE id = ?`, containerID+"-no-ssh", id)
			return err
		})
		if err != nil {
			t.Fatalf("Failed to update box container ID: %v", err)
		}

		// Should fail because no SSH server identity key
		_, err = server.SSHIdentityKeyForBox(t.Context(), boxNameNoSSH)
		if err == nil {
			t.Error("Expected error for box without SSH key")
		}
		if !strings.Contains(err.Error(), "has no SSH server identity key") {
			t.Errorf("Expected 'has no SSH server identity key' error, got: %v", err)
		}
	})
}

// TestMetricsEndpointProtection tests that /metrics is protected by IP restrictions
func TestMetricsEndpointProtection(t *testing.T) {
	// Test the requireLocalAccess decorator directly

	// Create a simple test handler
	testHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}

	// Test request from non-localhost, non-Tailscale IP should be denied
	t.Run("external_ip_denied", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.100:12345" // Simulate external IP
		w := httptest.NewRecorder()

		requireLocalAccess(testHandler)(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("Expected status 401 for external IP, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "Access denied") {
			t.Errorf("Expected 'Access denied' in response body, got: %s", body)
		}
	})

	// Test request from localhost should be allowed
	t.Run("localhost_allowed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "127.0.0.1:12345" // Localhost IP
		w := httptest.NewRecorder()

		requireLocalAccess(testHandler)(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200 for localhost, got %d", w.Code)
		}
		body := w.Body.String()
		if body != "success" {
			t.Errorf("Expected 'success' in response body, got: %s", body)
		}
	})

	// Test request from IPv6 localhost should be allowed
	t.Run("localhost_ipv6_allowed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "[::1]:12345" // IPv6 localhost
		w := httptest.NewRecorder()

		requireLocalAccess(testHandler)(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200 for IPv6 localhost, got %d", w.Code)
		}
		body := w.Body.String()
		if body != "success" {
			t.Errorf("Expected 'success' in response body, got: %s", body)
		}
	})

	// Test request from Tailscale IP should be allowed
	t.Run("tailscale_ip_allowed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "100.64.1.1:12345" // Tailscale IP range
		w := httptest.NewRecorder()

		requireLocalAccess(testHandler)(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200 for Tailscale IP, got %d", w.Code)
		}
		body := w.Body.String()
		if body != "success" {
			t.Errorf("Expected 'success' in response body, got: %s", body)
		}
	})

	// Test malformed RemoteAddr
	t.Run("malformed_remote_addr", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "invalid-ip" // Malformed IP
		w := httptest.NewRecorder()

		requireLocalAccess(testHandler)(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Errorf("Expected status 500 for malformed IP, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "remoteaddr check") {
			t.Errorf("Expected 'remoteaddr check' error in response body, got: %s", body)
		}
	})
}
