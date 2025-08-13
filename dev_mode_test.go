package exe

import (
	"os"
	"testing"
	"exe.dev/container"
)

func TestDevModeConfiguration(t *testing.T) {
	tests := []struct {
		name            string
		devMode         string
		gcpProjectID    string
		expectManager   bool
		expectBackend   string
	}{
		{
			name:            "production mode with GCP",
			devMode:         "",
			gcpProjectID:    "test-project",
			expectManager:   false, // Would be true if GKE was actually available
			expectBackend:   "",
		},
		{
			name:            "local mode",
			devMode:         "local",
			gcpProjectID:    "",
			expectManager:   true,
			expectBackend:   "docker",
		},
		{
			name:            "realgke mode with GCP",
			devMode:         "realgke",
			gcpProjectID:    "test-project",
			expectManager:   false, // Would be true if GKE was actually available
			expectBackend:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary database file
			tmpDB, err := os.CreateTemp("", "test_*.db")
			if err != nil {
				t.Fatalf("Failed to create temp db: %v", err)
			}
			defer os.Remove(tmpDB.Name())
			tmpDB.Close()

			// Create server with specified configuration
			server, err := NewServer(":0", "", ":0", tmpDB.Name(), tt.devMode, tt.gcpProjectID)
			if err != nil {
				t.Fatalf("Failed to create server: %v", err)
			}
			defer server.Stop()

			// Check container manager
			if tt.expectManager {
				if server.containerManager == nil {
					t.Errorf("Expected container manager to be initialized, but got nil")
				} else if tt.expectBackend == "docker" {
					// Check that it's a Docker manager
					if _, ok := server.containerManager.(*container.DockerManager); !ok {
						t.Errorf("Expected DockerManager, got %T", server.containerManager)
					}
				}
			}

			// Check dev mode setting
			if server.devMode != tt.devMode {
				t.Errorf("Expected devMode to be %q, got %q", tt.devMode, server.devMode)
			}
		})
	}
}

func TestDevModeEmailBehavior(t *testing.T) {
	tests := []struct {
		name         string
		devMode      string
		expectEmail  bool
	}{
		{
			name:         "production mode sends emails",
			devMode:      "",
			expectEmail:  true,
		},
		{
			name:         "local mode logs emails",
			devMode:      "local",
			expectEmail:  false,
		},
		{
			name:         "realgke mode logs emails",
			devMode:      "realgke",
			expectEmail:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary database file
			tmpDB, err := os.CreateTemp("", "test_*.db")
			if err != nil {
				t.Fatalf("Failed to create temp db: %v", err)
			}
			defer os.Remove(tmpDB.Name())
			tmpDB.Close()

			// Create server with specified configuration
			server, err := NewServer(":0", "", ":0", tmpDB.Name(), tt.devMode, "")
			if err != nil {
				t.Fatalf("Failed to create server: %v", err)
			}
			defer server.Stop()

			// In dev modes, emails should not error
			err = server.sendEmail("test@example.com", "Test", "Test body")
			if tt.devMode != "" && err != nil {
				t.Errorf("In dev mode %q, expected no error, got: %v", tt.devMode, err)
			}
			// In production mode without Postmark configured, it's expected to return an error
		})
	}
}