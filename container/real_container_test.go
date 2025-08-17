package container

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestRealContainerSSHSetup creates an actual container and verifies the authorized_keys content
func TestRealContainerSSHSetup(t *testing.T) {
	start := time.Now()
	defer func() {
		duration := time.Since(start)
		if duration < 3*time.Second {
			t.Logf("Test completed in %v - consider running in all modes", duration)
		}
	}()

	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Generate SSH keys first
	sshKeys, err := GenerateContainerSSHKeys()
	if err != nil {
		t.Fatalf("Failed to generate SSH keys: %v", err)
	}

	t.Logf("Generated AuthorizedKeys: %s", sshKeys.AuthorizedKeys)
	t.Logf("Generated HostCertificate: %s", sshKeys.HostCertificate)

	// Also derive public key from private key to see if they match
	clientPrivKey, err := ssh.ParsePrivateKey([]byte(sshKeys.ClientPrivateKey))
	if err != nil {
		t.Fatalf("Failed to parse client private key: %v", err)
	}
	derivedPublicKey := string(ssh.MarshalAuthorizedKey(clientPrivKey.PublicKey()))
	t.Logf("Public key derived from ClientPrivateKey: %s", derivedPublicKey)

	if strings.TrimSpace(derivedPublicKey) != strings.TrimSpace(sshKeys.AuthorizedKeys) {
		t.Errorf("\u274c BUG IN KEY GENERATION: AuthorizedKeys doesn't match ClientPrivateKey!")
		t.Errorf("   AuthorizedKeys:    %s", strings.TrimSpace(sshKeys.AuthorizedKeys))
		t.Errorf("   Derived from priv: %s", strings.TrimSpace(derivedPublicKey))
	} else {
		t.Logf("\u2705 AuthorizedKeys matches ClientPrivateKey")
	}

	// Check that AuthorizedKeys is a public key, not a certificate
	if strings.Contains(sshKeys.AuthorizedKeys, "cert-v01") {
		t.Fatalf("ERROR: AuthorizedKeys contains certificate, should be public key: %s", sshKeys.AuthorizedKeys)
	}

	if !strings.HasPrefix(sshKeys.AuthorizedKeys, "ssh-ed25519 AAAAC3") {
		t.Fatalf("ERROR: AuthorizedKeys should start with 'ssh-ed25519 AAAAC3', got: %s", sshKeys.AuthorizedKeys)
	}

	t.Logf("✅ AuthorizedKeys is correctly a public key")

	config := &Config{
		DockerHosts:          []string{""},
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "128Mi",
	}

	manager, err := NewDockerManager(config)
	if err != nil {
		t.Fatalf("Failed to create Docker manager: %v", err)
	}

	// Create container request
	req := &CreateContainerRequest{
		UserID:        "test-user-ssh",
		Name:          "ssh-test-container",
		TeamName:      "test-team",
		Image:         "ubuntu:22.04",
		Size:          "small",
		CPURequest:    "100m",
		MemoryRequest: "128Mi",
		StorageSize:   "1Gi",
		Ephemeral:     false, // Keep it around long enough to check
	}

	ctx := context.Background()
	container, err := manager.CreateContainer(ctx, req)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}

	t.Logf("Container created: %s", container.ID)

	// Cleanup
	defer func() {
		err := manager.DeleteContainer(ctx, req.UserID, container.ID)
		if err != nil {
			t.Logf("Failed to delete container: %v", err)
		}
	}()

	// Wait for SSH setup to complete by checking if SSH port is accessible
	t.Logf("Waiting for SSH setup to complete...")
	sshPort := container.SSHPort
	t.Logf("Container SSH port: %d", sshPort)

	for i := 0; i < 30; i++ { // 30 seconds max
		// Try to connect to the SSH port to verify it's ready
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", sshPort), 2*time.Second)
		if err == nil {
			conn.Close()
			t.Logf("SSH port %d is accessible", sshPort)
			break
		}
		if i == 29 {
			t.Fatalf("SSH setup did not complete within 30 seconds")
		}
		time.Sleep(1 * time.Second)
	}

	// Test SSH connection directly - this is better than checking file contents
	t.Logf("Testing SSH connection to container on port %d", sshPort)

	// Parse the client private key for SSH connection
	clientPrivKey, err = ssh.ParsePrivateKey([]byte(sshKeys.ClientPrivateKey))
	if err != nil {
		t.Fatalf("Failed to parse client private key: %v", err)
	}

	// Configure SSH client
	sshConfig := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(clientPrivKey),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // For testing only
		Timeout:         5 * time.Second,
	}

	// Try to connect via SSH
	client, err := ssh.Dial("tcp", fmt.Sprintf("localhost:%d", sshPort), sshConfig)
	if err != nil {
		t.Fatalf("❌ CRITICAL BUG: Failed to SSH connect to container: %v", err)
	}
	defer client.Close()

	// Run a simple command to verify the connection works
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("Failed to create SSH session: %v", err)
	}
	defer session.Close()

	output, err := session.Output("echo 'SSH connection successful'")
	if err != nil {
		t.Fatalf("Failed to run command via SSH: %v", err)
	}

	if !strings.Contains(string(output), "SSH connection successful") {
		t.Fatalf("SSH command output unexpected: %s", string(output))
	}

	t.Logf("✅ SUCCESS: SSH connection and authentication working correctly")

	// Also check authorized_keys file for completeness
	var stdout strings.Builder
	err = manager.ExecuteInContainer(ctx, req.UserID, container.ID,
		[]string{"cat", "/root/.ssh/authorized_keys"},
		nil, &stdout, nil)
	if err != nil {
		t.Fatalf("Failed to read authorized_keys from container: %v", err)
	}

	authorizedKeysContent := strings.TrimSpace(stdout.String())
	t.Logf("=== CONTAINER authorized_keys content ===")
	t.Logf("%s", authorizedKeysContent)

	// Verify the authorized_keys content matches
	expectedContent := strings.TrimSpace(sshKeys.AuthorizedKeys)
	if authorizedKeysContent != expectedContent {
		t.Errorf("❌ WARNING: Container authorized_keys doesn't match generated AuthorizedKeys!")
		t.Errorf("   Generated: %s", expectedContent)
		t.Errorf("   Container: %s", authorizedKeysContent)

		// Check if container has a certificate
		if strings.Contains(authorizedKeysContent, "cert-v01") {
			t.Errorf("❌ Container has CERTIFICATE in authorized_keys")
		}
	} else {
		t.Logf("✅ Container authorized_keys matches generated AuthorizedKeys")
	}
}
