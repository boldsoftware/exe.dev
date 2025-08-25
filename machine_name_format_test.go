package exe

import (
	"os"
	"testing"
)

// TestMachineNameFormatParsing tests machine name parsing with the new alloc-based system
func TestMachineNameFormatParsing(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create test user and alloc
	userID := "test-user-123"
	allocID := "alloc-123"
	machineName := "testmachine"

	// Create user
	_, err = server.db.Exec(`INSERT INTO users (user_id, email, created_at) VALUES (?, ?, datetime('now'))`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Create alloc with all required fields
	_, err = server.db.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
		VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test@example.com')`, allocID, userID)
	if err != nil {
		t.Fatal(err)
	}

	// Create a machine in the alloc
	_, err = server.db.Exec(`
		INSERT INTO machines (alloc_id, name, status, image, created_by_user_id, created_at, updated_at)
		VALUES (?, ?, 'stopped', 'ubuntu', ?, datetime('now'), datetime('now'))
	`, allocID, machineName, userID)
	if err != nil {
		t.Fatal(err)
	}

	// Add SSH key for user
	_, err = server.db.Exec(`INSERT INTO ssh_keys (user_id, public_key, verified) VALUES (?, ?, 1)`, userID, "dummy-key")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		machineName   string
		expectedAlloc string
		expectedFound bool
		description   string
	}{
		{
			name:          "machine by name - globally unique",
			machineName:   "testmachine",
			expectedAlloc: allocID,
			expectedFound: true,
			description:   "Machine names are globally unique",
		},
		{
			name:          "nonexistent machine",
			machineName:   "nonexistent",
			expectedFound: false,
			description:   "Should return nil for nonexistent machine",
		},
		{
			name:          "machine with dots in name",
			machineName:   "test.machine.name",
			expectedFound: false,
			description:   "Machine with dots (old format) should not exist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			machine := server.FindMachineByNameForUser(userID, tt.machineName)

			if tt.expectedFound {
				if machine == nil {
					t.Errorf("Expected to find machine, but got nil. %s", tt.description)
					return
				}
				if machine.AllocID != tt.expectedAlloc {
					t.Errorf("Expected alloc %s, got %s. %s", tt.expectedAlloc, machine.AllocID, tt.description)
				}
				if machine.Name != "testmachine" {
					t.Errorf("Expected machine name 'testmachine', got %s", machine.Name)
				}
			} else {
				if machine != nil {
					t.Errorf("Expected nil, but found machine: %+v. %s", machine, tt.description)
				}
			}
		})
	}
}

// TestFormatSSHConnectionInfo tests the SSH connection format with the new naming
func TestFormatSSHConnectionInfo(t *testing.T) {
	server := &Server{}

	tests := []struct {
		name        string
		devMode     string
		allocID     string
		machineName string
		expected    string
	}{
		{
			name:        "production mode",
			devMode:     "",
			allocID:     "alloc-123",
			machineName: "mymachine",
			expected:    "ssh mymachine@exe.dev",
		},
		{
			name:        "local dev mode with port 22",
			devMode:     "local",
			allocID:     "alloc-456",
			machineName: "testmachine",
			expected:    "ssh testmachine@localhost",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server.devMode = tt.devMode
			result := server.formatSSHConnectionInfo(tt.allocID, tt.machineName)

			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}
