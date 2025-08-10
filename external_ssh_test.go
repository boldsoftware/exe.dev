package exe

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/container"
)

func TestExternalSSHAccess(t *testing.T) {
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

	// Create a test container
	containerReq := &container.CreateContainerRequest{
		UserID: fingerprint,
		Name:   "web-app",
		Image:  "nginx:latest",
	}
	
	_, err = mockManager.CreateContainer(context.Background(), containerReq)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
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

	tests := []struct {
		name         string
		username     string
		expectDirect bool
		expectOutput []string
		skipFullRun  bool // Skip full execution for non-direct access cases
	}{
		{
			name:         "direct access to existing container",
			username:     "web-app",
			expectDirect: true,
			expectOutput: []string{"web-app.exe.dev"},
			skipFullRun:  false,
		},
		{
			name:         "check container lookup for non-existent", 
			username:     "nonexistent",
			expectDirect: false,
			expectOutput: []string{}, // We'll test the lookup logic separately
			skipFullRun:  true,
		},
		{
			name:         "check container lookup for empty username",
			username:     "",
			expectDirect: false,
			expectOutput: []string{},
			skipFullRun:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skipFullRun {
				// Just test the container lookup logic for non-direct cases
				container := server.findContainerByName(fingerprint, tt.username)
				if tt.expectDirect && container == nil {
					t.Error("Expected to find container but got nil")
				} else if !tt.expectDirect && container != nil {
					t.Error("Expected not to find container but got one")
				}
				return
			}

			// Reset output buffer
			outputBuf.Reset()

			// For direct access cases, run the full test
			server.handleSSHShell(mockChannel, tt.username, fingerprint, true)

			// Give it a moment to process
			time.Sleep(50 * time.Millisecond)

			// Check output
			rawOutput := outputBuf.String()
			output := stripANSI(rawOutput)

			t.Logf("Output: %s", output)

			for _, expected := range tt.expectOutput {
				if !strings.Contains(output, expected) {
					t.Errorf("Expected output to contain %q, got: %s", expected, output)
				}
			}

			if tt.expectDirect {
				// For direct access, should connect to container, not show main menu
				if strings.Contains(output, "exe.dev ▶") {
					t.Error("Expected direct container access, but got main menu")
				}
			}
		})
	}
}

func TestExternalSSHAccessWithoutContainerManager(t *testing.T) {
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

	// Test the container lookup logic when no container manager
	container := server.findContainerByName(fingerprint, "test-container")
	if container != nil {
		t.Error("Expected no container when container manager is nil, but got one")
	}
}

func TestFindContainerByName(t *testing.T) {
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

	fingerprint := "test-user"
	
	// Create test containers
	containers := []*container.CreateContainerRequest{
		{UserID: fingerprint, Name: "web", Image: "nginx:latest"},
		{UserID: fingerprint, Name: "api", Image: "node:16"},
		{UserID: fingerprint, Name: "database", Image: "postgres:13"},
	}
	
	for _, req := range containers {
		_, err := mockManager.CreateContainer(context.Background(), req)
		if err != nil {
			t.Fatalf("Failed to create container: %v", err)
		}
	}

	tests := []struct {
		name          string
		searchName    string
		shouldFind    bool
		expectedName  string
	}{
		{"find existing container", "web", true, "web"},
		{"find another existing container", "api", true, "api"},
		{"case sensitive search", "WEB", false, ""},
		{"non-existent container", "nonexistent", false, ""},
		{"empty name", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := server.findContainerByName(fingerprint, tt.searchName)
			
			if tt.shouldFind {
				if result == nil {
					t.Errorf("Expected to find container %q, got nil", tt.searchName)
				} else if result.Name != tt.expectedName {
					t.Errorf("Expected container name %q, got %q", tt.expectedName, result.Name)
				}
			} else {
				if result != nil {
					t.Errorf("Expected not to find container %q, but got %v", tt.searchName, result)
				}
			}
		})
	}
}