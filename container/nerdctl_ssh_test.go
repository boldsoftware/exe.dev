package container

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	
	"golang.org/x/crypto/ssh"
)

// TestNerdctlSSHConnectivity tests that SSH can connect to containers created with nerdctl
func TestNerdctlSSHConnectivity(t *testing.T) {
	// Skip if not in the right environment
	if os.Getenv("CTR_HOST") == "" {
		t.Skip("CTR_HOST not set, skipping nerdctl SSH test")
	}

	config := &Config{
		DockerHosts:          []string{os.Getenv("CTR_HOST")},
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "128Mi",
	}

	t.Log("Creating nerdctl manager...")
	manager, err := NewNerdctlManager(config)
	if err != nil {
		t.Fatalf("failed to create nerdctl manager: %v", err)
	}
	t.Log("Nerdctl manager created successfully")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create a test container
	t.Log("Creating test container request...")
	// Use a unique AllocID to avoid network conflicts
	uniqueID := fmt.Sprintf("test-ssh-%d", time.Now().UnixNano())
	req := &CreateContainerRequest{
		AllocID:         uniqueID,
		Name:            "sshtest",
		Image:           "ubuntu:latest",
		CommandOverride: "",
	}

	t.Logf("Calling CreateContainer with request: AllocID=%s, Name=%s, Image=%s", 
		req.AllocID, req.Name, req.Image)
	container, err := manager.CreateContainer(ctx, req)
	if err != nil {
		t.Fatalf("failed to create container: %v", err)
	}
	t.Logf("Container created successfully: ID=%s, SSHPort=%d", container.ID, container.SSHPort)
	defer func() {
		// Clean up container
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer deleteCancel()
		if err := manager.DeleteContainer(deleteCtx, req.AllocID, container.ID); err != nil {
			t.Logf("warning: failed to delete container: %v", err)
		}
	}()

	// Give SSH some time to start
	time.Sleep(10 * time.Second)

	// Test SSH connectivity
	t.Run("DirectSSHConnection", func(t *testing.T) {
		// Parse the client private key
		signer, err := ssh.ParsePrivateKey([]byte(container.SSHClientPrivateKey))
		if err != nil {
			t.Fatalf("failed to parse SSH private key: %v", err)
		}

		// SSH client configuration
		sshConfig := &ssh.ClientConfig{
			User: "root",
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(signer),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         10 * time.Second,
		}

		// Connect to container SSH port
		// For nerdctl with remote host, we need to use the tunnel
		sshAddr := ""
		if strings.HasPrefix(container.DockerHost, "ssh://") {
			// SSH tunnel should be on localhost
			sshAddr = fmt.Sprintf("localhost:%d", container.SSHPort)
		} else {
			// Local container
			sshAddr = fmt.Sprintf("localhost:%d", container.SSHPort)
		}

		client, err := ssh.Dial("tcp", sshAddr, sshConfig)
		if err != nil {
			t.Fatalf("failed to connect via SSH: %v", err)
		}
		defer client.Close()

		// Run a simple command
		session, err := client.NewSession()
		if err != nil {
			t.Fatalf("failed to create SSH session: %v", err)
		}
		defer session.Close()

		output, err := session.Output("echo 'SSH test successful'")
		if err != nil {
			t.Fatalf("failed to run command via SSH: %v", err)
		}

		expectedOutput := "SSH test successful\n"
		if string(output) != expectedOutput {
			t.Errorf("unexpected output: got %q, want %q", string(output), expectedOutput)
		}
	})
	
	// Test that authorized_keys is properly set
	t.Run("AuthorizedKeysSetup", func(t *testing.T) {
		// Check that authorized_keys was set during creation
		if container.SSHAuthorizedKeys == "" {
			t.Error("SSHAuthorizedKeys is empty")
		}
		
		// Check that it contains the expected public key format
		if !strings.Contains(container.SSHAuthorizedKeys, "ssh-ed25519") {
			t.Errorf("authorized_keys does not contain ed25519 key: %s", container.SSHAuthorizedKeys)
		}
	})
	
	// Test that SSH keys are properly generated
	t.Run("SSHKeysGenerated", func(t *testing.T) {
		// Check that all SSH key fields are populated
		if container.SSHServerIdentityKey == "" {
			t.Error("SSHServerIdentityKey is empty")
		}
		if container.SSHClientPrivateKey == "" {
			t.Error("SSHClientPrivateKey is empty")
		}
		if container.SSHPort == 0 {
			t.Error("SSHPort is not set")
		}
	})
}