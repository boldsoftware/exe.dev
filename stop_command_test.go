package exe

import (
	"exe.dev/sshbuf"
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"exe.dev/container"
)

func TestHandleStopCommand(t *testing.T) {
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
	machineName := "test-machine"

	if err := server.createUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, email); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(fingerprint, teamName, true); err != nil {
		t.Fatalf("Failed to add team member: %v", err)
	}

	// Create a container in the mock manager first
	containerReq := &container.CreateContainerRequest{
		UserID: fingerprint,
		Name:   machineName,
		Image:  "ubuntu:latest",
	}

	createdContainer, err := mockManager.CreateContainer(context.Background(), containerReq)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}

	// Create a machine record in database with container ID
	err = server.createMachine(fingerprint, teamName, machineName, createdContainer.ID, "ubuntu:latest")
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	tests := []struct {
		name         string
		args         []string
		expectOutput []string
		expectError  bool
		setupFunc    func()
	}{
		{
			name:         "stop running container successfully",
			args:         []string{machineName},
			expectOutput: []string{"Stopping machine", "Machine 'test-machine' stopped"},
			expectError:  false,
			setupFunc: func() {
				// Ensure container is running
				createdContainer.Status = container.StatusRunning
			},
		},
		{
			name:         "stop already stopped container",
			args:         []string{machineName},
			expectOutput: []string{"container is not running"},
			expectError:  true,
			setupFunc: func() {
				// Set container to stopped
				createdContainer.Status = container.StatusStopped
			},
		},
		{
			name:         "no container name provided",
			args:         []string{},
			expectOutput: []string{"Usage: stop <name>"},
			expectError:  true,
			setupFunc:    func() {},
		},
		{
			name:         "container not found",
			args:         []string{"nonexistent"},
			expectOutput: []string{"Machine 'nonexistent' not found"},
			expectError:  true,
			setupFunc:    func() {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup for this test
			tt.setupFunc()

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
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)

			// Create user session
			server.createUserSession(bufferedChannel, fingerprint, email, teamName, true)
			defer server.removeUserSession(bufferedChannel)

			// Call handleStopCommand
			server.handleStopCommand(bufferedChannel, tt.args)

			// Check output
			rawOutput := outputBuf.String()
			output := stripANSI(rawOutput)

			t.Logf("Output: %s", output)

			for _, expected := range tt.expectOutput {
				if !strings.Contains(output, expected) {
					t.Errorf("Expected output to contain %q, got: %s", expected, output)
				}
			}
		})
	}
}

func TestHandleStopCommandWithoutContainerManager(t *testing.T) {
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
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)

	// Create user session
	server.createUserSession(bufferedChannel, fingerprint, email, teamName, true)
	defer server.removeUserSession(bufferedChannel)

	// Call handleStopCommand
	server.handleStopCommand(bufferedChannel, []string{"testmachine"})

	// Check that it reports container management not available
	rawOutput := outputBuf.String()
	output := stripANSI(rawOutput)
	if !strings.Contains(output, "Machine management is not available") {
		t.Errorf("Expected 'Machine management is not available' in output, got: %s", output)
	}

	server.removeUserSession(bufferedChannel)
}

func TestHandleStopCommandContainerNotCreated(t *testing.T) {
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
	`, teamName, machineName, "pending", "ubuntu:latest", fingerprint)
	if err != nil {
		t.Fatalf("Failed to create machine without container: %v", err)
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
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)

	// Create user session
	server.createUserSession(bufferedChannel, fingerprint, email, teamName, true)
	defer server.removeUserSession(bufferedChannel)

	// Call handleStopCommand
	server.handleStopCommand(bufferedChannel, []string{machineName})

	// Check that it reports container not yet created
	rawOutput := outputBuf.String()
	output := stripANSI(rawOutput)
	if !strings.Contains(output, "Machine 'testmachine' not running") {
		t.Errorf("Expected 'Machine not running' in output, got: %s", output)
	}
}

func TestHandleStopCommandWithoutUserSession(t *testing.T) {
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
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)

	// Call handleStopCommand without user session
	server.handleStopCommand(bufferedChannel, []string{"testmachine"})

	// Check that it reports authentication error
	rawOutput := outputBuf.String()
	output := stripANSI(rawOutput)
	if !strings.Contains(output, "user not authenticated") {
		t.Errorf("Expected 'user not authenticated' in output, got: %s", output)
	}
}
