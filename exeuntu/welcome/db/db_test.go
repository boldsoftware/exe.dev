package db

import (
	"context"
	"os"
	"testing"
	"time"

	welcomedb "exe.dev/exeuntu/welcome/sqlc"
)

var ctx = context.Background()

func TestDatabaseCreationAndMigrations(t *testing.T) {
	// Use a temporary database file
	tempDB := t.TempDir() + "/test.sqlite3"
	defer os.Remove(tempDB)

	// Test opening database
	db, err := Open(tempDB)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Test running migrations
	if err := RunMigrations(db); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Verify the visitors table was created by trying to use it
	q := welcomedb.New(db)

	// Test inserting a visitor
	visitorID := "test:user123"
	email := "test@example.com"
	now := time.Now()
	params := welcomedb.UpsertVisitorParams{
		ID:        visitorID,
		Email:     &email,
		CreatedAt: now,
		LastSeen:  now,
	}

	if err := q.UpsertVisitor(ctx, params); err != nil {
		t.Fatalf("failed to upsert visitor: %v", err)
	}

	// Test retrieving the visitor
	visitor, err := q.GetVisitor(ctx, visitorID)
	if err != nil {
		t.Fatalf("failed to get visitor: %v", err)
	}

	if visitor.ID != visitorID {
		t.Errorf("expected visitor ID %s, got %s", visitorID, visitor.ID)
	}

	if visitor.Email == nil || *visitor.Email != email {
		t.Errorf("expected email %s, got %v", email, visitor.Email)
	}

	if visitor.ViewCount != 1 {
		t.Errorf("expected view count 1, got %d", visitor.ViewCount)
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	tempDB := t.TempDir() + "/test_fk.sqlite3"
	defer os.Remove(tempDB)

	db, err := Open(tempDB)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Check that foreign keys are enabled
	var fkEnabled int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled); err != nil {
		t.Fatalf("failed to check foreign_keys pragma: %v", err)
	}

	if fkEnabled != 1 {
		t.Errorf("expected foreign_keys to be enabled (1), got %d", fkEnabled)
	}
}
