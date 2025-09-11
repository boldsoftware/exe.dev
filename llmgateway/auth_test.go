package llmgateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// mockBoxKeyAuthority implements boxKeyAuthority for testing
type mockBoxKeyAuthority struct {
	keys map[string]string // boxName -> SSH public key string
}

func (m *mockBoxKeyAuthority) SSHIdentityKeyForBox(ctx context.Context, name string) (string, error) {
	key, exists := m.keys[name]
	if !exists {
		return "", fmt.Errorf("box not found: %s", name)
	}
	return key, nil
}

var _ boxKeyAuthority = &mockBoxKeyAuthority{}

// testKeyPair holds a key pair for testing
type testKeyPair struct {
	privateKey    any        // crypto private key
	publicKey     any        // crypto public key
	sshPublicKey  string     // SSH format public key
	sshPrivateKey ssh.Signer // SSH signer
	keyType       string     // "ed25519"
}

// generateTestKeys creates Ed25519 test key pair (as used by exe.dev containers)
func generateTestKeys(t *testing.T) *testKeyPair {
	// Generate Ed25519 key pair
	ed25519Pub, ed25519Priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate Ed25519 key: %v", err)
	}

	// Convert to SSH format
	ed25519SSHSigner, err := ssh.NewSignerFromKey(ed25519Priv)
	if err != nil {
		t.Fatalf("Failed to create Ed25519 SSH signer: %v", err)
	}
	ed25519SSHPubKey := string(ssh.MarshalAuthorizedKey(ed25519SSHSigner.PublicKey()))

	return &testKeyPair{
		privateKey:    ed25519Priv,
		publicKey:     ed25519Pub,
		sshPublicKey:  ed25519SSHPubKey,
		sshPrivateKey: ed25519SSHSigner,
		keyType:       "ed25519",
	}
}

// Benchmark the authentication process
func TestNewBearerToken_And_BearerTokenAuth(t *testing.T) {
	// Generate test keys
	keyPair := generateTestKeys(t)

	// Create test parameters
	boxName := "test-box"
	startTime := time.Now().Add(-5 * time.Minute) // 5 minutes ago
	duration := 10 * time.Minute

	// Create bearer token
	token := NewBearerToken(boxName, startTime, duration)

	// Verify token fields
	if token.BoxName != boxName {
		t.Errorf("Expected BoxName %s, got %s", boxName, token.BoxName)
	}
	if !token.StartTime.Equal(startTime) {
		t.Errorf("Expected StartTime %v, got %v", startTime, token.StartTime)
	}
	if token.Duration != duration {
		t.Errorf("Expected Duration %v, got %v", duration, token.Duration)
	}
	sig, err := token.Sign(keyPair.sshPrivateKey)
	if err != nil {
		t.Fatalf("Failed to sign bearer token: %v", err)
	}
	if len(sig) == 0 {
		t.Error("Expected non-empty signature")
	}

	// Create mock authority with test key
	mockAuth := &mockBoxKeyAuthority{
		keys: map[string]string{
			boxName: keyPair.sshPublicKey,
		},
	}

	// Create llmProxy instance with fixed time for testing
	fixedTime := startTime.Add(2 * time.Minute) // 2 minutes after start, well within duration
	proxy := &llmGateway{
		boxKeyAuthority: mockAuth,
		now:             func() time.Time { return fixedTime },
	}

	// Encode token for Authorization header
	tokenEncoded, err := token.Encode(keyPair.sshPrivateKey)
	if err != nil {
		t.Fatalf("Failed to encode token: %v", err)
	}

	// Create HTTP request with Authorization Bearer header
	req := httptest.NewRequest("POST", "http://example.com/api/test", strings.NewReader("test body"))
	req.Header.Set("Authorization", "Bearer "+tokenEncoded)

	// Test authentication
	authenticatedBoxName, err := proxy.boxKeyAuth(context.Background(), req)
	if err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}

	if authenticatedBoxName != boxName {
		t.Errorf("Expected authenticated box name %s, got %s", boxName, authenticatedBoxName)
	}
}

func TestBearerTokenAuth_ExpiredToken(t *testing.T) {
	keyPair := generateTestKeys(t)
	boxName := "test-box"

	// Create expired token (started 10 minutes ago, duration 5 minutes)
	startTime := time.Now().Add(-10 * time.Minute)
	duration := 5 * time.Minute

	token := NewBearerToken(boxName, startTime, duration)

	// Create mock authority
	mockAuth := &mockBoxKeyAuthority{
		keys: map[string]string{
			boxName: keyPair.sshPublicKey,
		},
	}

	// Use current time (token should be expired)
	proxy := &llmGateway{
		boxKeyAuthority: mockAuth,
		now:             time.Now,
	}

	// Create request with expired token
	tokenEncoded, _ := token.Encode(keyPair.sshPrivateKey)
	req := httptest.NewRequest("POST", "http://example.com/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenEncoded)
	t.Logf("tokenEncoded: %q", tokenEncoded)

	// Test authentication should fail
	_, err := proxy.boxKeyAuth(context.Background(), req)
	if err == nil {
		t.Error("Expected authentication to fail for expired token")
	}
	if !strings.Contains(err.Error(), "token expired") {
		t.Errorf("Expected token expired error, got: %v", err)
	}
}

func TestBearerTokenAuth_MissingAuthHeader(t *testing.T) {
	mockAuth := &mockBoxKeyAuthority{keys: make(map[string]string)}
	proxy := &llmGateway{
		boxKeyAuthority: mockAuth,
		now:             time.Now,
	}

	// Create request without Authorization header
	req := httptest.NewRequest("POST", "http://example.com/api/test", nil)

	// Test authentication should fail
	_, err := proxy.boxKeyAuth(context.Background(), req)
	if err == nil {
		t.Error("Expected authentication to fail for missing authorization header")
	}
	if !strings.Contains(err.Error(), "no authorization header provided") {
		t.Errorf("Expected missing auth header error, got: %v", err)
	}
}

func TestBearerTokenAuth_InvalidBase64(t *testing.T) {
	mockAuth := &mockBoxKeyAuthority{keys: make(map[string]string)}
	proxy := &llmGateway{
		boxKeyAuthority: mockAuth,
		now:             time.Now,
	}

	// Create request with invalid base64 in Authorization header
	req := httptest.NewRequest("POST", "http://example.com/api/test", nil)
	req.Header.Set("Authorization", "Bearer {invalid-base64!")

	// Test authentication should fail
	_, err := proxy.boxKeyAuth(context.Background(), req)
	if err == nil {
		t.Error("Expected authentication to fail for invalid base64")
	}
	if !strings.Contains(err.Error(), "invalid bearer token") {
		t.Errorf("Expected base64 decode error, got: %v", err)
	}
}

func TestBearerTokenAuth_InvalidSignature(t *testing.T) {
	keyPair := generateTestKeys(t)
	wrongKeyPair := generateTestKeys(t) // Different key pair
	boxName := "test-box"

	// Create token with one key
	startTime := time.Now()
	duration := 10 * time.Minute
	token := NewBearerToken(boxName, startTime, duration)

	// Create mock authority with different key
	mockAuth := &mockBoxKeyAuthority{
		keys: map[string]string{
			boxName: wrongKeyPair.sshPublicKey, // Wrong key!
		},
	}

	proxy := &llmGateway{
		boxKeyAuthority: mockAuth,
		now:             func() time.Time { return startTime.Add(1 * time.Minute) },
	}

	// Create request
	tokenEncoded, _ := token.Encode(keyPair.sshPrivateKey)
	req := httptest.NewRequest("POST", "http://example.com/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenEncoded)

	// Test authentication should fail
	_, err := proxy.boxKeyAuth(context.Background(), req)
	if err == nil {
		t.Fatal("Expected authentication to fail for invalid signature")
	}
	if !strings.Contains(err.Error(), "verifying signature") {
		t.Errorf("Expected signature verification error, got: %v", err)
	}
}
