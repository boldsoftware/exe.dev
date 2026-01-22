package exedb_test

import (
	"context"
	"testing"

	"exe.dev/exedb"
)

func TestInsertDeletedBox_DuplicateIgnored(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	// Create a test user
	userID := "usr1234567890123"
	_, err := db.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	boxID := int64(12345)

	// First insert should succeed
	err = queries.InsertDeletedBox(ctx, exedb.InsertDeletedBoxParams{
		ID:     boxID,
		UserID: userID,
	})
	if err != nil {
		t.Fatalf("First InsertDeletedBox failed: %v", err)
	}

	// Verify the box is in deleted_boxes
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM deleted_boxes WHERE id = ?`, boxID).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query deleted_boxes: %v", err)
	}
	if count != 1 {
		t.Fatalf("Expected 1 deleted box, got %d", count)
	}

	// Second insert with the same ID should NOT fail (INSERT OR IGNORE)
	err = queries.InsertDeletedBox(ctx, exedb.InsertDeletedBoxParams{
		ID:     boxID,
		UserID: userID,
	})
	if err != nil {
		t.Fatalf("Second InsertDeletedBox should not fail with INSERT OR IGNORE, but got: %v", err)
	}

	// Verify there's still only one entry
	err = db.QueryRow(`SELECT COUNT(*) FROM deleted_boxes WHERE id = ?`, boxID).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query deleted_boxes after second insert: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected still 1 deleted box after duplicate insert, got %d", count)
	}
}
