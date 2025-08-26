package container

import (
	"crypto/ed25519"
	"encoding/pem"
	"testing"
	
	"golang.org/x/crypto/ssh"
)

// TestSSHKeyGeneration verifies that SSH keys are correctly generated and formatted
func TestSSHKeyGeneration(t *testing.T) {
	// Generate keys
	keys, err := GenerateContainerSSHKeys()
	if err != nil {
		t.Fatalf("failed to generate SSH keys: %v", err)
	}

	// Test server private key can be parsed
	t.Run("ServerPrivateKey", func(t *testing.T) {
		block, _ := pem.Decode([]byte(keys.ServerIdentityKey))
		if block == nil {
			t.Fatal("failed to decode server private key PEM block")
		}
		
		// The block type should be OPENSSH PRIVATE KEY
		if block.Type != "OPENSSH PRIVATE KEY" {
			t.Errorf("unexpected PEM block type: got %q, want %q", block.Type, "OPENSSH PRIVATE KEY")
		}
		
		// Verify we can parse it as an SSH private key
		privKey, err := ssh.ParsePrivateKey([]byte(keys.ServerIdentityKey))
		if err != nil {
			t.Fatalf("failed to parse server private key: %v", err)
		}
		
		// Verify it's an Ed25519 key
		if privKey.PublicKey().Type() != "ssh-ed25519" {
			t.Errorf("unexpected key type: got %q, want %q", privKey.PublicKey().Type(), "ssh-ed25519")
		}
	})
	
	// Test client private key can be parsed
	t.Run("ClientPrivateKey", func(t *testing.T) {
		block, _ := pem.Decode([]byte(keys.ClientPrivateKey))
		if block == nil {
			t.Fatal("failed to decode client private key PEM block")
		}
		
		// The block type should be OPENSSH PRIVATE KEY
		if block.Type != "OPENSSH PRIVATE KEY" {
			t.Errorf("unexpected PEM block type: got %q, want %q", block.Type, "OPENSSH PRIVATE KEY")
		}
		
		// Verify we can parse it as an SSH private key
		privKey, err := ssh.ParsePrivateKey([]byte(keys.ClientPrivateKey))
		if err != nil {
			t.Fatalf("failed to parse client private key: %v", err)
		}
		
		// Verify it's an Ed25519 key
		if privKey.PublicKey().Type() != "ssh-ed25519" {
			t.Errorf("unexpected key type: got %q, want %q", privKey.PublicKey().Type(), "ssh-ed25519")
		}
	})
	
	// Test authorized keys format
	t.Run("AuthorizedKeys", func(t *testing.T) {
		// Should be a valid SSH public key in authorized_keys format
		pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(keys.AuthorizedKeys))
		if err != nil {
			t.Fatalf("failed to parse authorized keys: %v", err)
		}
		
		if pubKey.Type() != "ssh-ed25519" {
			t.Errorf("unexpected key type in authorized_keys: got %q, want %q", pubKey.Type(), "ssh-ed25519")
		}
	})
	
	// Test CA public key format
	t.Run("CAPublicKey", func(t *testing.T) {
		pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(keys.CAPublicKey))
		if err != nil {
			t.Fatalf("failed to parse CA public key: %v", err)
		}
		
		if pubKey.Type() != "ssh-ed25519" {
			t.Errorf("unexpected CA key type: got %q, want %q", pubKey.Type(), "ssh-ed25519")
		}
	})
}

// TestMarshalPrivateKeyFormat checks the exact format issue
func TestMarshalPrivateKeyFormat(t *testing.T) {
	// Generate an Ed25519 key
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	
	// Use ssh.MarshalPrivateKey - this returns a *pem.Block
	block, err := ssh.MarshalPrivateKey(priv, "test comment")
	if err != nil {
		t.Fatalf("failed to marshal private key: %v", err)
	}
	
	// The block is already a PEM block, not PEM-encoded bytes
	t.Logf("Block type: %s", block.Type)
	t.Logf("Block headers: %v", block.Headers)
	
	// To get PEM-encoded bytes, we need to encode the block
	pemBytes := pem.EncodeToMemory(block)
	
	// Now verify we can parse it
	_, err = ssh.ParsePrivateKey(pemBytes)
	if err != nil {
		t.Fatalf("failed to parse marshaled key: %v", err)
	}
}