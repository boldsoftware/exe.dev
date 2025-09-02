package container

import (
	"strings"
	"testing"
)

// TestGenerateContainerSSHKeys tests SSH key generation
func TestGenerateContainerSSHKeys(t *testing.T) {
	sshKeys, err := GenerateContainerSSHKeys()
	if err != nil {
		t.Fatalf("Failed to generate SSH keys: %v", err)
	}

	// Verify all keys are generated
	if sshKeys.ServerIdentityKey == "" {
		t.Error("ServerIdentityKey is empty")
	}
	if sshKeys.AuthorizedKeys == "" {
		t.Error("AuthorizedKeys is empty")
	}
	if sshKeys.CAPublicKey == "" {
		t.Error("CAPublicKey is empty")
	}
	if sshKeys.HostCertificate == "" {
		t.Error("HostCertificate is empty")
	}
	if sshKeys.ClientPrivateKey == "" {
		t.Error("ClientPrivateKey is empty")
	}
	if sshKeys.SSHPort != 22 {
		t.Errorf("Expected SSHPort to be 22, got %d", sshKeys.SSHPort)
	}

	// Verify keys are in correct format
	if !strings.Contains(sshKeys.ServerIdentityKey, "OPENSSH PRIVATE KEY") {
		t.Error("ServerIdentityKey not in OpenSSH format")
	}
	if !strings.Contains(sshKeys.ClientPrivateKey, "OPENSSH PRIVATE KEY") {
		t.Error("ClientPrivateKey not in OpenSSH format")
	}

	// Verify public keys start with expected prefixes
	if !strings.HasPrefix(sshKeys.AuthorizedKeys, "ssh-ed25519") {
		t.Error("AuthorizedKeys not in ed25519 public key format")
	}
	if !strings.HasPrefix(sshKeys.CAPublicKey, "ssh-ed25519") {
		t.Error("CAPublicKey not in ed25519 format")
	}
	if !strings.HasPrefix(sshKeys.HostCertificate, "ssh-ed25519-cert-v01@openssh.com") {
		t.Error("HostCertificate not in certificate format")
	}
}

// TestSSHKeyParsing tests SSH key parsing functions
func TestSSHKeyParsing(t *testing.T) {
	sshKeys, err := GenerateContainerSSHKeys()
	if err != nil {
		t.Fatalf("Failed to generate SSH keys: %v", err)
	}

	// Test parsing private key
	_, err = ParsePrivateKey(sshKeys.ServerIdentityKey)
	if err != nil {
		t.Errorf("Failed to parse server private key: %v", err)
	}

	_, err = ParsePrivateKey(sshKeys.ClientPrivateKey)
	if err != nil {
		t.Errorf("Failed to parse client private key: %v", err)
	}

	// Test creating SSH signer
	_, err = CreateSSHSigner(sshKeys.ServerIdentityKey)
	if err != nil {
		t.Errorf("Failed to create SSH signer from server key: %v", err)
	}

	_, err = CreateSSHSigner(sshKeys.ClientPrivateKey)
	if err != nil {
		t.Errorf("Failed to create SSH signer from client key: %v", err)
	}
}

// TestContainerWithSSH tests creating a container with SSH enabled
// Note: The CTR_HOST-dependent TestContainerWithSSH has been replaced by the
// consolidated integration suite (integration_suite_test.go).
