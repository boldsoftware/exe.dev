package container

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestDebugSSHKeyGeneration tests SSH key generation logic in isolation
// This is complementary to TestRealContainerSSHSetup which tests full integration
func TestDebugSSHKeyGeneration(t *testing.T) {
	sshKeys, err := GenerateContainerSSHKeys()
	if err != nil {
		t.Fatalf("Failed to generate SSH keys: %v", err)
	}

	t.Logf("=== SSH Keys Debug ===")
	t.Logf("ServerIdentityKey (first 100 chars): %s", sshKeys.ServerIdentityKey[:100])
	t.Logf("AuthorizedKeys: %s", sshKeys.AuthorizedKeys)
	t.Logf("ClientPrivateKey (first 100 chars): %s", sshKeys.ClientPrivateKey[:100])

	// Test that the private key can be parsed
	clientPrivKey, err := ssh.ParsePrivateKey([]byte(sshKeys.ClientPrivateKey))
	if err != nil {
		t.Fatalf("Failed to parse client private key: %v", err)
	}

	// Get the public key from the private key
	pubKeyFromPriv := clientPrivKey.PublicKey()
	pubKeyFromPrivString := string(ssh.MarshalAuthorizedKey(pubKeyFromPriv))

	t.Logf("\n=== Key Validation Test ===")
	t.Logf("Public key derived from private key: %s", strings.TrimSpace(pubKeyFromPrivString))
	t.Logf("AuthorizedKeys content (certificate): %s", strings.TrimSpace(sshKeys.AuthorizedKeys))

	// Verify that AuthorizedKeys matches the public key derived from private key
	if strings.TrimSpace(pubKeyFromPrivString) == strings.TrimSpace(sshKeys.AuthorizedKeys) {
		t.Logf("✅ SUCCESS: Private key and authorized_keys match!")
	} else {
		t.Errorf("❌ MISMATCH: Private key and authorized_keys don't match!")
		t.Errorf("   Expected: %s", strings.TrimSpace(pubKeyFromPrivString))
		t.Errorf("   Got:      %s", strings.TrimSpace(sshKeys.AuthorizedKeys))
	}

	// Verify that AuthorizedKeys is a public key, not a certificate
	if strings.HasPrefix(sshKeys.AuthorizedKeys, "ssh-ed25519 AAAAC3") {
		t.Logf("✅ SUCCESS: AuthorizedKeys is correctly a public key")
	} else {
		t.Errorf("❌ ERROR: AuthorizedKeys should be a public key, got: %s", sshKeys.AuthorizedKeys)
	}
}
