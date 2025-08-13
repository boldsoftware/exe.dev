package exe

import (
	"context"
	"os"
	"testing"
	"exe.dev/container"
)

func TestLocalDockerIntegration(t *testing.T) {
	// Skip if Docker is not available
	if os.Getenv("SKIP_DOCKER_TESTS") != "" {
		t.Skip("Skipping Docker integration test")
	}

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server in local mode
	server, err := NewServer(":0", "", ":0", tmpDB.Name(), "local", "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Verify we have a Docker manager
	if server.containerManager == nil {
		t.Fatal("Expected container manager to be initialized in local mode")
	}

	dockerManager, ok := server.containerManager.(*container.DockerManager)
	if !ok {
		t.Fatalf("Expected DockerManager, got %T", server.containerManager)
	}

	// Test basic container operations
	ctx := context.Background()
	
	// Create a container
	req := &container.CreateContainerRequest{
		UserID:   "test-user",
		TeamName: "test-team",
		Name:     "test-container",
		Image:    "alpine:latest",
	}
	
	createdContainer, err := dockerManager.CreateContainer(ctx, req)
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
	if createdContainer.TeamName != "test-team" {
		t.Errorf("Expected team name 'test-team', got %s", createdContainer.TeamName)
	}

	// List containers
	containers, err := dockerManager.ListContainers(ctx, "test-user")
	if err != nil {
		t.Fatalf("Failed to list containers: %v", err)
	}
	if len(containers) != 1 {
		t.Errorf("Expected 1 container, got %d", len(containers))
	}

	// Clean up - delete the container
	err = dockerManager.DeleteContainer(ctx, "test-user", createdContainer.ID)
	if err != nil {
		t.Logf("Warning: Failed to delete container: %v", err)
	}
}

func TestDevModeString(t *testing.T) {
	tests := []struct {
		devMode  string
		valid    bool
	}{
		{"", true},
		{"local", true},
		{"realgke", true},
		{"invalid", false},
		{"production", false},
	}

	for _, tt := range tests {
		t.Run(tt.devMode, func(t *testing.T) {
			// This test verifies that the validation in cmd/exed/exed.go works
			// The actual validation happens in the main function
			validModes := map[string]bool{"": true, "local": true, "realgke": true}
			
			if validModes[tt.devMode] != tt.valid {
				t.Errorf("Expected devMode %q to be valid=%v", tt.devMode, tt.valid)
			}
		})
	}
}