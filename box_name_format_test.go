package exe

import (
	"context"
	"testing"

	"exe.dev/sqlite"
)

// TestBoxNameFormatParsing tests box name parsing with the new alloc-based system
func TestBoxNameFormatParsing(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Create test user and alloc
	userID := "test-user-123"
	allocID := "alloc-123"
	boxName := "testbox"

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
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost, created_at, stripe_customer_id, billing_email)
			VALUES (?, ?, 'medium', 'aws-us-west-2', 'local', datetime('now'), '', 'test@example.com')`, allocID, userID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create a box in the alloc
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO boxes (alloc_id, name, status, image, created_by_user_id, created_at, updated_at)
			VALUES (?, ?, 'stopped', 'ubuntu', ?, datetime('now'), datetime('now'))
		`, allocID, boxName, userID)
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
		boxName       string
		expectedAlloc string
		expectedFound bool
		description   string
	}{
		{
			name:          "box by name - globally unique",
			boxName:       "testbox",
			expectedAlloc: allocID,
			expectedFound: true,
			description:   "Box names are globally unique",
		},
		{
			name:          "nonexistent box",
			boxName:       "nonexistent",
			expectedFound: false,
			description:   "Should return nil for nonexistent box",
		},
		{
			name:          "box with dots in name",
			boxName:       "test.box.name",
			expectedFound: false,
			description:   "Box with dots (old format) should not exist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			box := server.FindBoxByNameForUser(t.Context(), userID, tt.boxName)

			if tt.expectedFound {
				if box == nil {
					t.Errorf("Expected to find box, but got nil. %s", tt.description)
					return
				}
				if box.AllocID != tt.expectedAlloc {
					t.Errorf("Expected alloc %s, got %s. %s", tt.expectedAlloc, box.AllocID, tt.description)
				}
				if box.Name != "testbox" {
					t.Errorf("Expected box name 'testbox', got %s", box.Name)
				}
			} else {
				if box != nil {
					t.Errorf("Expected nil, but found box: %+v. %s", box, tt.description)
				}
			}
		})
	}
}
