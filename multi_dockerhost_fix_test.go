package exe

import (
	"testing"
)

// TestMultiDockerHostSchemaCompatibility tests backward compatibility with existing machines
func TestMultiDockerHostSchemaCompatibility(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Create a machine without docker host (legacy case)
	userID := "test-user-id"
	allocID := "test-alloc"
	machineName := "legacymachine"
	containerID := "legacy-container-id"
	image := "ubuntu:latest"

	// Create test user and alloc
	err := server.createUser(userID, "test@example.com")
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create alloc without docker host
	_, err = server.db.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, created_at)
		VALUES (?, ?, 'medium', 'aws-us-west-2', datetime('now'))`, allocID, userID)
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Use the old method without docker host
	err = server.createMachine(userID, allocID, machineName, containerID, image)
	if err != nil {
		t.Fatalf("Failed to create legacy machine: %v", err)
	}

	// Retrieve machine from database (globally unique name now)
	machine, err := server.getMachineByName(machineName)
	if err != nil {
		t.Fatalf("Failed to retrieve legacy machine: %v", err)
	}

	// Legacy machine should have nil docker host
	if machine.DockerHost != nil {
		t.Errorf("Legacy machine should have nil DockerHost, got %v", *machine.DockerHost)
	}

	t.Logf("✅ Legacy machine compatibility verified - DockerHost is nil as expected")
}
