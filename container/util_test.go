package container

import (
	"context"
	"math/rand"
	"os/exec"
	"testing"
	"time"

	"exe.dev/ctrhosttest"
)

// CreateTestManager creates a NerdctlManager instance.
func CreateTestManager(t *testing.T) *NerdctlManager {
	ctrHost := ctrhosttest.Detect()
	if ctrHost == "" {
		t.Skip("cannot reach a ctr-host (set CTR_HOST or run ./ops/setup-lima-hosts.sh)")
	}
	config := &Config{
		ContainerdAddresses:  []string{ctrHost},
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "256Mi",
		DefaultStorageSize:   "1Gi",
	}
	manager, err := NewNerdctlManager(config)
	if err != nil {
		t.Fatalf("Failed to create containerd manager: %v", err)
	}

	return manager
}

// isCommandAvailable checks if a command is available in PATH
func isCommandAvailable(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

// SkipIfShort skips the test if running in short mode
func SkipIfShort(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
}

// CleanupContainer ensures a container is deleted after a test
func CleanupContainer(t *testing.T, manager *NerdctlManager, allocID, containerID string) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	if err := manager.DeleteContainer(ctx, allocID, containerID); err != nil {
		t.Logf("Warning: Failed to delete container %s: %v", containerID, err)
	}
}

// WaitForContainerReady waits for a container to be in running state
func WaitForContainerReady(t *testing.T, manager *NerdctlManager, allocID, containerID string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()

	start := time.Now()
	for {
		container, err := manager.GetContainer(ctx, allocID, containerID)
		if err == nil && container.Status == StatusRunning {
			t.Logf("Container %s ready after %v", containerID, time.Since(start))
			return
		}

		if time.Since(start) > timeout {
			t.Fatalf("Container %s not ready after %v", containerID, timeout)
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// GenerateTestBoxID generates a unique BoxID for testing purposes.
// It uses timestamp and random components to ensure uniqueness across
// parallel test runs.
func GenerateTestBoxID() int {
	// Use timestamp (last 9 digits of nanoseconds) + random component (0-999)
	// This ensures uniqueness even when tests run in parallel
	return int(time.Now().UnixNano()%1000000000) + rand.Intn(1000)
}
