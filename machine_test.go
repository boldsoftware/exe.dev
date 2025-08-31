package exe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"exe.dev/sqlite"
)

func TestGetMachineByName(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Create test data
	userID := "test-user-id"
	email := "test@example.com"
	allocID := "test-alloc-id"
	machineName := "testmachine"

	if err := server.createUser(t.Context(), userID, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create alloc with all required fields
	err := server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
			VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test@example.com')`, allocID, userID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	err = server.createMachine(t.Context(), userID, allocID, machineName, "container-123", "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Test getting machine by name (globally unique now)
	machine, err := server.getMachineByName(t.Context(), machineName)
	if err != nil {
		t.Fatalf("Failed to get machine: %v", err)
	}

	if machine.Name != machineName {
		t.Errorf("Expected machine name %s, got %s", machineName, machine.Name)
	}

	// Test getting non-existent machine
	_, err = server.getMachineByName(t.Context(), "nonexistent")
	if err == nil {
		t.Error("Expected error when getting non-existent machine")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Expected sql.ErrNoRows, got %v", err)
	}

	// Test getting machine with empty name
	_, err = server.getMachineByName(t.Context(), "")
	if err == nil {
		t.Error("Expected error when getting machine with empty name")
	}
}

func TestMachineUniqueConstraint(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Create test users and allocs
	userID1 := "test-user-1"
	userID2 := "test-user-2"
	allocID1 := "test-alloc-1"
	allocID2 := "test-alloc-2"
	machineName := "testmachine"

	if err := server.createUser(t.Context(), userID1, "user1@example.com"); err != nil {
		t.Fatalf("Failed to create user1: %v", err)
	}
	if err := server.createUser(t.Context(), userID2, "user2@example.com"); err != nil {
		t.Fatalf("Failed to create user2: %v", err)
	}

	// Create allocs
	err := server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, created_at)
			VALUES (?, ?, 'medium', 'aws-us-west-2', datetime('now'))`, allocID1, userID1)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create alloc1: %v", err)
	}

	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
			VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test2@example.com')`, allocID2, userID2)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create alloc2: %v", err)
	}

	// Create machine in first alloc
	err = server.createMachine(t.Context(), userID1, allocID1, machineName, "container-1", "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create first machine: %v", err)
	}

	// Try to create machine with same name in different alloc - should fail now (globally unique)
	err = server.createMachine(t.Context(), userID2, allocID2, machineName, "container-2", "ubuntu:22.04")
	if err == nil {
		t.Error("Expected error when creating machine with duplicate name (globally unique)")
	}

	// Create machine with different name should work
	err = server.createMachine(t.Context(), userID2, allocID2, "differentmachine", "container-3", "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create machine with different name: %v", err)
	}
}

func TestMachineNameValidationIntegration(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Create test data
	userID := "test-user-id"
	allocID := "test-alloc-id"

	if err := server.createUser(t.Context(), userID, "test@example.com"); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create alloc with all required fields
	err := server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
			VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test@example.com')`, allocID, userID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	tests := []struct {
		name        string
		machineName string
		shouldFail  bool
		description string
	}{
		{"valid lowercase", "validmachine", false, "Valid lowercase name should succeed"},
		{"valid with numbers", "machine123", false, "Valid name with numbers should succeed"},
		{"valid with hyphen", "my-machine", false, "Valid name with hyphen should succeed"},
		{"empty name", "", true, "Empty name should fail"},
		{"uppercase letters", "MyMachine", true, "Uppercase letters should fail"},
		{"with underscore", "my_machine", true, "Underscore should fail"},
		{"with space", "my machine", true, "Space should fail"},
		{"with dot", "my.machine", true, "Dot should fail"},
		{"starts with hyphen", "-machine", true, "Starting with hyphen should fail"},
		{"ends with hyphen", "machine-", true, "Ending with hyphen should fail"},
		{"too long", "verylongmachinenamethatexceedslimit12345678901234567890", true, "Name exceeding limit should fail"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			containerID := fmt.Sprintf("container-%s", tt.machineName)
			err := server.createMachine(t.Context(), userID, allocID, tt.machineName, containerID, "ubuntu:22.04")

			if tt.shouldFail {
				if err == nil {
					t.Errorf("%s: Expected error but got none", tt.description)
				}
			} else {
				if err != nil {
					t.Errorf("%s: Expected success but got error: %v", tt.description, err)
				} else {
					// Clean up successful creation for next test
					server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
						_, _ = tx.Exec(`DELETE FROM machines WHERE name = ?`, tt.machineName)
						return nil
					})
				}
			}
		})
	}
}

func TestGeneratedMachineNamesAreValid(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Test that generateRandomContainerName creates valid names
	for i := 0; i < 10; i++ {
		name := generateRandomContainerName()

		if !server.isValidMachineName(name) {
			t.Errorf("Generated name '%s' is not valid", name)
		}

		// Check length
		if len(name) > 30 {
			t.Errorf("Generated name '%s' is too long (%d chars)", name, len(name))
		}
	}
}
