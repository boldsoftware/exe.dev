package exe

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// generateTestSSHKey creates a test SSH key pair
func generateTestSSHKey() (ssh.Signer, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return nil, err
	}

	return signer, nil
}


func TestRegistrationFlow(t *testing.T) {
	// Generate test SSH key
	signer, err := generateTestSSHKey()
	if err != nil {
		t.Fatalf("Failed to generate test SSH key: %v", err)
	}

	// Start test server on fixed ports for simplicity
	server := NewServer("127.0.0.1:18080", "", "127.0.0.1:12222")
	
	// Start servers in background
	go func() {
		if err := server.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			t.Logf("HTTP server error: %v", err)
		}
	}()
	
	go func() {
		if err := server.serveSSH(); err != nil {
			t.Logf("SSH server error: %v", err)
		}
	}()

	// Wait for servers to start
	time.Sleep(300 * time.Millisecond)
	
	defer server.Stop()

	// Test that authentication works
	fingerprint := server.getPublicKeyFingerprint(signer.PublicKey())
	permissions, err := server.authenticatePublicKey(nil, signer.PublicKey())
	if err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}
	if permissions.Extensions["registered"] != "false" {
		t.Errorf("Expected unregistered key")
	}

	// Test that we can create a registration
	token := server.generateRegistrationToken()
	reg := &Registration{
		PublicKeyFingerprint: fingerprint,
		Token:                token,
		CompleteChan:         make(chan struct{}),
		CreatedAt:            time.Now(),
	}
	
	server.registrationsMu.Lock()
	server.registrations[token] = reg
	server.registrationsMu.Unlock()

	// Test HTTP registration completion
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:18080/register?token=%s", token))
	if err != nil {
		t.Fatalf("Failed to complete registration: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// Verify key is now registered
	server.publicKeysMu.RLock()
	registered := server.publicKeys[fingerprint]
	server.publicKeysMu.RUnlock()

	if !registered {
		t.Error("Public key should be registered after completing registration")
	}

	// Test that authentication now returns registered=true
	permissions2, err := server.authenticatePublicKey(nil, signer.PublicKey())
	if err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}
	if permissions2.Extensions["registered"] != "true" {
		t.Errorf("Expected registered key, got %s", permissions2.Extensions["registered"])
	}
}

// TestSSHRegistrationIntegration tests that SSH connections work properly with the registration flow
func TestSSHRegistrationIntegration(t *testing.T) {
	// Generate test SSH key
	signer, err := generateTestSSHKey()
	if err != nil {
		t.Fatalf("Failed to generate test SSH key: %v", err)
	}

	// Pre-register the key to test normal flow
	server := NewServer("127.0.0.1:18081", "", "127.0.0.1:12223")
	fingerprint := server.getPublicKeyFingerprint(signer.PublicKey())
	
	server.publicKeysMu.Lock()
	server.publicKeys[fingerprint] = true
	server.publicKeysMu.Unlock()
	
	// Start servers
	go func() {
		if err := server.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			t.Logf("HTTP server error: %v", err)
		}
	}()
	
	go func() {
		if err := server.serveSSH(); err != nil {
			t.Logf("SSH server error: %v", err)
		}
	}()

	// Wait for servers to start
	time.Sleep(300 * time.Millisecond)
	defer server.Stop()

	// Test SSH connection with registered key
	config := &ssh.ClientConfig{
		User: "testuser",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         2 * time.Second,
	}

	client, err := ssh.Dial("tcp", "127.0.0.1:12223", config)
	if err != nil {
		t.Fatalf("Failed to connect via SSH: %v", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("Failed to create SSH session: %v", err)
	}
	defer session.Close()

	// Set up session I/O
	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatalf("Failed to get stdout pipe: %v", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatalf("Failed to get stdin pipe: %v", err)
	}

	// Start shell
	if err := session.Shell(); err != nil {
		t.Fatalf("Failed to start shell: %v", err)
	}

	// Give the server time to send the welcome message
	time.Sleep(200 * time.Millisecond)

	// Read welcome output
	output := make([]byte, 2048)
	n, err := stdout.Read(output)
	if err != nil {
		t.Fatalf("Failed to read from SSH session: %v", err)
	}

	outputStr := string(output[:n])
	t.Logf("SSH output: %s", outputStr)

	// Should see the container management console, not registration
	if !strings.Contains(outputStr, "Container Management Console") {
		t.Error("Expected Container Management Console in output")
	}

	// Should not see registration message
	if strings.Contains(outputStr, "Your SSH public key is not registered") {
		t.Error("Should not see registration message for registered user")
	}

	// Send a command
	_, err = stdin.Write([]byte("help\n"))
	if err != nil {
		t.Fatalf("Failed to send command: %v", err)
	}

	// Read command response
	time.Sleep(100 * time.Millisecond)
	output2 := make([]byte, 2048)
	n2, err := stdout.Read(output2)
	if err != nil {
		t.Logf("Note: Could not read command response (this may be normal): %v", err)
	} else {
		output2Str := string(output2[:n2])
		t.Logf("Help command output: %s", output2Str)
		
		if !strings.Contains(output2Str, "Available commands") {
			t.Error("Expected help output")
		}
	}
}

func TestPublicKeyAuthentication(t *testing.T) {
	server := NewServer("127.0.0.1:8080", "", "127.0.0.1:2222")
	
	// Generate test key
	signer, err := generateTestSSHKey()
	if err != nil {
		t.Fatalf("Failed to generate test SSH key: %v", err)
	}

	// Test authentication with unregistered key
	permissions, err := server.authenticatePublicKey(nil, signer.PublicKey())
	if err != nil {
		t.Fatalf("Authentication should not fail: %v", err)
	}
	
	if permissions.Extensions["registered"] != "false" {
		t.Errorf("Expected registered=false, got %s", permissions.Extensions["registered"])
	}
	
	fingerprint := permissions.Extensions["fingerprint"]
	if fingerprint == "" {
		t.Error("Expected non-empty fingerprint")
	}

	// Register the key
	server.publicKeysMu.Lock()
	server.publicKeys[fingerprint] = true
	server.publicKeysMu.Unlock()

	// Test authentication with registered key
	permissions, err = server.authenticatePublicKey(nil, signer.PublicKey())
	if err != nil {
		t.Fatalf("Authentication should not fail: %v", err)
	}
	
	if permissions.Extensions["registered"] != "true" {
		t.Errorf("Expected registered=true, got %s", permissions.Extensions["registered"])
	}
}

func TestRegistrationHTTPEndpoint(t *testing.T) {
	server := NewServer("127.0.0.1:0", "", "127.0.0.1:0")

	// Create a test registration
	fingerprint := "testfingerprint123"
	token := "testtoken456"
	
	reg := &Registration{
		PublicKeyFingerprint: fingerprint,
		Token:                token,
		CompleteChan:         make(chan struct{}),
		CreatedAt:            time.Now(),
	}
	
	server.registrationsMu.Lock()
	server.registrations[token] = reg
	server.registrationsMu.Unlock()

	// Test missing token
	url1, _ := url.Parse("http://example.com/register")
	req := &http.Request{URL: url1}
	w := &mockResponseWriter{}
	
	server.handleRegistrationHTTP(w, req)
	
	if w.statusCode != 400 {
		t.Errorf("Expected status 400, got %d", w.statusCode)
	}

	// Test invalid token
	url2, _ := url.Parse("http://example.com/register?token=invalid")
	req = &http.Request{URL: url2}
	w = &mockResponseWriter{}
	
	server.handleRegistrationHTTP(w, req)
	
	if w.statusCode != 404 {
		t.Errorf("Expected status 404, got %d", w.statusCode)
	}

	// Test valid token
	url3, _ := url.Parse(fmt.Sprintf("http://example.com/register?token=%s", token))
	req = &http.Request{URL: url3}
	w = &mockResponseWriter{}
	
	// Wait for completion signal in goroutine
	completed := false
	go func() {
		<-reg.CompleteChan
		completed = true
	}()
	
	server.handleRegistrationHTTP(w, req)
	
	// Wait a bit for the channel to be closed
	time.Sleep(10 * time.Millisecond)
	
	if !completed {
		t.Error("Registration completion channel should have been closed")
	}
	
	// Check that key is now registered
	server.publicKeysMu.RLock()
	registered := server.publicKeys[fingerprint]
	server.publicKeysMu.RUnlock()
	
	if !registered {
		t.Error("Public key should be registered")
	}
	
	// Check that registration was cleaned up
	server.registrationsMu.RLock()
	_, exists := server.registrations[token]
	server.registrationsMu.RUnlock()
	
	if exists {
		t.Error("Registration should have been cleaned up")
	}
}

func TestTokenGeneration(t *testing.T) {
	server := NewServer("127.0.0.1:8080", "", "127.0.0.1:2222")
	
	token1 := server.generateRegistrationToken()
	token2 := server.generateRegistrationToken()
	
	if token1 == token2 {
		t.Error("Generated tokens should be unique")
	}
	
	if len(token1) != 32 { // 16 bytes = 32 hex chars
		t.Errorf("Expected token length 32, got %d", len(token1))
	}
}

func TestFingerprintGeneration(t *testing.T) {
	server := NewServer("127.0.0.1:8080", "", "127.0.0.1:2222")
	
	// Generate two different keys
	signer1, err := generateTestSSHKey()
	if err != nil {
		t.Fatalf("Failed to generate test SSH key: %v", err)
	}
	
	signer2, err := generateTestSSHKey()
	if err != nil {
		t.Fatalf("Failed to generate test SSH key: %v", err)
	}
	
	fp1 := server.getPublicKeyFingerprint(signer1.PublicKey())
	fp2 := server.getPublicKeyFingerprint(signer2.PublicKey())
	
	if fp1 == fp2 {
		t.Error("Different keys should have different fingerprints")
	}
	
	if len(fp1) != 64 { // SHA256 = 32 bytes = 64 hex chars
		t.Errorf("Expected fingerprint length 64, got %d", len(fp1))
	}
	
	// Same key should produce same fingerprint
	fp1_again := server.getPublicKeyFingerprint(signer1.PublicKey())
	if fp1 != fp1_again {
		t.Error("Same key should produce same fingerprint")
	}
}

// Mock types for HTTP testing
type mockResponseWriter struct {
	statusCode int
	headers    http.Header
	body       strings.Builder
}

func (m *mockResponseWriter) Header() http.Header {
	if m.headers == nil {
		m.headers = make(http.Header)
	}
	return m.headers
}

func (m *mockResponseWriter) Write(data []byte) (int, error) {
	return m.body.Write(data)
}

func (m *mockResponseWriter) WriteHeader(statusCode int) {
	m.statusCode = statusCode
}

// TestBaseURL verifies that BaseURL is configured correctly based on HTTPS settings
func TestBaseURL(t *testing.T) {
	// Test with no HTTPS
	server1 := NewServer("127.0.0.1:8080", "", "127.0.0.1:2222")
	expectedHTTP := "http://localhost:8080"
	if server1.BaseURL != expectedHTTP {
		t.Errorf("Expected BaseURL %s, got %s", expectedHTTP, server1.BaseURL)
	}

	// Test with port-only address
	server2 := NewServer(":8080", "", ":2222")
	expectedHTTPPort := "http://localhost:8080"
	if server2.BaseURL != expectedHTTPPort {
		t.Errorf("Expected BaseURL %s, got %s", expectedHTTPPort, server2.BaseURL)
	}

	// Test with HTTPS configured
	server3 := NewServer("127.0.0.1:8080", ":443", "127.0.0.1:2222")
	expectedHTTPS := "https://exe.dev"
	if server3.BaseURL != expectedHTTPS {
		t.Errorf("Expected BaseURL %s, got %s", expectedHTTPS, server3.BaseURL)
	}

	// Test registration URL uses BaseURL
	server4 := NewServer(":9999", "", ":2222")
	signer, err := generateTestSSHKey()
	if err != nil {
		t.Fatalf("Failed to generate test key: %v", err)
	}
	
	fingerprint := server4.getPublicKeyFingerprint(signer.PublicKey())
	token := server4.generateRegistrationToken()
	
	reg := &Registration{
		PublicKeyFingerprint: fingerprint,
		Token:                token,
		CompleteChan:         make(chan struct{}),
		CreatedAt:            time.Now(),
	}
	
	server4.registrationsMu.Lock()
	server4.registrations[token] = reg
	server4.registrationsMu.Unlock()
	
	expectedURL := fmt.Sprintf("%s/register?token=%s", server4.BaseURL, token)
	if !strings.Contains(expectedURL, "http://localhost:9999/register") {
		t.Errorf("Expected registration URL to use BaseURL with localhost:9999, got %s", expectedURL)
	}
}