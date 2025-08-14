package exe

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestSSHEmailVerificationBug tests the bug where user is not found after email verification
func TestSSHEmailVerificationBug(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_verification_bug_*.db")
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
	server.testMode = true
	defer server.Stop()

	// Find a free port for SSH
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	sshAddr := listener.Addr().String()
	listener.Close()

	// Find a free port for HTTP
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port for HTTP: %v", err)
	}
	httpAddr := httpListener.Addr().String()
	httpListener.Close()
	server.httpAddr = httpAddr

	// Start HTTP server to handle email verification
	go func() {
		http.HandleFunc("/verify-email", server.handleEmailVerificationHTTP)
		if err := http.ListenAndServe(httpAddr, nil); err != nil {
			t.Logf("HTTP server error: %v", err)
		}
	}()

	// Start SSH server
	sshServer := NewSSHServer(server)
	go func() {
		if err := sshServer.Start(sshAddr); err != nil {
			t.Logf("SSH server error: %v", err)
		}
	}()

	// Wait for servers to start
	time.Sleep(100 * time.Millisecond)

	// Generate test SSH key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}

	hash := sha256.Sum256(signer.PublicKey().Marshal())
	fingerprint := hex.EncodeToString(hash[:])
	publicKeyStr := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))

	t.Logf("Test fingerprint: %s", fingerprint)

	// Simulate the registration flow
	// 1. Start email verification
	email := "test@example.com"
	token := fmt.Sprintf("%x", make([]byte, 16))
	
	// Manually set up the verification like startEmailVerificationNew does
	completeChan := make(chan struct{})
	server.emailVerificationsMu.Lock()
	server.emailVerifications[token] = &EmailVerification{
		Token:                token,
		Email:                email,
		PublicKeyFingerprint: fingerprint,
		PublicKey:            publicKeyStr,
		CompleteChan:         completeChan,
		CreatedAt:           time.Now(),
	}
	server.emailVerificationsMu.Unlock()

	// 2. Simulate the HTTP verification (what happens when user clicks the link)
	form := url.Values{}
	form.Add("token", token)
	req, err := http.NewRequest("POST", fmt.Sprintf("http://%s/verify-email", httpAddr), strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to perform verification: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Verification failed with status: %d", resp.StatusCode)
	}

	// 3. Check if verification was marked as complete
	// In the actual code, completion is signaled by closing CompleteChan
	// The verification entry should be deleted after completion
	server.emailVerificationsMu.Lock()
	_, exists := server.emailVerifications[token]
	server.emailVerificationsMu.Unlock()

	// After successful verification, the token should be removed
	if exists {
		t.Log("Warning: Verification token still exists after verification (might be OK depending on implementation)")
	}

	// 4. Now test if getUserByFingerprint can find the user
	user, err := server.getUserByFingerprint(fingerprint)
	if err != nil {
		t.Fatalf("Error getting user by fingerprint: %v", err)
	}
	if user == nil {
		t.Fatal("User is nil after verification - THIS IS THE BUG")
	}

	// Verify user details
	if user.Email != email {
		t.Errorf("User email mismatch: got %s, want %s", user.Email, email)
	}
	if user.PublicKeyFingerprint != fingerprint {
		t.Errorf("User fingerprint mismatch: got %s, want %s", user.PublicKeyFingerprint, fingerprint)
	}

	// 5. Check database state
	var userCount int
	err = server.db.QueryRow(`SELECT COUNT(*) FROM users WHERE public_key_fingerprint = ?`, fingerprint).Scan(&userCount)
	if err != nil {
		t.Fatalf("Failed to query users: %v", err)
	}
	if userCount != 1 {
		t.Errorf("Expected 1 user in database, got %d", userCount)
	}

	// Check if SSH key was stored
	var sshKeyCount int
	err = server.db.QueryRow(`SELECT COUNT(*) FROM ssh_keys WHERE fingerprint = ?`, fingerprint).Scan(&sshKeyCount)
	if err != nil {
		t.Fatalf("Failed to query ssh_keys: %v", err)  
	}
	t.Logf("SSH keys in database: %d", sshKeyCount)

	// Check team membership
	var teamCount int
	err = server.db.QueryRow(`SELECT COUNT(*) FROM team_members WHERE user_fingerprint = ?`, fingerprint).Scan(&teamCount)
	if err != nil {
		t.Fatalf("Failed to query team_members: %v", err)
	}
	if teamCount == 0 {
		t.Error("User not added to any team")
	}
}

// TestGetUserByFingerprintLogic tests the getUserByFingerprint function logic
func TestGetUserByFingerprintLogic(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_user_lookup_*.db")
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
	defer server.Stop()

	fingerprint := "test-fingerprint-123"
	email := "test@example.com"

	// Test 1: User doesn't exist
	user, err := server.getUserByFingerprint(fingerprint)
	if err != nil {
		t.Errorf("Expected no error for non-existent user, got: %v", err)
	}
	if user != nil {
		t.Error("Expected nil user for non-existent fingerprint")
	}

	// Test 2: Create user directly in users table
	err = server.createUser(fingerprint, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Test 3: User should be found by primary fingerprint
	user, err = server.getUserByFingerprint(fingerprint)
	if err != nil {
		t.Fatalf("Error getting user after creation: %v", err)
	}
	if user == nil {
		t.Fatal("User is nil after creation")
	}
	if user.Email != email {
		t.Errorf("Email mismatch: got %s, want %s", user.Email, email)
	}
	if user.PublicKeyFingerprint != fingerprint {
		t.Errorf("Fingerprint mismatch: got %s, want %s", user.PublicKeyFingerprint, fingerprint)
	}

	// Test 4: Create another SSH key for the same user
	newFingerprint := "another-fingerprint-456"
	_, err = server.db.Exec(`
		INSERT INTO ssh_keys (fingerprint, user_email, public_key, verified)
		VALUES (?, ?, 'test-public-key', 1)`,
		newFingerprint, email)
	if err != nil {
		t.Fatalf("Failed to add SSH key: %v", err)
	}

	// Test 5: User should be found by secondary fingerprint
	user2, err := server.getUserByFingerprint(newFingerprint)
	if err != nil {
		t.Fatalf("Error getting user by secondary fingerprint: %v", err)
	}
	if user2 == nil {
		t.Fatal("User is nil when looking up by secondary fingerprint")
	}
	if user2.Email != email {
		t.Errorf("Email mismatch for secondary lookup: got %s, want %s", user2.Email, email)
	}
}