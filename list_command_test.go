package exe

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"exe.dev/container"
	"exe.dev/sshbuf"
)

func TestHandleListCommand(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create mock container manager
	mockManager := NewMockContainerManager()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), "local", "")
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

	tests := []struct {
		name            string
		setupContainers func()
		expectedOutput  []string
		expectError     bool
	}{
		{
			name: "no containers",
			setupContainers: func() {
				// No setup - no containers
			},
			expectedOutput: []string{"No machines found", "create <name>"},
			expectError:    false,
		},
		{
			name: "single container",
			setupContainers: func() {
				// Create a container in the mock manager
				mockManager.CreateContainer(nil, &container.CreateContainerRequest{
					UserID: fingerprint,
					Name:   "test-container",
					Image:  "ubuntu:22.04",
				})
			},
			expectedOutput: []string{"Machines for team", "test-container"},
			expectError:    false,
		},
		{
			name: "multiple containers",
			setupContainers: func() {
				// Create multiple containers in the mock manager
				mockManager.CreateContainer(nil, &container.CreateContainerRequest{
					UserID: fingerprint,
					Name:   "container1",
					Image:  "ubuntu:22.04",
				})
				mockManager.CreateContainer(nil, &container.CreateContainerRequest{
					UserID: fingerprint,
					Name:   "container2",
					Image:  "python:3.9",
				})
			},
			expectedOutput: []string{"Machines for team", "container1", "container2"},
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear containers from previous test
			mockManager.containers = make(map[string]*container.Container)

			// Setup containers for this test
			tt.setupContainers()

			// Reset output buffer
			outputBuf.Reset()

			// Call handleListCommand
			server.handleListCommand(bufferedChannel)

			// Check output
			rawOutput := outputBuf.String()
			output := stripANSI(rawOutput)

			t.Logf("Output: %s", output)

			for _, expected := range tt.expectedOutput {
				if !strings.Contains(output, expected) {
					t.Errorf("Expected output to contain %q, got: %s", expected, output)
				}
			}
		})
	}
}

func TestHandleListCommandWithoutContainerManager(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), "local", "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	// Explicitly set containerManager to nil for this test
	server.containerManager = nil
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

	// Call handleListCommand
	server.handleListCommand(bufferedChannel)

	// Check that it reports container management not available
	rawOutput := outputBuf.String()
	output := stripANSI(rawOutput)
	if !strings.Contains(output, "Machine management is not available") {
		t.Errorf("Expected 'Machine management is not available' in output, got: %s", output)
	}

	server.removeUserSession(bufferedChannel)
}
