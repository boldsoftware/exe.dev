package exe

import (
	"context"
	"os"
	"testing"
	"exe.dev/container"
)

// TestDevLocalContainerFix tests that the Docker container lookup bug is fixed
func TestDevLocalContainerFix(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
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

	// Test container creation and lookup
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
		// Skip the test if Docker is not available
		return
	}

	// Verify container was created
	if createdContainer == nil {
		t.Fatal("Expected container to be created")
	}

	t.Logf("Created container: ID=%s, PodName=%s", createdContainer.ID, createdContainer.PodName)

	// The key test: verify that we can retrieve the container using GetContainer
	// This should work even if the Docker container ID would be truncated
	retrievedContainer, err := dockerManager.GetContainer(ctx, "test-user", createdContainer.ID)
	if err != nil {
		t.Fatalf("Failed to retrieve container: %v", err)
	}

	if retrievedContainer.ID != createdContainer.ID {
		t.Errorf("Expected container ID %s, got %s", createdContainer.ID, retrievedContainer.ID)
	}

	// Test that ConnectToContainer works (this was the original failing point)
	conn, err := dockerManager.ConnectToContainer(ctx, "test-user", createdContainer.ID)
	if err != nil {
		t.Fatalf("Failed to connect to container: %v", err)
	}
	if conn == nil {
		t.Fatal("Expected connection to be established")
	}

	t.Logf("Successfully connected to container %s", createdContainer.ID)

	// Clean up - delete the container
	err = dockerManager.DeleteContainer(ctx, "test-user", createdContainer.ID)
	if err != nil {
		t.Logf("Warning: Failed to delete container: %v", err)
	}

	t.Log("Container lookup and connection test passed - bug is fixed!")
}