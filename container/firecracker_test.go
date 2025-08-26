package container

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestFirecrackerContainerStartupDelay verifies that we properly wait for Firecracker
// VMs to fully boot before attempting to exec into them.
func TestFirecrackerContainerStartupDelay(t *testing.T) {
	// Skip if not using Firecracker
	if os.Getenv("CTR_HOST") == "" {
		t.Skip("CTR_HOST not set, skipping Firecracker test")
	}
	
	// Create a nerdctl manager
	config := &Config{
		DockerHosts:          []string{os.Getenv("CTR_HOST")},
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "128Mi",
	}
	
	// Skip Kata check for testing
	os.Setenv("SKIP_KATA_CHECK", "true")
	defer os.Unsetenv("SKIP_KATA_CHECK")
	
	manager, err := NewNerdctlManager(config)
	if err != nil {
		t.Fatalf("Failed to create nerdctl manager: %v", err)
	}
	defer manager.Close()
	
	ctx := context.Background()
	
	// Create a container
	req := &CreateContainerRequest{
		AllocID:       "test-firecracker-" + fmt.Sprintf("%d", time.Now().Unix()),
		Name:          "fc-test",
		Image:         "ghcr.io/boldsoftware/exeuntu",
		CPURequest:    "100m",
		MemoryRequest: "256Mi",
	}
	
	container, err := manager.CreateContainer(ctx, req)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}
	
	// Clean up container when done
	defer func() {
		deleteCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := manager.DeleteContainer(deleteCtx, req.AllocID, container.ID); err != nil {
			t.Logf("Failed to delete container: %v", err)
		}
	}()
	
	// The container creation should succeed
	if container.ID == "" {
		t.Fatal("Container ID is empty")
	}
	
	// Wait a moment for the async SSH setup to start
	time.Sleep(2 * time.Second)
	
	// Now try to exec into the container directly to verify it's ready
	// This mimics what setupContainerSSH does
	host := manager.hosts[0]
	
	// Try to exec with proper timing for Firecracker
	var lastError error
	start := time.Now()
	for i := 0; i < 30; i++ {
		testCmd := manager.execNerdctl(ctx, host, "exec", "--user", "root", container.ID, "echo", "ready")
		output, err := testCmd.CombinedOutput()
		if err == nil && string(output) == "ready\n" {
			t.Logf("Container ready after %v", time.Since(start))
			return // Success!
		}
		lastError = err
		if err != nil {
			t.Logf("Attempt %d failed after %v: %v", i+1, time.Since(start), err)
			if len(output) > 0 {
				t.Logf("Output: %s", string(output))
			}
		}
		
		// Use appropriate backoff for Firecracker
		if i < 3 {
			time.Sleep(2 * time.Second) // Give Firecracker more time initially
		} else {
			time.Sleep(3 * time.Second)
		}
	}
	
	t.Fatalf("Container never became ready after %v: %v", time.Since(start), lastError)
}