package exe

import (
	"context"
	"testing"

	"exe.dev/sqlite"
)

// TestMachineNameFormatParsing tests machine name parsing with the new alloc-based system
func TestMachineNameFormatParsing(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Create test user and alloc
	userID := "test-user-123"
	allocID := "alloc-123"
	machineName := "testmachine"

	// Create user
	err := server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`INSERT INTO users (user_id, email, created_at) VALUES (?, ?, datetime('now'))`, userID, "test@example.com")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create alloc with all required fields
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
			VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test@example.com')`, allocID, userID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create a machine in the alloc
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO machines (alloc_id, name, status, image, created_by_user_id, created_at, updated_at)
			VALUES (?, ?, 'stopped', 'ubuntu', ?, datetime('now'), datetime('now'))
		`, allocID, machineName, userID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Add SSH key for user
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key) VALUES (?, ?)`, userID, "dummy-key")
		return err
	})
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
			machine := server.FindMachineByNameForUser(t.Context(), userID, tt.machineName)

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
