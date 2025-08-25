package exe

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestCreateMachine(t *testing.T) {
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

	// Create test user and alloc first
	userID := "test-user-id"
	email := "test@example.com"
	allocID := "test-alloc-id"

	if err := server.createUser(userID, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create alloc for the user
	_, err = server.db.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, created_at)
		VALUES (?, ?, 'medium', 'aws-us-west-2', datetime('now'))`, allocID, userID)
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Test creating a machine
	machineName := "testmachine"
	containerID := "mock-container-123"
	image := "ubuntu:22.04"

	err = server.createMachine(userID, allocID, machineName, containerID, image)
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Verify machine was created correctly
	machine, err := server.getMachineByName(machineName)
	if err != nil {
		t.Fatalf("Failed to get machine: %v", err)
	}

	if machine.AllocID != allocID {
		t.Errorf("Expected alloc ID %s, got %s", allocID, machine.AllocID)
	}

	if machine.Name != machineName {
		t.Errorf("Expected machine name %s, got %s", machineName, machine.Name)
	}

	if machine.Image != image {
		t.Errorf("Expected image %s, got %s", image, machine.Image)
	}

	if machine.ContainerID == nil || *machine.ContainerID != containerID {
		t.Errorf("Expected container ID %s, got %v", containerID, machine.ContainerID)
	}

	// TODO: Verify machine was created by the correct user
	// This test needs to be updated to work with the new user management system

	if machine.Status != "pending" {
		t.Errorf("Expected status 'pending', got %s", machine.Status)
	}
}

func TestGetMachineByName(t *testing.T) {
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

	// Create test data
	userID := "test-user-id"
	email := "test@example.com"
	allocID := "test-alloc-id"
	machineName := "testmachine"

	if err := server.createUser(userID, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create alloc with all required fields
	_, err = server.db.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
		VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test@example.com')`, allocID, userID)
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	err = server.createMachine(userID, allocID, machineName, "container-123", "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Test getting machine by name (globally unique now)
	machine, err := server.getMachineByName(machineName)
	if err != nil {
		t.Fatalf("Failed to get machine: %v", err)
	}

	if machine.Name != machineName {
		t.Errorf("Expected machine name %s, got %s", machineName, machine.Name)
	}

	// Test getting non-existent machine
	_, err = server.getMachineByName("nonexistent")
	if err == nil {
		t.Error("Expected error when getting non-existent machine")
	}
	if err != sql.ErrNoRows {
		t.Errorf("Expected sql.ErrNoRows, got %v", err)
	}

	// Test getting machine with empty name
	_, err = server.getMachineByName("")
	if err == nil {
		t.Error("Expected error when getting machine with empty name")
	}
}

func TestMachineUniqueConstraint(t *testing.T) {
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

	// Create test users and allocs
	userID1 := "test-user-1"
	userID2 := "test-user-2"
	allocID1 := "test-alloc-1"
	allocID2 := "test-alloc-2"
	machineName := "testmachine"

	if err := server.createUser(userID1, "user1@example.com"); err != nil {
		t.Fatalf("Failed to create user1: %v", err)
	}
	if err := server.createUser(userID2, "user2@example.com"); err != nil {
		t.Fatalf("Failed to create user2: %v", err)
	}

	// Create allocs
	_, err = server.db.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, created_at)
		VALUES (?, ?, 'medium', 'aws-us-west-2', datetime('now'))`, allocID1, userID1)
	if err != nil {
		t.Fatalf("Failed to create alloc1: %v", err)
	}

	_, err = server.db.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
		VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test2@example.com')`, allocID2, userID2)
	if err != nil {
		t.Fatalf("Failed to create alloc2: %v", err)
	}

	// Create machine in first alloc
	err = server.createMachine(userID1, allocID1, machineName, "container-1", "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create first machine: %v", err)
	}

	// Try to create machine with same name in different alloc - should fail now (globally unique)
	err = server.createMachine(userID2, allocID2, machineName, "container-2", "ubuntu:22.04")
	if err == nil {
		t.Error("Expected error when creating machine with duplicate name (globally unique)")
	}

	// Create machine with different name should work
	err = server.createMachine(userID2, allocID2, "differentmachine", "container-3", "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create machine with different name: %v", err)
	}
}

func TestMachineTimestamps(t *testing.T) {
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

	// Create test data
	userID := "test-user-id"
	allocID := "test-alloc-id"
	machineName := "testmachine"

	if err := server.createUser(userID, "test@example.com"); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create alloc with all required fields
	_, err = server.db.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
		VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test@example.com')`, allocID, userID)
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Truncate to second precision since SQLite datetime() has second precision
	beforeCreate := time.Now().UTC().Truncate(time.Second)
	err = server.createMachine(userID, allocID, machineName, "container-123", "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}
	afterCreate := time.Now().UTC().Truncate(time.Second).Add(time.Second) // Add 1 second for upper bound

	machine, err := server.getMachineByName(machineName)
	if err != nil {
		t.Fatalf("Failed to get machine: %v", err)
	}

	// Check created_at timestamp (with second precision)
	if machine.CreatedAt.Before(beforeCreate) || machine.CreatedAt.After(afterCreate) {
		t.Errorf("Created timestamp %v is not between %v and %v",
			machine.CreatedAt, beforeCreate, afterCreate)
	}

	// Check updated_at timestamp (with second precision)
	if machine.UpdatedAt.Before(beforeCreate) || machine.UpdatedAt.After(afterCreate) {
		t.Errorf("Updated timestamp %v is not between %v and %v",
			machine.UpdatedAt, beforeCreate, afterCreate)
	}

	// Update machine status
	time.Sleep(1 * time.Second) // Ensure at least 1 second has passed for SQLite datetime precision
	beforeUpdate := time.Now().UTC().Truncate(time.Second)
	_, err = server.db.Exec(`UPDATE machines SET status = 'running', updated_at = datetime('now') WHERE name = ?`, machineName)
	if err != nil {
		t.Fatalf("Failed to update machine: %v", err)
	}
	afterUpdate := time.Now().UTC().Truncate(time.Second).Add(time.Second) // Add 1 second for upper bound

	updatedMachine, err := server.getMachineByName(machineName)
	if err != nil {
		t.Fatalf("Failed to get updated machine: %v", err)
	}

	// Check that updated_at changed (with second precision)
	if updatedMachine.UpdatedAt.Before(beforeUpdate) || updatedMachine.UpdatedAt.After(afterUpdate) {
		t.Errorf("Updated timestamp %v is not between %v and %v after update",
			updatedMachine.UpdatedAt, beforeUpdate, afterUpdate)
	}

	// Check that created_at didn't change
	if !updatedMachine.CreatedAt.Equal(machine.CreatedAt) {
		t.Errorf("Created timestamp changed from %v to %v",
			machine.CreatedAt, updatedMachine.CreatedAt)
	}
}

func TestMachineNameValidationIntegration(t *testing.T) {
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

	// Create test data
	userID := "test-user-id"
	allocID := "test-alloc-id"

	if err := server.createUser(userID, "test@example.com"); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create alloc with all required fields
	_, err = server.db.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
		VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test@example.com')`, allocID, userID)
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
			err := server.createMachine(userID, allocID, tt.machineName, containerID, "ubuntu:22.04")

			if tt.shouldFail {
				if err == nil {
					t.Errorf("%s: Expected error but got none", tt.description)
				}
			} else {
				if err != nil {
					t.Errorf("%s: Expected success but got error: %v", tt.description, err)
				} else {
					// Clean up successful creation for next test
					_, _ = server.db.Exec(`DELETE FROM machines WHERE name = ?`, tt.machineName)
				}
			}
		})
	}
}

func TestGeneratedMachineNamesAreValid(t *testing.T) {
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
