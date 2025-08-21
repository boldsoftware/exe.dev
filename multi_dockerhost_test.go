package exe

import (
	"os"
	"testing"

	"exe.dev/container"
)

// TestMultiDockerHostSSH tests SSH connectivity with multiple docker hosts
// This test verifies the fix for multi-dockerhost SSH routing
func TestMultiDockerHostSSH(t *testing.T) {
	// This test verifies the fix for multi-dockerhost SSH routing

	// Create test server with database
	server, tempDB := setupTestServerWithDB(t)
	defer tempDB.Close()
	defer os.Remove(tempDB.Name())

	// Test docker host values
	dockerHost := "tcp://dockerhost1:2376"
	userID := "test-user-id"
	teamName := "testteam"
	machineName := "testmachine"
	containerID := "test-container-id"
	image := "ubuntu:latest"

	// Create test user
	err := server.createUser(userID, "test@example.com")
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create SSH keys for testing
	sshKeys, err := container.GenerateContainerSSHKeys()
	if err != nil {
		t.Fatalf("Failed to generate SSH keys: %v", err)
	}

	// Store machine with docker host using the NEW method
	err = server.createMachineWithSSHAndDockerHost(
		userID, teamName, machineName, containerID, image, dockerHost,
		sshKeys, 2222,
	)
	if err != nil {
		t.Fatalf("Failed to store machine with docker host: %v", err)
	}

	// Verify the fix: machine should retain docker host info
	machine, err := server.getMachineByName(teamName, machineName)
	if err != nil {
		t.Fatalf("Failed to retrieve machine: %v", err)
	}

	if machine.DockerHost == nil {
		t.Fatal("FIXED: Machine now properly stores DockerHost")
	}

	if *machine.DockerHost != dockerHost {
		t.Errorf("Expected docker host %s, got %s", dockerHost, *machine.DockerHost)
	}

	// Verify SSH details include docker host
	sshDetails, err := server.GetMachineSSHDetails(machine.ID)
	if err != nil {
		t.Fatalf("Failed to get SSH details: %v", err)
	}

	if sshDetails.DockerHost == nil || *sshDetails.DockerHost != dockerHost {
		t.Error("SSH details should include docker host information")
	}

	t.Logf("✅ Multi-dockerhost SSH routing now works - docker host preserved: %s", *sshDetails.DockerHost)
}

// TestDockerHostPersistence tests that docker host information is persisted in the database
func TestDockerHostPersistence(t *testing.T) {
	// This test verifies that docker host information is now properly stored

	// Create test server with database
	server, tempDB := setupTestServerWithDB(t)
	defer tempDB.Close()
	defer os.Remove(tempDB.Name())

	dockerHost := "tcp://production-docker:2376"
	userID := "test-user-fp"
	teamName := "prodteam"
	machineName := "prodmachine"
	containerID := "prod-container"
	image := "alpine:latest"

	// Create test user
	err := server.createUser(userID, "test@example.com")
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Generate SSH keys
	sshKeys, err := container.GenerateContainerSSHKeys()
	if err != nil {
		t.Fatalf("Failed to generate SSH keys: %v", err)
	}

	// 1. Container created on specific docker host
	// 2. Machine record stored in DB with docker host info
	err = server.createMachineWithSSHAndDockerHost(
		userID, teamName, machineName, containerID, image, dockerHost,
		sshKeys, 2222,
	)
	if err != nil {
		t.Fatalf("Failed to create machine with docker host: %v", err)
	}

	// 3. Machine retrieved from DB with docker host info intact
	machine, err := server.getMachineByName(teamName, machineName)
	if err != nil {
		t.Fatalf("Failed to retrieve machine: %v", err)
	}

	// Verify docker host persistence
	if machine.DockerHost == nil {
		t.Fatal("Docker host should be persisted in database")
	}
	if *machine.DockerHost != dockerHost {
		t.Errorf("Expected docker host %s, got %s", dockerHost, *machine.DockerHost)
	}

	// 4. SSH routing uses correct docker host
	sshDetails, err := server.GetMachineSSHDetails(machine.ID)
	if err != nil {
		t.Fatalf("Failed to get SSH details: %v", err)
	}

	if sshDetails.DockerHost == nil || *sshDetails.DockerHost != dockerHost {
		t.Fatal("SSH routing should have access to docker host information")
	}

	t.Logf("✅ All steps work correctly - docker host persisted and available for SSH routing")
}
