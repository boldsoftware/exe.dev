package exe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"exe.dev/sqlite"
)

func TestGetBoxByName(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Create test data
	userID := "test-user-id"
	email := "test@example.com"
	allocID := "test-alloc-id"
	boxName := "testbox"

	if err := server.createUser(t.Context(), userID, email); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create alloc with all required fields
	err := server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost, created_at, stripe_customer_id, billing_email)
			VALUES (?, ?, 'medium', 'aws-us-west-2', 'local', datetime('now'), '', 'test@example.com')`, allocID, userID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	err = server.createBox(t.Context(), userID, allocID, boxName, "container-123", "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create box: %v", err)
	}

	// Test getting box by name (globally unique now)
	box, err := server.getBoxByName(t.Context(), boxName)
	if err != nil {
		t.Fatalf("Failed to get box: %v", err)
	}

	if box.Name != boxName {
		t.Errorf("Expected box name %s, got %s", boxName, box.Name)
	}

	// Test getting non-existent box
	_, err = server.getBoxByName(t.Context(), "nonexistent")
	if err == nil {
		t.Error("Expected error when getting non-existent box")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Expected sql.ErrNoRows, got %v", err)
	}

	// Test getting box with empty name
	_, err = server.getBoxByName(t.Context(), "")
	if err == nil {
		t.Error("Expected error when getting box with empty name")
	}
}

func TestBoxUniqueConstraint(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Create test users and allocs
	userID1 := "test-user-1"
	userID2 := "test-user-2"
	allocID1 := "test-alloc-1"
	allocID2 := "test-alloc-2"
	boxName := "testbox"

	if err := server.createUser(t.Context(), userID1, "user1@example.com"); err != nil {
		t.Fatalf("Failed to create user1: %v", err)
	}
	if err := server.createUser(t.Context(), userID2, "user2@example.com"); err != nil {
		t.Fatalf("Failed to create user2: %v", err)
	}

	// Create allocs
	err := server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost, created_at)
			VALUES (?, ?, 'medium', 'aws-us-west-2', 'local', datetime('now'))`, allocID1, userID1)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create alloc1: %v", err)
	}

	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost, created_at, stripe_customer_id, billing_email)
			VALUES (?, ?, 'medium', 'aws-us-west-2', 'local', datetime('now'), '', 'test2@example.com')`, allocID2, userID2)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create alloc2: %v", err)
	}

	// Create box in first alloc
	err = server.createBox(t.Context(), userID1, allocID1, boxName, "container-1", "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create first box: %v", err)
	}

	// Try to create box with same name in different alloc - should fail now (globally unique)
	err = server.createBox(t.Context(), userID2, allocID2, boxName, "container-2", "ubuntu:22.04")
	if err == nil {
		t.Error("Expected error when creating box with duplicate name (globally unique)")
	}

	// Create box with different name should work
	err = server.createBox(t.Context(), userID2, allocID2, "differentbox", "container-3", "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create box with different name: %v", err)
	}
}

func TestBoxNameValidationIntegration(t *testing.T) {
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
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost, created_at, stripe_customer_id, billing_email)
			VALUES (?, ?, 'medium', 'aws-us-west-2', 'local', datetime('now'), '', 'test@example.com')`, allocID, userID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	tests := []struct {
		name        string
		boxName     string
		shouldFail  bool
		description string
	}{
		{"valid lowercase", "validbox", false, "Valid lowercase name should succeed"},
		{"valid with numbers", "box123", false, "Valid name with numbers should succeed"},
		{"valid with hyphen", "my-box", false, "Valid name with hyphen should succeed"},
		{"empty name", "", true, "Empty name should fail"},
		{"uppercase letters", "MyBox", true, "Uppercase letters should fail"},
		{"with underscore", "my_box", true, "Underscore should fail"},
		{"with space", "my box", true, "Space should fail"},
		{"with dot", "my.box", true, "Dot should fail"},
		{"starts with hyphen", "-box", true, "Starting with hyphen should fail"},
		{"ends with hyphen", "box-", true, "Ending with hyphen should fail"},
		{"too long", "verylongboxnamethatexceedslimit12345678901234567890verylongboxnamethatexceedslimit12345678901234567890", true, "Name exceeding limit should fail"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			containerID := fmt.Sprintf("container-%s", tt.boxName)
			err := server.createBox(t.Context(), userID, allocID, tt.boxName, containerID, "ubuntu:22.04")

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
						_, _ = tx.Exec(`DELETE FROM boxes WHERE name = ?`, tt.boxName)
						return nil
					})
				}
			}
		})
	}
}

func TestGeneratedBoxNamesAreValid(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Test that generateRandomContainerName creates valid names
	for i := 0; i < 10; i++ {
		name := generateRandomContainerName()

		if !server.isValidBoxName(name) {
			t.Errorf("Generated name '%s' is not valid", name)
		}

		// Check length
		if len(name) > 30 {
			t.Errorf("Generated name '%s' is too long (%d chars)", name, len(name))
		}
	}
}
