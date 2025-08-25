package container

import (
	"context"
	"strings"
	"testing"
	"time"
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
func TestContainerWithSSH(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := &Config{
		DockerHosts:          []string{""},
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "128Mi",
	}

	manager, err := NewDockerManager(config)
	if err != nil {
		t.Fatalf("Failed to create Docker manager: %v", err)
	}
	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create container request
	req := &CreateContainerRequest{
		AllocID: "test-alloc",
		Name:    "ssh-test",
		Image:   "ubuntu:22.04",
	}

	// Create container
	container, err := manager.CreateContainer(ctx, req)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}
	defer func() {
		// Cleanup
		manager.DeleteContainer(context.Background(), req.AllocID, container.ID)
	}()

	// Verify SSH keys were generated
	if container.SSHServerIdentityKey == "" {
		t.Error("Container SSH server identity key is empty")
	}
	if container.SSHAuthorizedKeys == "" {
		t.Error("Container SSH authorized keys is empty")
	}
	if container.SSHCAPublicKey == "" {
		t.Error("Container SSH CA public key is empty")
	}
	if container.SSHHostCertificate == "" {
		t.Error("Container SSH host certificate is empty")
	}
	if container.SSHClientPrivateKey == "" {
		t.Error("Container SSH client private key is empty")
	}
	if container.SSHPort == 0 {
		t.Error("Container SSH port is 0")
	}

	// Wait for SSH setup to complete by checking for SSH daemon process
	// SSH setup runs in a goroutine, so we need to wait for it to finish
	waitStart := time.Now()
	var sshRunning bool
	for time.Since(waitStart) < 30*time.Second {
		var stdout strings.Builder
		err = manager.ExecuteInContainer(ctx, req.AllocID, container.ID,
			[]string{"sh", "-c", "ps aux | grep -v grep | grep -E '/sshd.*-D' || true"},
			nil, &stdout, nil)
		output := strings.TrimSpace(stdout.String())
		// Check if we found the actual SSH daemon (not mkdir or other setup commands)
		if err == nil && output != "" && strings.Contains(output, "/sshd") && strings.Contains(output, "-D") {
			sshRunning = true
			t.Logf("SSH daemon process found: %s", output)
			break
		}
		// Check if container is still running
		var statusOut strings.Builder
		statusErr := manager.ExecuteInContainer(ctx, req.AllocID, container.ID,
			[]string{"echo", "alive"},
			nil, &statusOut, nil)
		if statusErr != nil {
			t.Fatalf("Container stopped unexpectedly during SSH setup: %v", statusErr)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !sshRunning {
		t.Errorf("SSH daemon not running in container after 30 seconds")
	}

	// Test that SSH port is accessible - try both netstat and ss
	var portOut strings.Builder
	err = manager.ExecuteInContainer(ctx, req.AllocID, container.ID,
		[]string{"sh", "-c", "netstat -tuln 2>/dev/null || ss -tuln 2>/dev/null || echo 'No network tools available'"},
		nil, &portOut, nil)
	if err != nil {
		t.Logf("Warning: network tools check failed: %v", err)
	} else {
		output := portOut.String()
		if strings.Contains(output, "No network tools available") {
			t.Logf("Network tools not available, skipping port check")
		} else if !strings.Contains(output, ":22 ") && !strings.Contains(output, ":22\t") {
			t.Logf("SSH port 22 may not be listening (output: %s)", output)
		}
	}

	// Verify SSH key files exist in container - only if SSH daemon is running
	if sshRunning {
		var keyFileCheck strings.Builder
		err = manager.ExecuteInContainer(ctx, req.AllocID, container.ID,
			[]string{"ls", "-la", "/etc/ssh/ssh_host_ed25519_key", "/etc/ssh/ssh_host_ed25519_key.pub", "/root/.ssh/authorized_keys"},
			nil, &keyFileCheck, nil)
		if err != nil {
			t.Errorf("SSH key files not found in container: %v", err)
		} else {
			t.Logf("SSH key files found: %s", keyFileCheck.String())
		}
	}
}

// TestSSHPortParsing tests the SSH port parsing logic (legacy test - now uses TestParseDockerPortMapping)
func TestSSHPortParsing(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected int
		hasError bool
	}{
		{"IPv4 format", "0.0.0.0:32768", 32768, false},
		{"IPv6 format", "[::]:32769", 32769, false},
		{"Standard port", "0.0.0.0:22", 22, false},
		{"Empty output", "", 0, true},
		{"Invalid format", "invalid", 0, true},
		{"IPv4 no colon", "32770", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			portStr := strings.TrimSpace(tt.output)
			if portStr == "" && tt.hasError {
				return // Expected error case
			}

			port, err := parseDockerPortMapping(portStr)

			if tt.hasError {
				if err == nil {
					t.Errorf("Expected error for input %q, but got none", tt.output)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error for input %q: %v", tt.output, err)
				return
			}

			if port != tt.expected {
				t.Errorf("Expected port %d, got %d", tt.expected, port)
			}
		})
	}
}

// TestParseDockerPortMapping tests the Docker port mapping parser
func TestParseDockerPortMapping(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
		hasError bool
	}{
		{"IPv4 format", "0.0.0.0:32768", 32768, false},
		{"IPv6 format", "[::]:32769", 32769, false},
		{"Standard port", "0.0.0.0:22", 22, false},
		{"High port IPv6", "[::]:65535", 65535, false},
		{"Low port IPv4", "127.0.0.1:1024", 1024, false},
		{"IPv6 with address", "[2001:db8::1]:8080", 8080, false},
		{"Empty string", "", 0, true},
		{"Invalid IPv4 format", "invalid", 0, true},
		{"IPv4 no colon", "32770", 0, true},
		{"IPv6 malformed", "[::32771", 0, true},
		{"IPv6 no port", "[::]", 0, true},
		{"Non-numeric port IPv4", "0.0.0.0:abc", 0, true},
		{"Non-numeric port IPv6", "[::]:xyz", 0, true},
		{"Multiple colons IPv4", "0.0.0.0:80:443", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseDockerPortMapping(tt.input)

			if tt.hasError {
				if err == nil {
					t.Errorf("Expected error for input %q, but got none", tt.input)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error for input %q: %v", tt.input, err)
				return
			}

			if result != tt.expected {
				t.Errorf("Expected port %d for input %q, got %d", tt.expected, tt.input, result)
			}
		})
	}
}
