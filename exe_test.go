package exe

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestPublicKeyAuthentication(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Generate a test key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate RSA key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}

	fingerprint := server.getPublicKeyFingerprint(signer.PublicKey())

	// Test authentication with unregistered key
	permissions, err := server.authenticatePublicKey(nil, signer.PublicKey())
	if err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}

	if permissions.Extensions["registered"] != "false" {
		t.Error("Unregistered key should have registered=false")
	}

	if permissions.Extensions["fingerprint"] != fingerprint {
		t.Errorf("Expected fingerprint %s, got %s", fingerprint, permissions.Extensions["fingerprint"])
	}

	// Register the user and team in the database
	if err := server.createUser(fingerprint, "test@example.com"); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam("testteam", "test@example.com"); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(fingerprint, "testteam", true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	// Test authentication with registered key
	permissions2, err := server.authenticatePublicKey(nil, signer.PublicKey())
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

func TestServerStartStop(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18081", "", ":12223", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Start server in a goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start()
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Test that server is responding
	resp, err := http.Get("http://127.0.0.1:18081/health")
	if err != nil {
		t.Fatalf("Health check failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// Stop server
	if err := server.Stop(); err != nil {
		t.Errorf("Failed to stop server: %v", err)
	}

	// The Start() method blocks on OS signals, so we can't easily test
	// the complete shutdown flow. Just verify the server components stopped.
	t.Log("Server Stop() called successfully")
}

func TestHealthEndpoint(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18082", "", ":12224", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Start server
	go server.Start()
	defer server.Stop()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:18082/health")
	if err != nil {
		t.Fatalf("Health check failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
}

func TestEmailVerificationHTTP(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18083", "", ":12225", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Start server
	go server.Start()
	defer server.Stop()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Create a test email verification
	token := server.generateRegistrationToken()
	verification := &EmailVerification{
		PublicKeyFingerprint: "test-fingerprint",
		Email:                "test@example.com",
		Token:                token,
		CompleteChan:         make(chan struct{}),
		CreatedAt:            time.Now(),
	}

	server.emailVerificationsMu.Lock()
	server.emailVerifications[token] = verification
	server.emailVerificationsMu.Unlock()

	// Test GET request shows form
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:18083/verify-email?token=%s", token))
	if err != nil {
		t.Fatalf("Failed to GET verify-email: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("GET: Expected status 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Confirm Your Email Address") {
		t.Error("GET should show confirmation form")
	}

	// Test POST request completes verification
	form := url.Values{}
	form.Add("token", token)
	resp, err = http.Post(
		"http://127.0.0.1:18083/verify-email",
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatalf("Failed to POST verify-email: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("POST: Expected status 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Email Verified!") {
		t.Error("POST should show success message")
	}

	// Test invalid token
	resp2, err := http.Get("http://127.0.0.1:18083/verify-email?token=invalid")
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp2.Body.Close()

	if resp2.StatusCode != 404 {
		t.Errorf("Expected status 404 for invalid token, got %d", resp2.StatusCode)
	}

	// Test missing token
	resp3, err := http.Get("http://127.0.0.1:18083/verify-email")
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp3.Body.Close()

	if resp3.StatusCode != 400 {
		t.Errorf("Expected status 400 for missing token, got %d", resp3.StatusCode)
	}
}

func TestBaseURLGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping test that requires Docker in short mode")
	}
	
	tests := []struct {
		httpAddr  string
		httpsAddr string
		expected  string
	}{
		{":8080", "", "http://localhost:8080"},
		{":80", "", "http://localhost:80"},
		{"localhost:8080", "", "http://localhost:8080"},
		{"0.0.0.0:8080", "", "http://localhost:8080"},
		{":8080", ":443", "https://exe.dev"},
	}

	for _, tt := range tests {
		// Create temporary database file
		tmpDB, err := os.CreateTemp("", "test_*.db")
		if err != nil {
			t.Fatalf("Failed to create temp db: %v", err)
		}
		defer os.Remove(tmpDB.Name())
		tmpDB.Close()

		server, err := NewServer(tt.httpAddr, tt.httpsAddr, ":2222", tmpDB.Name(), "local", []string{""})
		if err != nil {
			t.Fatalf("Failed to create server: %v", err)
		}
		defer server.Stop()
		if server.BaseURL != tt.expected {
			t.Errorf("BaseURL for http=%s https=%s: expected %s, got %s",
				tt.httpAddr, tt.httpsAddr, tt.expected, server.BaseURL)
		}
	}
}

func TestPostmarkClientInitialization(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Test without API key (should be nil since POSTMARK_API_KEY is not set)
	server1, err := NewServer(":8080", "", ":2222", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server1.Stop()
	if server1.postmarkClient != nil {
		t.Log("Warning: Postmark client was initialized, POSTMARK_API_KEY might be set in environment")
	}
}

func TestTokenGeneration(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":8080", "", ":2222", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

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
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":8080", "", ":2222", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

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

func TestTeamNameValidation(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":8080", "", ":2222", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	tests := []struct {
		teamName string
		valid    bool
	}{
		{"validteam", true},
		{"valid-team", true},
		{"team123", true},
		{"my-team-123", true},
		{"ab", false},                     // too short
		{"toolongteamnamehere123", false}, // too long
		{"Team", false},                   // uppercase
		{"team_name", false},              // underscore
		{"team name", false},              // space
		{"-team", false},                  // starts with hyphen
		{"team-", false},                  // ends with hyphen
		{"team--name", false},             // consecutive hyphens
		{"123", true},                     // numbers only
		{"a-b-c", true},                   // minimum length with hyphens
	}

	for _, tt := range tests {
		result := server.isValidTeamName(tt.teamName)
		if result != tt.valid {
			t.Errorf("isValidTeamName(%q) = %v, want %v", tt.teamName, result, tt.valid)
		}
	}
}

func TestTeamNameAvailability(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":8080", "", ":2222", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create a team to mark the name as taken
	if err := server.createTeam("taken", "test@example.com"); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}

	// Check that it's marked as taken
	isTaken, err := server.isTeamNameTaken("taken")
	if err != nil {
		t.Fatalf("Failed to check if team name is taken: %v", err)
	}
	if !isTaken {
		t.Error("Team name 'taken' should be marked as taken")
	}

	// Check that a new name is available
	isAvailable, err := server.isTeamNameTaken("available")
	if err != nil {
		t.Fatalf("Failed to check if team name is taken: %v", err)
	}
	if isAvailable {
		t.Error("Team name 'available' should not be taken")
	}
}

// TestEmailVerificationRequiresPOST tests that email verification requires POST confirmation
func TestEmailVerificationRequiresPOST(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":0", "", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Create a test user
	fingerprint := "test-fingerprint-12345"
	email := "test@example.com"
	_, err = server.db.Exec(`INSERT INTO users (public_key_fingerprint, email) VALUES (?, ?)`, fingerprint, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create an email verification token
	token := "test-token-" + time.Now().Format("20060102150405")
	expires := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	_, err = server.db.Exec(`
		INSERT INTO email_verifications (token, email, user_fingerprint, expires_at)
		VALUES (?, ?, ?, ?)`,
		token, email, fingerprint, expires)
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

	body := w.Body.String()
	if !strings.Contains(body, "Confirm Your Email Address") {
		t.Error("GET response should show confirmation form")
	}
	if !strings.Contains(body, `<form method="POST"`) {
		t.Error("GET response should contain POST form")
	}
	if !strings.Contains(body, "Confirm Email Verification") {
		t.Error("GET response should have confirmation button")
	}

	// Verify token is still valid (not consumed by GET)
	var count int
	err = server.db.QueryRow(`SELECT COUNT(*) FROM email_verifications WHERE token = ?`, token).Scan(&count)
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

	body = w.Body.String()
	if !strings.Contains(body, "Email Verified!") {
		t.Error("POST response should show success message")
	}

	// Verify token is consumed
	err = server.db.QueryRow(`SELECT COUNT(*) FROM email_verifications WHERE token = ?`, token).Scan(&count)
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
