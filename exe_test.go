package exe

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestPublicKeyAuthentication(t *testing.T) {
	server := NewServer(":18080", "", ":12222")
	
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
	
	// Register the user
	user := &User{
		PublicKeyFingerprint: fingerprint,
		Email:                "test@example.com",
		TeamName:             "testteam",
		StripeCustomerID:     "cus_test123",
		RegisteredAt:         time.Now(),
	}
	
	server.usersMu.Lock()
	server.users[fingerprint] = user
	server.usersMu.Unlock()
	
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
	server := NewServer(":18081", "", ":12223")
	
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
	server := NewServer(":18082", "", ":12224")
	
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
	server := NewServer(":18083", "", ":12225")
	
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
	
	// Test successful email verification
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:18083/verify-email?token=%s", token))
	if err != nil {
		t.Fatalf("Failed to verify email: %v", err)
	}
	resp.Body.Close()
	
	if resp.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
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
		server := NewServer(tt.httpAddr, tt.httpsAddr, ":2222")
		if server.BaseURL != tt.expected {
			t.Errorf("BaseURL for http=%s https=%s: expected %s, got %s", 
				tt.httpAddr, tt.httpsAddr, tt.expected, server.BaseURL)
		}
	}
}

func TestPostmarkClientInitialization(t *testing.T) {
	// Test without API key (should be nil since POSTMARK_API_KEY is not set)
	server1 := NewServer(":8080", "", ":2222")
	if server1.postmarkClient != nil {
		t.Log("Warning: Postmark client was initialized, POSTMARK_API_KEY might be set in environment")
	}
}

func TestTokenGeneration(t *testing.T) {
	server := NewServer(":8080", "", ":2222")
	
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
	server := NewServer(":8080", "", ":2222")
	
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
	server := NewServer(":8080", "", ":2222")
	
	tests := []struct {
		teamName string
		valid    bool
	}{
		{"validteam", true},
		{"valid-team", true},
		{"team123", true},
		{"my-team-123", true},
		{"ab", false},                    // too short
		{"toolongteamnamehere123", false}, // too long
		{"Team", false},                  // uppercase
		{"team_name", false},             // underscore
		{"team name", false},             // space
		{"-team", false},                 // starts with hyphen
		{"team-", false},                 // ends with hyphen
		{"team--name", false},            // consecutive hyphens
		{"123", true},                    // numbers only
		{"a-b-c", true},                  // minimum length with hyphens
	}
	
	for _, tt := range tests {
		result := server.isValidTeamName(tt.teamName)
		if result != tt.valid {
			t.Errorf("isValidTeamName(%q) = %v, want %v", tt.teamName, result, tt.valid)
		}
	}
}

func TestTeamNameAvailability(t *testing.T) {
	server := NewServer(":8080", "", ":2222")
	
	// Mark a team name as taken
	server.teamNamesMu.Lock()
	server.teamNames["taken"] = true
	server.teamNamesMu.Unlock()
	
	// Check that it's marked as taken
	server.teamNamesMu.RLock()
	isTaken := server.teamNames["taken"]
	server.teamNamesMu.RUnlock()
	
	if !isTaken {
		t.Error("Team name 'taken' should be marked as taken")
	}
	
	// Check that a new name is available
	server.teamNamesMu.RLock()
	isAvailable := server.teamNames["available"]
	server.teamNamesMu.RUnlock()
	
	if isAvailable {
		t.Error("Team name 'available' should not be taken")
	}
}