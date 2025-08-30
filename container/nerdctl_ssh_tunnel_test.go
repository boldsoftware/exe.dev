package container

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestNerdctlSSHTunnel tests SSH tunnel setup for remote containerd hosts
func TestNerdctlSSHTunnel(t *testing.T) {
	// Skip if not in CI or no remote host configured
	remoteHost := os.Getenv("CTR_HOST")
	if remoteHost == "" {
		t.Skip("CTR_HOST not set, skipping SSH tunnel test")
	}

	// Parse the host to ensure it's remote (has ssh:// prefix)
	if !strings.HasPrefix(remoteHost, "ssh://") {
		t.Skip("CTR_HOST is not a remote SSH host, skipping SSH tunnel test")
	}

	// Always use sudo for remote commands
	t.Log("Using sudo for remote containerd commands")

	// Create a test config
	config := &Config{
		ContainerdAddresses:  []string{remoteHost},
		DefaultMemoryRequest: "256Mi",
		DefaultCPURequest:    "100m",
	}

	// Create nerdctl manager
	manager, err := NewNerdctlManager(config)
	if err != nil {
		t.Fatalf("Failed to create nerdctl manager: %v", err)
	}
	defer manager.Close()

	// Create a test container
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	req := &CreateContainerRequest{
		AllocID: "test-alloc-" + fmt.Sprintf("%d", time.Now().Unix()),
		Name:    "test-ssh-tunnel",
		Image:   "ubuntu:latest",
	}

	container, err := manager.CreateContainer(ctx, req)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}
	defer func() {
		// Clean up: delete the container
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer deleteCancel()
		if err := manager.DeleteContainer(deleteCtx, req.AllocID, container.ID); err != nil {
			t.Logf("Warning: Failed to delete test container: %v", err)
		}
	}()

	// Check that SSH port was allocated
	if container.SSHPort == 0 {
		t.Fatal("Container SSH port was not allocated")
	}

	t.Logf("Container created with SSH port: %d", container.SSHPort)

	// Check if SSH tunnel was created (only for remote hosts)
	host := container.DockerHost
	if host != "" && !strings.HasPrefix(host, "/") {
		// Give the tunnel a moment to establish
		time.Sleep(2 * time.Second)

		// Check if we can connect to the local port
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", container.SSHPort), 5*time.Second)
		if err != nil {
			// Check if the tunnel process exists
			manager.mu.RLock()
			tunnel, exists := manager.sshTunnels[container.ID]
			manager.mu.RUnlock()

			if !exists {
				t.Fatal("SSH tunnel was not created for remote container")
			}

			// Check if tunnel process is still running
			if tunnel.ProcessState != nil && tunnel.ProcessState.Exited() {
				t.Fatalf("SSH tunnel process exited unexpectedly")
			}

			t.Logf("Warning: Could not connect to localhost:%d - tunnel may still be establishing: %v", container.SSHPort, err)
		} else {
			conn.Close()
			t.Logf("Successfully connected to SSH tunnel on localhost:%d", container.SSHPort)
		}

		// Test that the tunnel is in the map
		manager.mu.RLock()
		tunnel, exists := manager.sshTunnels[container.ID]
		manager.mu.RUnlock()

		if !exists {
			t.Fatal("SSH tunnel not found in manager's tunnel map")
		}

		// Verify the tunnel command is correct
		if tunnel == nil {
			t.Fatal("SSH tunnel command is nil")
		}

		// Check the tunnel command arguments
		expectedArgs := []string{
			"-N",
			"-L", fmt.Sprintf("%d:localhost:%d", container.SSHPort, container.SSHPort),
		}

		args := tunnel.Args
		for _, expected := range expectedArgs {
			found := false
			for _, arg := range args {
				if arg == expected {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Expected SSH tunnel argument %q not found in %v", expected, args)
			}
		}

		t.Log("SSH tunnel setup verified successfully")
	}

	// Test container stop/start to ensure tunnel is managed correctly
	t.Log("Testing container stop/start cycle...")

	if err := manager.StopContainer(ctx, req.AllocID, container.ID); err != nil {
		t.Fatalf("Failed to stop container: %v", err)
	}

	// Give it a moment
	time.Sleep(1 * time.Second)

	if err := manager.StartContainer(ctx, req.AllocID, container.ID); err != nil {
		t.Fatalf("Failed to start container: %v", err)
	}

	// Check tunnel was re-established for remote hosts
	if host != "" && !strings.HasPrefix(host, "/") {
		time.Sleep(2 * time.Second)

		manager.mu.RLock()
		tunnel, exists := manager.sshTunnels[container.ID]
		manager.mu.RUnlock()

		if !exists {
			t.Error("SSH tunnel was not re-established after container restart")
		} else if tunnel == nil {
			t.Error("SSH tunnel command is nil after restart")
		} else {
			t.Log("SSH tunnel successfully re-established after container restart")
		}
	}
}

// TestSSHTunnelCleanup tests that SSH tunnels are properly cleaned up
func TestSSHTunnelCleanup(t *testing.T) {
	// This test verifies tunnel cleanup on container deletion
	remoteHost := os.Getenv("CTR_HOST")
	if remoteHost == "" || !strings.HasPrefix(remoteHost, "ssh://") {
		t.Skip("CTR_HOST not set to remote SSH host, skipping cleanup test")
	}

	config := &Config{
		ContainerdAddresses: []string{remoteHost},
	}

	manager, err := NewNerdctlManager(config)
	if err != nil {
		t.Fatalf("Failed to create nerdctl manager: %v", err)
	}

	// Create and delete multiple containers to test cleanup
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		req := &CreateContainerRequest{
			AllocID: fmt.Sprintf("test-cleanup-%d-%d", i, time.Now().Unix()),
			Name:    fmt.Sprintf("cleanup-test-%d", i),
			Image:   "alpine:latest",
		}

		container, err := manager.CreateContainer(ctx, req)
		if err != nil {
			t.Fatalf("Failed to create container %d: %v", i, err)
		}

		// Verify tunnel exists
		manager.mu.RLock()
		_, exists := manager.sshTunnels[container.ID]
		manager.mu.RUnlock()

		if !exists && container.DockerHost != "" {
			t.Errorf("Tunnel not created for container %d", i)
		}

		// Delete container
		if err := manager.DeleteContainer(ctx, req.AllocID, container.ID); err != nil {
			t.Errorf("Failed to delete container %d: %v", i, err)
		}

		// Verify tunnel is cleaned up
		manager.mu.RLock()
		_, exists = manager.sshTunnels[container.ID]
		manager.mu.RUnlock()

		if exists {
			t.Errorf("Tunnel not cleaned up for deleted container %d", i)
		}
	}

	// Test manager Close() cleans up all tunnels
	req := &CreateContainerRequest{
		AllocID: "test-close-" + fmt.Sprintf("%d", time.Now().Unix()),
		Name:    "close-test",
		Image:   "alpine:latest",
	}

	container, err := manager.CreateContainer(ctx, req)
	if err == nil {
		defer func() {
			// Try to clean up even if Close() fails
			exec.Command("ssh", strings.TrimPrefix(remoteHost, "ssh://"),
				"sudo", "nerdctl", "--namespace", "exe", "rm", "-f", container.ID).Run()
		}()

		// Close manager (should clean up tunnel)
		if err := manager.Close(); err != nil {
			t.Errorf("Manager Close() failed: %v", err)
		}

		// Verify all tunnels are gone
		manager.mu.RLock()
		tunnelCount := len(manager.sshTunnels)
		manager.mu.RUnlock()

		if tunnelCount > 0 {
			t.Errorf("Manager Close() did not clean up all tunnels, %d remaining", tunnelCount)
		}
	}
}
