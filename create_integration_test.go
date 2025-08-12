package exe

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/container"
)

func TestCreateCommandVariantsIntegration(t *testing.T) {
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
	defer server.Stop()

	server.containerManager = mockManager

	// Create test user and team in database
	fingerprint := "test-create-variants"
	email := "test@example.com"
	teamName := "testteam"

	if err := server.createUser(fingerprint, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	if err := server.createTeam(teamName, "test-stripe-123"); err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	if err := server.addTeamMember(fingerprint, teamName, true); err != nil {
		t.Fatalf("Failed to add user to team: %v", err)
	}

	tests := []struct {
		name     string
		args     []string
		stdin    string
		expected []string
	}{
		{
			name:     "create with custom image and name",
			args:     []string{"--image=python:3.9", "--name=myapp"},
			stdin:    "",
			expected: []string{"Creating myapp", "python:3.9"},
		},
		{
			name:     "create with just custom image",
			args:     []string{"--image=node:18"},
			stdin:    "",
			expected: []string{"Creating", "node:18"}, // Name will be auto-generated
		},
		{
			name:     "create with Dockerfile",
			args:     []string{"--name=custom-service"},
			stdin:    "FROM nginx:alpine\nCOPY index.html /usr/share/nginx/html/",
			expected: []string{"Creating custom-service", "custom Dockerfile"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create terminal emulator and buffer
			var outputBuf bytes.Buffer
			term, err := NewTerminalEmulator()
			if err != nil {
				t.Skipf("Could not create terminal emulator: %v", err)
			}
			defer term.Close()

			term.buffer = &outputBuf

			mockChannel := &MockSSHChannel{
				term: term,
			}

			// Create user session
			server.createUserSession(mockChannel, fingerprint, email, teamName, true)
			defer server.removeUserSession(mockChannel)

			// Create stdin reader
			stdinReader := strings.NewReader(tt.stdin)

			// Call handleCreateCommandWithStdin
			server.handleCreateCommandWithStdin(mockChannel, tt.args, stdinReader)

			// Check output
			rawOutput := outputBuf.String()
			output := stripANSI(rawOutput)

			t.Logf("Output for %s: %s", tt.name, output)

			// Verify expected strings appear in output
			for _, expected := range tt.expected {
				if !strings.Contains(output, expected) {
					t.Errorf("Expected output to contain %q, got: %s", expected, output)
				}
			}

			// Verify container was created in the mock
			containers := mockManager.containers
			if len(containers) == 0 {
				t.Errorf("Expected container to be created in mock manager")
			} else {
				// Get the most recently created container
				var lastContainer *container.Container
				var lastTime time.Time
				for _, c := range containers {
					if c.CreatedAt.After(lastTime) {
						lastTime = c.CreatedAt
						lastContainer = c
					}
				}

				if lastContainer == nil {
					t.Errorf("Could not find created container")
				} else {
					t.Logf("Created container: %s with image: %s", lastContainer.Name, lastContainer.Image)

					// Verify image is set correctly
					if tt.stdin != "" {
						// Custom Dockerfile - image should be default
						if lastContainer.Image != "ubuntu:22.04" {
							t.Errorf("Expected default image ubuntu:22.04 for Dockerfile build, got: %s", lastContainer.Image)
						}
					} else {
						// Check if --image flag was used
						expectedImage := "ubuntu:22.04" // default
						for _, arg := range tt.args {
							if strings.HasPrefix(arg, "--image=") {
								expectedImage = strings.TrimPrefix(arg, "--image=")
								break
							}
						}
						if lastContainer.Image != expectedImage {
							t.Errorf("Expected image %s, got: %s", expectedImage, lastContainer.Image)
						}
					}
				}
			}

			// Clean up created containers for next test
			mockManager.containers = make(map[string]*container.Container)
		})
	}
}
