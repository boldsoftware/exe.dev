package execore

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// mockConnMetadata implements libplugin.ConnMetadata for testing
type mockConnMetadata struct {
	user string
	addr string
}

func (m mockConnMetadata) User() string              { return m.user }
func (m mockConnMetadata) RemoteAddr() string        { return m.addr }
func (m mockConnMetadata) UniqueID() string          { return "test-unique-id" }
func (m mockConnMetadata) GetMeta(key string) string { return "" }
func (m mockConnMetadata) LocalAddress() string      { return "127.0.0.1:2222" }

// generateTestHostKey creates a properly formatted SSH public key for testing
func generateTestHostKey(t *testing.T) []byte {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate test key: %v", err)
	}
	pubKey, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("Failed to create SSH public key: %v", err)
	}
	return pubKey.Marshal()
}

// TestVerifyHostKeyRejectsUnknownKeys verifies our implementation properly rejects unknown host keys
// and only accepts keys from machines with stored expected host keys
func TestVerifyHostKeyRejectsUnknownKeys(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)

	piper := NewPiperPlugin(server, "127.0.0.1", 0)
	mockConn := mockConnMetadata{
		user: "testuser",
		addr: "127.0.0.1:54321",
	}

	testCases := []struct {
		name     string
		hostname string
		netaddr  string
		hostKey  []byte
	}{
		{
			name:     "exed_local_connection",
			hostname: "127.0.0.1",
			netaddr:  "127.0.0.1:2223",
			hostKey:  generateTestHostKey(t),
		},
		{
			name:     "container_connection",
			hostname: "127.0.0.1",
			netaddr:  "127.0.0.1:32768",
			hostKey:  generateTestHostKey(t),
		},
		{
			name:     "localhost_connection",
			hostname: "localhost",
			netaddr:  "localhost:2223",
			hostKey:  generateTestHostKey(t),
		},
		{
			name:     "empty_host_key",
			hostname: "127.0.0.1",
			netaddr:  "127.0.0.1:2223",
			hostKey:  []byte{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := piper.handleVerifyHostKey(mockConn, tc.hostname, tc.netaddr, tc.hostKey)
			if err == nil {
				t.Errorf("VerifyHostKey should reject unknown keys but accepted one for %s", tc.name)
			} else {
				t.Logf("✅ Properly rejected unknown host key for %s -> %s (key length: %d): %v", tc.hostname, tc.netaddr, len(tc.hostKey), err)
			}
		})
	}
}

// TestVerifyHostKeyAcceptsKnownKeys verifies that stored host keys are properly validated
func TestVerifyHostKeyAcceptsKnownKeys(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	piper := NewPiperPlugin(server, "127.0.0.1", 0)
	testMachineName := "test-machine"
	mockConn := mockConnMetadata{
		user: testMachineName,
		addr: "127.0.0.1:54321",
	}

	// Generate a test host key
	hostKeyBytes := generateTestHostKey(t)
	pubKey, err := ssh.ParsePublicKey(hostKeyBytes)
	if err != nil {
		t.Fatalf("Failed to parse generated host key: %v", err)
	}
	testHostKeyString := string(ssh.MarshalAuthorizedKey(pubKey))

	// Store the expected host key for this connection using the mock's unique ID
	connID := "test-unique-id" // This matches what mockConnMetadata.UniqueID() returns
	piper.storeExpectedHostKeyForConnection(connID, testHostKeyString)

	// Test host key validation - should accept the stored key
	err = piper.handleVerifyHostKey(mockConn, "127.0.0.1", "127.0.0.1:32768", hostKeyBytes)
	if err != nil {
		t.Errorf("VerifyHostKey should accept known keys but rejected it: %v", err)
	} else {
		t.Logf("✅ Successfully accepted known host key for machine %s", testMachineName)
	}
}

// TestVerifyHostKeyExpiration verifies that expired host keys are rejected
func TestVerifyHostKeyExpiration(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	piper := NewPiperPlugin(server, "127.0.0.1", 0)
	testMachineName := "expire-test-machine"
	mockConn := mockConnMetadata{
		user: testMachineName,
		addr: "127.0.0.1:54321",
	}

	// Generate a test host key
	hostKeyBytes := generateTestHostKey(t)
	pubKey, err := ssh.ParsePublicKey(hostKeyBytes)
	if err != nil {
		t.Fatalf("Failed to parse generated host key: %v", err)
	}
	testHostKeyString := string(ssh.MarshalAuthorizedKey(pubKey))

	// Store the expected host key for this connection
	connID := "test-unique-id"
	piper.storeExpectedHostKeyForConnection(connID, testHostKeyString)

	// Test that key is accepted when fresh
	err = piper.handleVerifyHostKey(mockConn, "127.0.0.1", "127.0.0.1:32768", hostKeyBytes)
	if err != nil {
		t.Errorf("VerifyHostKey should accept fresh keys but rejected it: %v", err)
	}

	// Manually expire the key by modifying its CreatedAt timestamp
	piper.expectedHostKeysMutex.Lock()
	if mapping, exists := piper.expectedHostKeys[connID]; exists {
		mapping.CreatedAt = time.Now().Add(-6 * time.Minute) // 6 minutes ago
	}
	piper.expectedHostKeysMutex.Unlock()

	// Test that expired key is rejected
	err = piper.handleVerifyHostKey(mockConn, "127.0.0.1", "127.0.0.1:32768", hostKeyBytes)
	if err == nil {
		t.Errorf("VerifyHostKey should reject expired keys but accepted it")
	} else {
		t.Logf("✅ Successfully rejected expired host key: %v", err)
	}
}
