package exe

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"exe.dev/container"
)

// stripANSI removes ANSI color codes from string
func stripANSI(str string) string {
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansiRegex.ReplaceAllString(str, "")
}

func TestHandleCreateCommand(t *testing.T) {
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

	// Create test user and team
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

	tests := []struct {
		name         string
		args         []string
		expectError  bool
		expectOutput []string
	}{
		{
			name:         "no arguments",
			args:         []string{},
			expectError:  false,
			expectOutput: []string{"Creating", "for team testteam", "Ready in", "Access with ssh", "exe.dev"},
		},
		{
			name:         "invalid container name",
			args:         []string{"AB"}, // too short and uppercase
			expectError:  true,
			expectOutput: []string{"Invalid container name"},
		},
		{
			name:         "valid container name", 
			args:         []string{"mycontainer"},
			expectError:  false,
			expectOutput: []string{"Creating", "mycontainer", "for team testteam", "Ready in", "Access with ssh mycontainer@exe.dev"},
		},
		{
			name:         "duplicate container name",
			args:         []string{"mycontainer"}, // same as above
			expectError:  true,
			expectOutput: []string{"Container name 'mycontainer' already exists"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

			// Call handleCreateCommand
			server.handleCreateCommand(mockChannel, tt.args)

			// Check output (strip ANSI color codes for comparison)
			rawOutput := outputBuf.String()
			output := stripANSI(rawOutput)
			for _, expected := range tt.expectOutput {
				if !strings.Contains(output, expected) {
					t.Errorf("Expected output to contain %q, got: %s", expected, output)
				}
			}

			// Clean up session
			server.removeUserSession(mockChannel)
		})
	}
}

func TestHandleCreateCommandWithoutContainerManager(t *testing.T) {
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

	// Create test user and team
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

	// Call handleCreateCommand
	server.handleCreateCommand(mockChannel, []string{"testcontainer"})

	// Check that it reports container management not available
	rawOutput := outputBuf.String()
	output := stripANSI(rawOutput)
	if !strings.Contains(output, "Container management is not available") {
		t.Errorf("Expected 'Container management is not available' in output, got: %s", output)
	}

	server.removeUserSession(mockChannel)
}

func TestHandleCreateCommandWithoutUserSession(t *testing.T) {
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

	// Call handleCreateCommand without user session
	server.handleCreateCommand(mockChannel, []string{"testcontainer"})

	// Check that it reports authentication error
	rawOutput := outputBuf.String()
	output := stripANSI(rawOutput)
	if !strings.Contains(output, "user not authenticated") {
		t.Errorf("Expected 'user not authenticated' in output, got: %s", output)
	}
}

func TestCreateCommandIntegration(t *testing.T) {
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

	// Create test user and team
	fingerprint := "test-fingerprint"
	email := "test@example.com"
	teamName := "testteam"
	containerName := "integrationtest"

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

	// Call handleCreateCommand
	server.handleCreateCommand(mockChannel, []string{containerName})

	// Verify container was created in database
	machine, err := server.getMachineByName(teamName, containerName)
	if err != nil {
		t.Fatalf("Failed to get machine from database: %v", err)
	}

	if machine.Name != containerName {
		t.Errorf("Expected machine name %s, got %s", containerName, machine.Name)
	}
	if machine.TeamName != teamName {
		t.Errorf("Expected team name %s, got %s", teamName, machine.TeamName)
	}
	if machine.CreatedByFingerprint != fingerprint {
		t.Errorf("Expected created by %s, got %s", fingerprint, machine.CreatedByFingerprint)
	}
	if machine.ContainerID == nil {
		t.Error("Expected container ID to be set")
	}

	// Verify container was created in mock manager
	containers, err := mockManager.ListContainers(context.Background(), fingerprint)
	if err != nil {
		t.Fatalf("Failed to list containers: %v", err)
	}
	
	found := false
	for _, c := range containers {
		if c.Name == containerName && c.UserID == fingerprint {
			found = true
			if c.Status != container.StatusRunning {
				t.Errorf("Expected container status %s, got %s", container.StatusRunning, c.Status)
			}
			break
		}
	}
	
	if !found {
		t.Error("Container not found in mock manager")
	}

	// Verify success message in output
	rawOutput := outputBuf.String()
	output := stripANSI(rawOutput)
	if !strings.Contains(output, "Ready in") && !strings.Contains(output, "Access with ssh") {
		t.Errorf("Expected success message in output, got: %s", output)
	}

	server.removeUserSession(mockChannel)
}