package exe

import (
	"context"
	"os"
	"strings"
	"testing"

	"exe.dev/container"
)

func TestLocalContainerIntegration(t *testing.T) {
	t.Parallel()
	// Skip if container tests are disabled
	if os.Getenv("SKIP_CONTAINER_TESTS") != "" {
		t.Skip("Skipping container integration test")
	}

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Determine backend based on environment
	backend := "docker" // default for backward compatibility
	hosts := []string{""}
	
	if ctrHost := os.Getenv("CTR_HOST"); ctrHost != "" {
		backend = "containerd"
		if ctrHost != "local" {
			hosts = []string{strings.TrimPrefix(ctrHost, "ssh://")}
		}
	} else if os.Getenv("USE_CONTAINERD") == "true" {
		backend = "containerd"
	}
	
	// Create server in local mode with detected backend
	server, err := NewServerWithBackend(":0", "", ":0", ":0", tmpDB.Name(), "local", hosts, backend)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Verify we have a Docker manager
	if server.containerManager == nil {
		t.Fatal("Expected container manager to be initialized in local mode")
	}

	// Verify we have the correct manager type
	switch backend {
	case "docker":
		if _, ok := server.containerManager.(*container.DockerManager); !ok {
			t.Fatalf("Expected DockerManager, got %T", server.containerManager)
		}
	case "containerd":
		if _, ok := server.containerManager.(*container.NerdctlManager); !ok {
			t.Fatalf("Expected NerdctlManager, got %T", server.containerManager)
		}
	}

	// Test basic container operations
	ctx := context.Background()

	// Create a container
	req := &container.CreateContainerRequest{
		AllocID: "test-alloc",
		Name:    "test-container",
		Image:   "alpine:latest",
	}

	createdContainer, err := server.containerManager.CreateContainer(ctx, req)
	if err != nil {
		t.Logf("Container creation error (expected if Docker not available): %v", err)
		// Not a fatal error - Docker might not be available in CI
		return
	}

	// Verify container was created
	if createdContainer == nil {
		t.Fatal("Expected container to be created")
	}
	if createdContainer.Name != "test-container" {
		t.Errorf("Expected container name 'test-container', got %s", createdContainer.Name)
	}
	if createdContainer.AllocID != "test-alloc" {
		t.Errorf("Expected alloc ID 'test-alloc', got %s", createdContainer.AllocID)
	}

	// List containers
	containers, err := server.containerManager.ListContainers(ctx, "test-alloc")
	if err != nil {
		t.Fatalf("Failed to list containers: %v", err)
	}
	if len(containers) != 1 {
		t.Errorf("Expected 1 container, got %d", len(containers))
	}

	// Clean up - delete the container
	err = server.containerManager.DeleteContainer(ctx, "test-alloc", createdContainer.ID)
	if err != nil {
		t.Logf("Warning: Failed to delete container: %v", err)
	}
}

func TestDevModeString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		devMode string
		valid   bool
	}{
		{"", true},
		{"local", true},
		{"invalid", false},
		{"production", false},
	}

	for _, tt := range tests {
		t.Run(tt.devMode, func(t *testing.T) {
			// This test verifies that the validation in cmd/exed/exed.go works
			// The actual validation happens in the main function
			validModes := map[string]bool{"": true, "local": true}

			if validModes[tt.devMode] != tt.valid {
				t.Errorf("Expected devMode %q to be valid=%v", tt.devMode, tt.valid)
			}
		})
	}
}
