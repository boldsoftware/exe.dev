package exe

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"exe.dev/container"
)

func TestHandleStopCommandMultipleMachines(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create mock container manager
	mockManager := NewMockContainerManager()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.containerManager = mockManager
	defer server.Stop()

	// Create test data
	fingerprint := "test-fingerprint"
	email := "test@example.com"
	teamName := "testteam"

	// Create three test machines
	machines := []struct {
		name        string
		containerID string
	}{
		{"machine1", "container1"},
		{"machine2", "container2"},
		{"machine3", "container3"},
	}

	if err := server.createUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(fingerprint, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	// Create containers and machines
	for _, m := range machines {
		containerReq := &container.CreateContainerRequest{
			UserID: fingerprint,
			Name:   m.name,
			Image:  "ubuntu:latest",
		}

		createdContainer, err := mockManager.CreateContainer(context.Background(), containerReq)
		if err != nil {
			t.Fatalf("Failed to create container %s: %v", m.name, err)
		}

		// Store the actual container ID
		machines[findMachineIndex(machines, m.name)].containerID = createdContainer.ID

		// Create machine record in database
		err = server.createMachine(fingerprint, teamName, m.name, createdContainer.ID, "ubuntu:latest")
		if err != nil {
			t.Fatalf("Failed to create machine %s: %v", m.name, err)
		}

		// Ensure container is running
		createdContainer.Status = container.StatusRunning
	}

	// Create mock channel and terminal
	var outputBuf bytes.Buffer
	term, err := NewTerminalEmulator()
	if err != nil {
		t.Skipf("Could not create terminal emulator: %v", err)
	}
	defer term.Close()

	// Override the buffer for output capture
	term.buffer = &outputBuf

	mockChannel := &MockSSHChannel{
		term: term,
	}

	// Create user session
	server.createUserSession(mockChannel, fingerprint, email, teamName, true)
	defer server.removeUserSession(mockChannel)

	// Test stopping all three machines at once
	server.handleStopCommand(mockChannel, []string{"machine1", "machine2", "machine3"})

	// Check output
	rawOutput := outputBuf.String()
	output := stripANSI(rawOutput)

	t.Logf("Output: %s", output)

	// Verify each machine shows "Stopping machine" message
	for _, m := range machines {
		expectedStopping := "Stopping machine " + m.name
		if !strings.Contains(output, expectedStopping) {
			t.Errorf("Expected '%s' in output, got: %s", expectedStopping, output)
		}
	}

	// Verify each machine shows success
	for _, m := range machines {
		expectedSuccess := "✓ Machine '" + m.name + "' stopped"
		if !strings.Contains(output, expectedSuccess) {
			t.Errorf("Expected '%s' in output, got: %s", expectedSuccess, output)
		}
	}

	// Verify summary shows all stopped
	if !strings.Contains(output, "Summary: 3 stopped, 0 failed") {
		t.Errorf("Expected 'Summary: 3 stopped, 0 failed' in output, got: %s", output)
	}

	// Verify containers are actually stopped in the mock manager
	for _, m := range machines {
		c, err := mockManager.GetContainer(context.Background(), fingerprint, m.containerID)
		if err != nil {
			t.Errorf("Failed to get container %s: %v", m.name, err)
			continue
		}
		if c.Status != container.StatusStopped {
			t.Errorf("Expected container %s to be stopped, but status is %s", m.name, c.Status)
		}
	}
}

func TestHandleStopCommandPartialFailure(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create mock container manager
	mockManager := NewMockContainerManager()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.containerManager = mockManager
	defer server.Stop()

	// Create test data
	fingerprint := "test-fingerprint"
	email := "test@example.com"
	teamName := "testteam"

	if err := server.createUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(fingerprint, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	// Create one running machine
	containerReq := &container.CreateContainerRequest{
		UserID: fingerprint,
		Name:   "running-machine",
		Image:  "ubuntu:latest",
	}

	createdContainer, err := mockManager.CreateContainer(context.Background(), containerReq)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}
	createdContainer.Status = container.StatusRunning

	err = server.createMachine(fingerprint, teamName, "running-machine", createdContainer.ID, "ubuntu:latest")
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Create one stopped machine
	stoppedReq := &container.CreateContainerRequest{
		UserID: fingerprint,
		Name:   "stopped-machine",
		Image:  "ubuntu:latest",
	}

	stoppedContainer, err := mockManager.CreateContainer(context.Background(), stoppedReq)
	if err != nil {
		t.Fatalf("Failed to create stopped container: %v", err)
	}
	stoppedContainer.Status = container.StatusStopped

	err = server.createMachine(fingerprint, teamName, "stopped-machine", stoppedContainer.ID, "ubuntu:latest")
	if err != nil {
		t.Fatalf("Failed to create stopped machine: %v", err)
	}

	// Create mock channel and terminal
	var outputBuf bytes.Buffer
	term, err := NewTerminalEmulator()
	if err != nil {
		t.Skipf("Could not create terminal emulator: %v", err)
	}
	defer term.Close()

	// Override the buffer for output capture
	term.buffer = &outputBuf

	mockChannel := &MockSSHChannel{
		term: term,
	}

	// Create user session
	server.createUserSession(mockChannel, fingerprint, email, teamName, true)
	defer server.removeUserSession(mockChannel)

	// Test stopping both machines (one will fail, one nonexistent)
	server.handleStopCommand(mockChannel, []string{"running-machine", "stopped-machine", "nonexistent"})

	// Check output
	rawOutput := outputBuf.String()
	output := stripANSI(rawOutput)

	t.Logf("Output: %s", output)

	// Verify each machine shows "Stopping machine" message
	expectedRunning := "Stopping machine running-machine"
	if !strings.Contains(output, expectedRunning) {
		t.Errorf("Expected '%s' in output, got: %s", expectedRunning, output)
	}

	// Verify running machine was stopped
	if !strings.Contains(output, "✓ Machine 'running-machine' stopped") {
		t.Errorf("Expected success for running-machine in output, got: %s", output)
	}

	// Verify stopped machine shows error
	if !strings.Contains(output, "✗ Failed to stop machine 'stopped-machine'") {
		t.Errorf("Expected failure for stopped-machine in output, got: %s", output)
	}

	// Verify nonexistent machine shows not found
	if !strings.Contains(output, "✗ Machine 'nonexistent' not found") {
		t.Errorf("Expected not found for nonexistent in output, got: %s", output)
	}

	// Verify summary shows partial success
	if !strings.Contains(output, "Summary: 1 stopped, 2 failed") {
		t.Errorf("Expected 'Summary: 1 stopped, 2 failed' in output, got: %s", output)
	}
}

// Helper function to find machine index
func findMachineIndex(machines []struct{ name, containerID string }, name string) int {
	for i, m := range machines {
		if m.name == name {
			return i
		}
	}
	return -1
}
