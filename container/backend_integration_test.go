package container

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestBackendIntegration tests basic container operations with the configured backend
func TestBackendIntegration(t *testing.T) {
	SkipIfShort(t)

	backend := GetTestBackend(t)
	manager := CreateTestManager(t, backend)
	defer manager.Close()

	ctx := t.Context()
	allocID := "test-alloc"
	ipRange := WithAllocIPRange(t, allocID)

	t.Run("CreateAndDeleteContainer", func(t *testing.T) {
		req := &CreateContainerRequest{
			AllocID: allocID,
			IPRange: ipRange,
			Name:    fmt.Sprintf("test-%d", time.Now().UnixNano()),
			Image:   "alpine:latest",
		}

		container, err := manager.CreateContainer(ctx, req)
		if err != nil {
			t.Fatalf("Failed to create container: %v", err)
		}

		t.Logf("Created container: %s (backend: %s)", container.ID, backend.Backend)

		// Verify container exists
		retrieved, err := manager.GetContainer(ctx, allocID, container.ID)
		if err != nil {
			t.Errorf("Failed to get container: %v", err)
		}
		if retrieved.ID != container.ID {
			t.Errorf("Container ID mismatch: got %s, want %s", retrieved.ID, container.ID)
		}

		// Clean up
		CleanupContainer(t, manager, allocID, container.ID)
	})

	t.Run("ListContainers", func(t *testing.T) {
		// Create multiple containers
		var containers []*Container
		for i := 0; i < 3; i++ {
			req := &CreateContainerRequest{
				AllocID: allocID,
				IPRange: ipRange,
				Name:    fmt.Sprintf("list-test-%d-%d", i, time.Now().UnixNano()),
				Image:   "alpine:latest",
			}

			container, err := manager.CreateContainer(ctx, req)
			if err != nil {
				t.Fatalf("Failed to create container %d: %v", i, err)
			}
			containers = append(containers, container)
			defer CleanupContainer(t, manager, allocID, container.ID)
		}

		// List containers
		listed, err := manager.ListContainers(ctx, allocID)
		if err != nil {
			t.Fatalf("Failed to list containers: %v", err)
		}

		if len(listed) < len(containers) {
			t.Errorf("Expected at least %d containers, got %d", len(containers), len(listed))
		}

		// Verify our containers are in the list
		for _, created := range containers {
			found := false
			for _, listed := range listed {
				if listed.ID == created.ID {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Container %s not found in list", created.ID)
			}
		}
	})

	t.Run("StartStopContainer", func(t *testing.T) {
		req := &CreateContainerRequest{
			AllocID:         allocID,
			IPRange:         ipRange,
			Name:            fmt.Sprintf("startstop-%d", time.Now().UnixNano()),
			Image:           "alpine:latest",
			CommandOverride: "sleep 3600", // Long-running command
		}

		container, err := manager.CreateContainer(ctx, req)
		if err != nil {
			t.Fatalf("Failed to create container: %v", err)
		}
		defer CleanupContainer(t, manager, allocID, container.ID)

		// Container should be running initially
		WaitForContainerReady(t, manager, allocID, container.ID, 10*time.Second)

		// Stop the container
		if err := manager.StopContainer(ctx, allocID, container.ID); err != nil {
			t.Errorf("Failed to stop container: %v", err)
		}

		// Wait for stopped state
		time.Sleep(2 * time.Second)

		// Check status
		stopped, err := manager.GetContainer(ctx, allocID, container.ID)
		if err != nil {
			t.Errorf("Failed to get container after stop: %v", err)
		}
		if stopped.Status == StatusRunning {
			t.Errorf("Container should be stopped, but status is %v", stopped.Status)
		}

		// Start the container again
		if err := manager.StartContainer(ctx, allocID, container.ID); err != nil {
			t.Errorf("Failed to start container: %v", err)
		}

		// Wait for running state
		WaitForContainerReady(t, manager, allocID, container.ID, 10*time.Second)
	})

	t.Run("ExecuteCommand", func(t *testing.T) {
		req := &CreateContainerRequest{
			AllocID: allocID,
			IPRange: ipRange,
			Name:    fmt.Sprintf("exec-%d", time.Now().UnixNano()),
			Image:   "alpine:latest",
		}

		container, err := manager.CreateContainer(ctx, req)
		if err != nil {
			t.Fatalf("Failed to create container: %v", err)
		}
		defer CleanupContainer(t, manager, allocID, container.ID)

		// Wait for container to be ready
		WaitForContainerReady(t, manager, allocID, container.ID, 10*time.Second)

		// Execute a simple command
		var stdout bytes.Buffer
		err = manager.ExecuteInContainer(ctx, allocID, container.ID,
			[]string{"echo", "hello from", backend.Backend},
			nil, &stdout, nil)
		if err != nil {
			t.Errorf("Failed to execute command: %v", err)
		}

		output := stdout.String()
		expectedOutput := fmt.Sprintf("hello from %s", backend.Backend)
		if !strings.Contains(output, expectedOutput) {
			t.Errorf("Expected output to contain '%s', got: %s", expectedOutput, output)
		}
	})
}

// TestCTRHostEnvironment specifically tests CTR_HOST environment variable
func TestCTRHostEnvironment(t *testing.T) {
	SkipIfShort(t)

	// Save original env var
	originalCTRHost := os.Getenv("CTR_HOST")
	defer os.Setenv("CTR_HOST", originalCTRHost)

	testCases := []struct {
		name     string
		ctrHost  string
		expected string
	}{
		{
			name:     "SSH format",
			ctrHost:  "ssh://user@host",
			expected: "user@host",
		},
		{
			name:     "Direct host",
			ctrHost:  "user@host",
			expected: "user@host",
		},
		{
			name:     "Local socket",
			ctrHost:  "/var/run/containerd.sock",
			expected: "/var/run/containerd.sock",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			os.Setenv("CTR_HOST", tc.ctrHost)

			backend := GetTestBackend(t)

			if backend.Backend != "containerd" {
				t.Errorf("Expected containerd backend, got %s", backend.Backend)
			}

			if len(backend.Hosts) > 0 {
				if backend.Hosts[0] != tc.expected {
					t.Errorf("Expected host %s, got %s", tc.expected, backend.Hosts[0])
				}
			} else if tc.expected != "" {
				t.Errorf("Expected host %s, got empty", tc.expected)
			}
		})
	}
}

// TestRemoteContainerdSSH tests remote containerd access via SSH
func TestRemoteContainerdSSH(t *testing.T) {
	SkipIfShort(t)

	// This test requires CTR_HOST to be set to a remote host
	ctrHost := os.Getenv("CTR_HOST")
	if ctrHost == "" || strings.HasPrefix(ctrHost, "/") {
		t.Skip("Test requires CTR_HOST to be set to a remote host (e.g., ssh://exe-docker-12)")
	}

	backend := GetTestBackend(t)
	if backend.Backend != "containerd" {
		t.Skip("Test requires containerd backend")
	}

	manager := CreateTestManager(t, backend)
	defer manager.Close()

	ctx := t.Context()

	// Determine per-alloc IP range using the same policy space as production
	// by inspecting existing nerdctl networks on the remote host and picking
	// the first free /24 in 10.42.0.0/16..10.99.255.0/24.
	ipRange := WithAllocIPRange(t, "remote-test")

	// Create a container on the remote host with explicit per-alloc subnet
	req := &CreateContainerRequest{
		AllocID: "remote-test",
		IPRange: ipRange,
		Name:    fmt.Sprintf("remote-%d", time.Now().UnixNano()),
		Image:   "alpine:latest",
	}

	container, err := manager.CreateContainer(ctx, req)
	if err != nil {
		// Check if it's a connection error
		if strings.Contains(err.Error(), "255") || strings.Contains(err.Error(), "connection") || strings.Contains(err.Error(), "ssh") {
			t.Skipf("Cannot connect to remote host %s: %v", ctrHost, err)
		}
		t.Fatalf("Failed to create container on remote host %s: %v", ctrHost, err)
	}
	defer CleanupContainer(t, manager, "remote-test", container.ID)

	t.Logf("Successfully created container %s on remote host %s", container.ID, ctrHost)

	// Execute a command to verify it's working
	var stdout bytes.Buffer
	err = manager.ExecuteInContainer(ctx, "remote-test", container.ID,
		[]string{"hostname"},
		nil, &stdout, nil)
	if err != nil {
		t.Errorf("Failed to execute command on remote container: %v", err)
	}

	t.Logf("Remote container hostname: %s", strings.TrimSpace(stdout.String()))
}
