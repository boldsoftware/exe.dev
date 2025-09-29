package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"welcome.exe.dev/db/welcomedb"
)

func TestVisitors(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test.sqlite3")
	t.Cleanup(func() { os.Remove(tempDB) })

	db, err := Open(tempDB)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := RunMigrations(db); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	q := welcomedb.New(db)

	visitorID := "test:user123"
	now := time.Now()
	params := welcomedb.UpsertVisitorParams{
		ID:        visitorID,
		CreatedAt: now,
		LastSeen:  now,
	}

	if err := q.UpsertVisitor(t.Context(), params); err != nil {
		t.Fatalf("failed to upsert visitor: %v", err)
	}

	visitor, err := q.VisitorWithID(t.Context(), visitorID)
	if err != nil {
		t.Fatalf("failed to get visitor: %v", err)
	}

	if visitor.ID != visitorID {
		t.Errorf("expected visitor ID %s, got %s", visitorID, visitor.ID)
	}
	if visitor.ViewCount != 1 {
		t.Errorf("expected view count 1, got %d", visitor.ViewCount)
	}
}
