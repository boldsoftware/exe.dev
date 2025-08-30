package container

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestNerdctlRovolFS tests that RovolFS files are properly mounted into containers
func TestNerdctlRovolFS(t *testing.T) {
	// Skip if not running with containerd backend
	if os.Getenv("CTR_HOST") == "" {
		t.Skip("CTR_HOST not set, skipping nerdctl RovolFS test")
	}


	config := &Config{
		ContainerdAddresses:  []string{os.Getenv("CTR_HOST")},
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "128Mi",
	}

	manager, err := NewNerdctlManager(config)
	if err != nil {
		t.Fatalf("failed to create nerdctl manager: %v", err)
	}
	defer manager.Close()

	// Verify that RovolFS was prepared
	if manager.rovolMountPath == "" {
		t.Log("Warning: RovolFS was not prepared, continuing test without it")
	} else {
		t.Logf("RovolFS prepared at: %s", manager.rovolMountPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Create a test container
	req := &CreateContainerRequest{
		AllocID:         "test-rovol-" + time.Now().Format("20060102-150405"),
		Name:            "rovol-test",
		Image:           "ubuntu:latest",
		CommandOverride: "auto",
	}

	container, err := manager.CreateContainer(ctx, req)
	if err != nil {
		t.Fatalf("failed to create container: %v", err)
	}
	defer func() {
		deleteCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := manager.DeleteContainer(deleteCtx, req.AllocID, container.ID); err != nil {
			t.Errorf("failed to delete container: %v", err)
		}
	}()

	t.Logf("Created container: %s", container.ID)

	// Wait a bit for container to stabilize
	time.Sleep(2 * time.Second)

	// If RovolFS was mounted, check that /exe.dev exists and contains expected files
	if manager.rovolMountPath != "" {
		// Check if /exe.dev directory exists
		checkDirCmd := manager.execNerdctl(ctx, container.DockerHost, "exec", container.ID, "test", "-d", "/exe.dev")
		if err := checkDirCmd.Run(); err != nil {
			t.Errorf("/exe.dev directory does not exist in container")
		} else {
			t.Log("/exe.dev directory exists in container")
		}

		// Check if /exe.dev/bin/sshd exists
		checkSSHDCmd := manager.execNerdctl(ctx, container.DockerHost, "exec", container.ID, "test", "-f", "/exe.dev/bin/sshd")
		if err := checkSSHDCmd.Run(); err != nil {
			t.Logf("Warning: /exe.dev/bin/sshd does not exist in container (this might be expected if RovolFS is empty)")
		} else {
			t.Log("/exe.dev/bin/sshd exists in container")

			// Check if sshd is executable
			checkExecCmd := manager.execNerdctl(ctx, container.DockerHost, "exec", container.ID, "test", "-x", "/exe.dev/bin/sshd")
			if err := checkExecCmd.Run(); err != nil {
				t.Errorf("/exe.dev/bin/sshd is not executable")
			} else {
				t.Log("/exe.dev/bin/sshd is executable")
			}
		}

		// List contents of /exe.dev
		lsCmd := manager.execNerdctl(ctx, container.DockerHost, "exec", container.ID, "ls", "-la", "/exe.dev")
		output, err := lsCmd.Output()
		if err != nil {
			t.Logf("Warning: Failed to list /exe.dev contents: %v", err)
		} else {
			t.Logf("/exe.dev contents:\n%s", string(output))
		}

		// Check that /exe.dev is read-only
		testWriteCmd := manager.execNerdctl(ctx, container.DockerHost, "exec", container.ID, "sh", "-c", "touch /exe.dev/test-write 2>&1")
		output, err = testWriteCmd.Output()
		if err == nil {
			t.Errorf("/exe.dev should be read-only but write succeeded")
		} else if strings.Contains(string(output), "Read-only file system") {
			t.Log("/exe.dev is correctly mounted as read-only")
		} else {
			t.Logf("Unexpected error when testing write to /exe.dev: %v: %s", err, output)
		}
	}

	// Check container is running
	statusCmd := manager.execNerdctl(ctx, container.DockerHost, "inspect", container.ID, "--format", "{{.State.Status}}")
	statusOutput, err := statusCmd.Output()
	if err != nil {
		t.Fatalf("failed to get container status: %v", err)
	}
	status := strings.TrimSpace(string(statusOutput))
	if status != "running" {
		t.Errorf("container is not running, status: %s", status)
	}
}

// TestNerdctlRovolFSCleanup tests that RovolFS directories are properly managed
func TestNerdctlRovolFSCleanup(t *testing.T) {
	// Skip if not running with containerd backend
	if os.Getenv("CTR_HOST") == "" {
		t.Skip("CTR_HOST not set, skipping nerdctl RovolFS cleanup test")
	}


	config := &Config{
		ContainerdAddresses:  []string{os.Getenv("CTR_HOST")},
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "128Mi",
	}

	// Create first manager instance
	manager1, err := NewNerdctlManager(config)
	if err != nil {
		t.Fatalf("failed to create first nerdctl manager: %v", err)
	}

	rovolPath1 := manager1.rovolMountPath
	if rovolPath1 == "" {
		t.Skip("RovolFS was not prepared, skipping cleanup test")
	}

	t.Logf("First manager RovolFS path: %s", rovolPath1)
	manager1.Close()

	// Create second manager instance
	manager2, err := NewNerdctlManager(config)
	if err != nil {
		t.Fatalf("failed to create second nerdctl manager: %v", err)
	}
	defer manager2.Close()

	rovolPath2 := manager2.rovolMountPath
	if rovolPath2 == "" {
		t.Fatal("Second manager did not prepare RovolFS")
	}

	t.Logf("Second manager RovolFS path: %s", rovolPath2)

	// Paths should be different (each manager gets its own directory)
	if rovolPath1 == rovolPath2 {
		t.Errorf("Both managers are using the same RovolFS path: %s", rovolPath1)
	}

	// Both paths should exist on the host
	ctx := context.Background()
	host := config.ContainerdAddresses[0]

	for _, path := range []string{rovolPath1, rovolPath2} {
		var checkCmd *exec.Cmd
		if host != "" && !strings.HasPrefix(host, "/") {
			sshHost := host
			if strings.HasPrefix(sshHost, "ssh://") {
				sshHost = strings.TrimPrefix(sshHost, "ssh://")
			}
			// Always use sudo for remote commands
			checkCmd = exec.CommandContext(ctx, "ssh", sshHost, "sudo", "test", "-d", path)
		} else {
			checkCmd = exec.CommandContext(ctx, "test", "-d", path)
		}

		if err := checkCmd.Run(); err != nil {
			t.Errorf("RovolFS path %s does not exist on host", path)
		} else {
			t.Logf("RovolFS path %s exists on host", path)
		}
	}

	// Note: In production, you might want to add cleanup logic to remove old RovolFS directories
	// For now, we leave them to avoid affecting running containers
}