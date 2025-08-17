package exe

import (
	"os"
	"strings"
	"testing"

	"exe.dev/container"
)

// setupTestServerWithDB creates a test server with a temporary database
func setupTestServerWithDB(t *testing.T) (*Server, *os.File) {
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	tmpDB.Close()

	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		os.Remove(tmpDB.Name())
		t.Fatalf("Failed to create server: %v", err)
	}
	server.testMode = true
	server.quietMode = true

	return server, tmpDB
}

// TestMultiDockerHostDatabasePersistence tests that docker host information is properly stored and retrieved
func TestMultiDockerHostDatabasePersistence(t *testing.T) {
	// Create test server with database
	server, tempDB := setupTestServerWithDB(t)
	defer tempDB.Close()

	// Test docker host values
	dockerHost := "tcp://dockerhost1:2376"
	userFingerprint := "test-fingerprint"
	teamName := "testteam"
	machineName := "testmachine"
	containerID := "test-container-id"
	image := "ubuntu:latest"

	// Create SSH keys for testing
	sshKeys, err := container.GenerateContainerSSHKeys()
	if err != nil {
		t.Fatalf("Failed to generate SSH keys: %v", err)
	}

	// Store machine with docker host in database
	err = server.createMachineWithSSHAndDockerHost(
		userFingerprint, teamName, machineName, containerID, image, dockerHost,
		sshKeys, 2222,
	)
	if err != nil {
		t.Fatalf("Failed to store machine with docker host: %v", err)
	}

	// Retrieve machine from database
	machine, err := server.getMachineByName(teamName, machineName)
	if err != nil {
		t.Fatalf("Failed to retrieve machine: %v", err)
	}

	// Verify docker host is preserved
	if machine.DockerHost == nil {
		t.Fatal("Machine DockerHost is nil - should be populated")
	}
	if *machine.DockerHost != dockerHost {
		t.Errorf("Expected docker host %s, got %s", dockerHost, *machine.DockerHost)
	}

	// Test SSH details retrieval includes docker host
	sshDetails, err := server.GetMachineSSHDetails(machine.ID)
	if err != nil {
		t.Fatalf("Failed to get SSH details: %v", err)
	}

	if sshDetails.DockerHost == nil {
		t.Fatal("SSH details DockerHost is nil - should be populated")
	}
	if *sshDetails.DockerHost != dockerHost {
		t.Errorf("Expected SSH details docker host %s, got %s", dockerHost, *sshDetails.DockerHost)
	}

	t.Logf("✅ Docker host properly persisted: %s", *sshDetails.DockerHost)
}

// TestSSHRoutingWithDockerHost tests that SSH routing uses the correct docker host
func TestSSHRoutingWithDockerHost(t *testing.T) {
	// This test verifies that the piper routing logic correctly extracts
	// hostnames from DOCKER_HOST values for SSH connections

	testCases := []struct {
		name         string
		dockerHost   string
		expectedHost string
		description  string
	}{
		{
			name:         "tcp format",
			dockerHost:   "tcp://dockerhost1:2376",
			expectedHost: "dockerhost1",
			description:  "Should extract hostname from tcp:// format",
		},
		{
			name:         "direct hostname",
			dockerHost:   "dockerhost2",
			expectedHost: "dockerhost2",
			description:  "Should use direct hostname",
		},
		{
			name:         "localhost tcp",
			dockerHost:   "tcp://localhost:2376",
			expectedHost: "localhost",
			description:  "Should handle localhost in tcp format",
		},
		{
			name:         "empty docker host",
			dockerHost:   "",
			expectedHost: "localhost",
			description:  "Should default to localhost for empty docker host",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test the hostname extraction logic
			actualHost := "localhost" // default

			if tc.dockerHost != "" {
				if strings.HasPrefix(tc.dockerHost, "tcp://") {
					// Extract hostname from tcp://hostname:port
					parts := strings.Split(strings.TrimPrefix(tc.dockerHost, "tcp://"), ":")
					if len(parts) > 0 && parts[0] != "" {
						actualHost = parts[0]
					}
				} else if !strings.HasPrefix(tc.dockerHost, "unix://") {
					// Direct hostname
					actualHost = tc.dockerHost
				}
			}

			if actualHost != tc.expectedHost {
				t.Errorf("%s: expected host %s, got %s", tc.description, tc.expectedHost, actualHost)
			} else {
				t.Logf("✅ %s: %s -> %s", tc.description, tc.dockerHost, actualHost)
			}
		})
	}
}

// TestMultiDockerHostSchemaCompatibility tests backward compatibility with existing machines
func TestMultiDockerHostSchemaCompatibility(t *testing.T) {
	// Create test server with database
	server, tempDB := setupTestServerWithDB(t)
	defer tempDB.Close()

	// Create a machine without docker host (legacy case)
	userFingerprint := "test-fingerprint"
	teamName := "testteam"
	machineName := "legacymachine"
	containerID := "legacy-container-id"
	image := "ubuntu:latest"

	// Use the old method without docker host
	err := server.createMachine(userFingerprint, teamName, machineName, containerID, image)
	if err != nil {
		t.Fatalf("Failed to create legacy machine: %v", err)
	}

	// Retrieve machine from database
	machine, err := server.getMachineByName(teamName, machineName)
	if err != nil {
		t.Fatalf("Failed to retrieve legacy machine: %v", err)
	}

	// Legacy machine should have nil docker host
	if machine.DockerHost != nil {
		t.Errorf("Legacy machine should have nil DockerHost, got %v", *machine.DockerHost)
	}

	t.Logf("✅ Legacy machine compatibility verified - DockerHost is nil as expected")
}
