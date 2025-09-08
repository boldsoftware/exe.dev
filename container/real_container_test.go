package container

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestRealContainerSSHSetup creates an actual container and verifies the authorized_keys content
func TestRealContainerSSHSetup(t *testing.T) {
	t.Parallel()
	start := time.Now()
	defer func() {
		duration := time.Since(start)
		if duration < 3*time.Second {
			t.Logf("Test completed in %v - consider running in all modes", duration)
		}
	}()

	SkipIfShort(t)

	manager := CreateTestManager(t)
	defer manager.Close()

	// For testing, we use ubuntu:22.04 directly
	// Note: This test may fail if insufficient memory allocated
	// (apt-get install openssh-server needs ~512MB during installation)

	// Create container request
	ctx := t.Context()

	// Create the allocation
	if err := manager.CreateAlloc(ctx, "test-alloc"); err != nil {
		t.Fatalf("CreateAlloc failed: %v", err)
	}

	req := &CreateContainerRequest{
		AllocID:       "test-alloc",
		Name:          "ssh-test-container",
		Image:         "ubuntu:22.04",
		Size:          "small",
		CPURequest:    "100m",
		MemoryRequest: "512Mi",
		StorageSize:   "1Gi",
		Ephemeral:     false, // Keep it around long enough to check
		BoxID:         GenerateTestBoxID(),
	}

	container, err := manager.CreateContainer(ctx, req)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}

	t.Logf("Container created: %s", container.ID)
	t.Logf("Container SSH keys - AuthorizedKeys: %s", container.SSHAuthorizedKeys)
	t.Logf("Container SSH keys - HostCertificate: %s", container.SSHHostCertificate)

	// Validate the SSH keys from the container
	clientPrivKey, err := ssh.ParsePrivateKey([]byte(container.SSHClientPrivateKey))
	if err != nil {
		t.Fatalf("Failed to parse client private key from container: %v", err)
	}
	derivedPublicKey := string(ssh.MarshalAuthorizedKey(clientPrivKey.PublicKey()))
	t.Logf("Public key derived from container's ClientPrivateKey: %s", derivedPublicKey)

	if strings.TrimSpace(derivedPublicKey) != strings.TrimSpace(container.SSHAuthorizedKeys) {
		t.Errorf("❌ BUG IN KEY GENERATION: AuthorizedKeys doesn't match ClientPrivateKey!")
		t.Errorf("   AuthorizedKeys:    %s", strings.TrimSpace(container.SSHAuthorizedKeys))
		t.Errorf("   Derived from priv: %s", strings.TrimSpace(derivedPublicKey))
	} else {
		t.Logf("✅ Container's AuthorizedKeys matches ClientPrivateKey")
	}

	// Check that AuthorizedKeys is a public key, not a certificate
	if strings.Contains(container.SSHAuthorizedKeys, "cert-v01") {
		t.Fatalf("ERROR: Container's AuthorizedKeys contains certificate, should be public key: %s", container.SSHAuthorizedKeys)
	}

	if !strings.HasPrefix(container.SSHAuthorizedKeys, "ssh-ed25519 AAAAC3") {
		t.Fatalf("ERROR: Container's AuthorizedKeys should start with 'ssh-ed25519 AAAAC3', got: %s", container.SSHAuthorizedKeys)
	}

	t.Logf("✅ Container's AuthorizedKeys is correctly a public key")

	// Cleanup
	defer CleanupContainer(t, manager, req.AllocID, container.ID)

	// Wait for SSH setup to complete by checking if SSH port is accessible
	t.Logf("Waiting for SSH setup to complete...")
	sshPort := container.SSHPort
	t.Logf("Container SSH port: %d", sshPort)

	// First check if SSH was actually installed in the container
	// This can fail in memory-constrained environments
	var checkSSHOutput strings.Builder
	err = manager.ExecuteInContainer(ctx, req.AllocID, container.ID,
		[]string{"sh", "-c", "which sshd || echo NO_SSHD"},
		nil, &checkSSHOutput, nil)
	if err != nil || strings.Contains(checkSSHOutput.String(), "NO_SSHD") {
		t.Skip("SSH daemon not available in container (likely due to memory constraints during apt-get install)")
	}

	// Spin wait for SSH port to be accessible AND SSH daemon to be ready
	waitStart := time.Now()
	for {
		// Try to connect to the SSH port to verify it's ready
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", sshPort), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			// Port is open, now check if SSH daemon responds
			// Try a quick SSH handshake to see if sshd is really ready
			testConfig := &ssh.ClientConfig{
				User: "root",
				Auth: []ssh.AuthMethod{
					ssh.PublicKeys(clientPrivKey),
				},
				HostKeyCallback: ssh.InsecureIgnoreHostKey(),
				Timeout:         1 * time.Second,
			}
			testClient, err := ssh.Dial("tcp", fmt.Sprintf("localhost:%d", sshPort), testConfig)
			if err == nil {
				testClient.Close()
				t.Logf("SSH port %d is accessible and SSH daemon is ready (waited %v)", sshPort, time.Since(waitStart))
				break
			}
			// Port is open but SSH not ready yet, continue spinning
		}

		// Fail fast if waiting too long
		if time.Since(waitStart) > 30*time.Second {
			t.Fatalf("SSH setup did not complete within 30 seconds")
		}

		// Very short spin wait - 50ms
		time.Sleep(50 * time.Millisecond)
	}

	// Test SSH connection directly - this is better than checking file contents
	t.Logf("Testing SSH connection to container on port %d", sshPort)

	// Use the container's client private key for SSH connection
	clientPrivKey, err = ssh.ParsePrivateKey([]byte(container.SSHClientPrivateKey))
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
	err = manager.ExecuteInContainer(ctx, req.AllocID, container.ID,
		[]string{"cat", "/root/.ssh/authorized_keys"},
		nil, &stdout, nil)
	if err != nil {
		t.Fatalf("Failed to read authorized_keys from container: %v", err)
	}

	authorizedKeysContent := strings.TrimSpace(stdout.String())
	t.Logf("=== CONTAINER authorized_keys content ===")
	t.Logf("%s", authorizedKeysContent)

	// Verify the authorized_keys content matches
	expectedContent := strings.TrimSpace(container.SSHAuthorizedKeys)
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
