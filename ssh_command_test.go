package exe

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/container"
)

func TestHandleSSHCommand(t *testing.T) {
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
	machineName := "testmachine"
	containerID := "mock-container-123"

	// Set up user, team, and machine
	if err := server.createUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(fingerprint, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}
	if err := server.createMachine(fingerprint, teamName, machineName, containerID, "ubuntu:22.04"); err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Create mock container in manager with the same ID as stored in database
	now := time.Now()
	createdContainer := &container.Container{
		ID:        containerID,
		UserID:    fingerprint,
		Name:      machineName,
		Image:     "ubuntu:22.04",
		Status:    container.StatusRunning,
		CreatedAt: now,
		StartedAt: &now,
	}
	mockManager.containers[containerID] = createdContainer

	// Create mock channel
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

	tests := []struct {
		name          string
		args          []string
		expectError   bool
		expectOutput  []string
		expectedExecs int
	}{
		{
			name:          "no arguments",
			args:          []string{},
			expectError:   true,
			expectOutput:  []string{"Usage: ssh <name>"},
			expectedExecs: 0,
		},
		{
			name:          "non-existent container",
			args:          []string{"nonexistent"},
			expectError:   true,
			expectOutput:  []string{"Machine 'nonexistent' not found"},
			expectedExecs: 0,
		},
		{
			name:          "valid container",
			args:          []string{machineName},
			expectError:   false,
			expectOutput:  []string{"Machine 'testmachine' is running", "ssh testmachine@exe.dev"},
			expectedExecs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset output buffer and exec calls
			outputBuf.Reset()
			mockManager.execCalls = nil

			// Call handleSSHCommand
			server.handleSSHCommand(mockChannel, tt.args)

			// Check output (strip ANSI color codes for comparison)
			rawOutput := outputBuf.String()
			output := stripANSI(rawOutput)
			for _, expected := range tt.expectOutput {
				if !strings.Contains(output, expected) {
					t.Errorf("Expected output to contain %q, got: %s", expected, output)
				}
			}

			// Check exec calls
			execCalls := mockManager.GetExecCalls()
			if len(execCalls) != tt.expectedExecs {
				t.Errorf("Expected %d exec calls, got %d", tt.expectedExecs, len(execCalls))
			}

		})
	}
}

func TestHandleSSHCommandWithoutContainerManager(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	// Don't set containerManager - leave it nil
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

	// Create mock channel
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

	// Call handleSSHCommand
	server.handleSSHCommand(mockChannel, []string{"testmachine"})

	// Check that it reports container management not available
	rawOutput := outputBuf.String()
	output := stripANSI(rawOutput)
	if !strings.Contains(output, "Machine management is not available") {
		t.Errorf("Expected 'Machine management is not available' in output, got: %s", output)
	}
}

func TestHandleSSHCommandContainerNotCreated(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

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
	machineName := "testmachine"

	if err := server.createUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(fingerprint, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	// Create machine WITHOUT container ID (simulates machine record without container)
	_, err = server.db.Exec(`
		INSERT INTO machines (team_name, name, status, image, created_by_fingerprint) 
		VALUES (?, ?, ?, ?, ?)
	`, teamName, machineName, "pending", "ubuntu:22.04", fingerprint)
	if err != nil {
		t.Fatalf("Failed to create machine without container: %v", err)
	}

	// Create mock channel
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

	// Call handleSSHCommand
	server.handleSSHCommand(mockChannel, []string{machineName})

	// Check that it reports container not yet created
	rawOutput := outputBuf.String()
	output := stripANSI(rawOutput)
	if !strings.Contains(output, "Machine 'testmachine' not yet created") {
		t.Errorf("Expected 'Machine not yet created' in output, got: %s", output)
	}
}

func TestHandleSSHCommandWithStoppedContainer(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

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
	machineName := "testmachine"
	containerID := "mock-container-123"

	if err := server.createUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(fingerprint, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}
	if err := server.createMachine(fingerprint, teamName, machineName, containerID, "ubuntu:22.04"); err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Create container in manager with stopped status
	now := time.Now()
	stoppedContainer := &container.Container{
		ID:        containerID,
		UserID:    fingerprint,
		Name:      machineName,
		Image:     "ubuntu:22.04",
		Status:    container.StatusStopped,
		CreatedAt: now,
		StoppedAt: &now,
	}
	mockManager.containers[containerID] = stoppedContainer

	// Create mock channel
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

	// Call handleSSHCommand
	server.handleSSHCommand(mockChannel, []string{machineName})

	// handleSSHCommand now just shows instructions, doesn't check container status
	rawOutput := outputBuf.String()
	output := stripANSI(rawOutput)
	// It should still show instructions even for stopped containers
	if !strings.Contains(output, "Machine 'testmachine' is running") || !strings.Contains(output, "ssh testmachine@exe.dev") {
		t.Errorf("Expected instructions to be shown, got: %s", output)
	}
}

func TestHandleSSHCommandWithoutUserSession(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	mockManager := NewMockContainerManager()
	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.containerManager = mockManager
	defer server.Stop()

	// Create mock channel WITHOUT creating user session
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

	// Call handleSSHCommand without user session
	server.handleSSHCommand(mockChannel, []string{"testmachine"})

	// Check that it reports authentication error
	rawOutput := outputBuf.String()
	output := stripANSI(rawOutput)
	if !strings.Contains(output, "user not authenticated") {
		t.Errorf("Expected 'user not authenticated' in output, got: %s", output)
	}
}

// MockEOFError simulates EOF
type MockEOFError struct{}

func (e *MockEOFError) Error() string {
	return "EOF"
}
