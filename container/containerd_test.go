package container

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestContainerdManagerCreation(t *testing.T) {
	// Skip if CTR_HOST is not set (e2e test requires containerd)
	if os.Getenv("CTR_HOST") == "" {
		t.Skip("CTR_HOST not set, skipping e2e container test")
	}

	cfg := &Config{
		ContainerdAddresses:  []string{os.Getenv("CTR_HOST")},
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "256Mi",
		DefaultStorageSize:   "1Gi",
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("Failed to create containerd manager: %v", err)
	}

	if _, ok := manager.(*NerdctlManager); !ok {
		t.Fatal("Expected NerdctlManager instance")
	}
}

// TestContainerdIntegration tests basic containerd operations if containerd is available
func TestContainerdIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Skip if CTR_HOST is not set (e2e test requires containerd)
	if os.Getenv("CTR_HOST") == "" {
		t.Skip("CTR_HOST not set, skipping e2e container test")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	cfg := &Config{
		ContainerdAddresses:  []string{os.Getenv("CTR_HOST")},
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "256Mi",
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("Failed to create containerd manager: %v", err)
	}
	defer manager.Close()

	// Test listing containers (should not fail even if empty)
	containers, err := manager.ListContainers(ctx, "test-alloc")
	if err != nil {
		t.Fatalf("Failed to list containers: %v", err)
	}

	t.Logf("Found %d containers", len(containers))
}

func checkContainerdAvailable(ctx context.Context) error {
	// Try to run ctr version to check if containerd is available
	cmd := exec.CommandContext(ctx, "ctr", "version")
	return cmd.Run()
}
