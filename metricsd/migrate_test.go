package metricsd

import (
	"context"
	"testing"
)

func TestRunMigrations(t *testing.T) {
	ctx := context.Background()

	connector, db, err := OpenDB(ctx, "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	// Verify migrations table exists and has entries
	var count int
	err = db.QueryRowContext(ctx, "SELECT count(*) FROM migrations").Scan(&count)
	if err != nil {
		t.Fatalf("query migrations: %v", err)
	}
	if count < 2 {
		t.Errorf("expected at least 2 migrations, got %d", count)
	}

	// Verify resource_group column exists
	var rg string
	err = db.QueryRowContext(ctx,
		"SELECT column_name FROM information_schema.columns WHERE table_name = 'vm_metrics' AND column_name = 'resource_group'",
	).Scan(&rg)
	if err != nil {
		t.Fatalf("resource_group column not found: %v", err)
	}
}

func TestRunMigrations_Idempotent(t *testing.T) {
	ctx := context.Background()

	connector, db, err := OpenDB(ctx, "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	// Running migrations again should be a no-op
	if err := RunMigrations(ctx, db); err != nil {
		t.Fatalf("RunMigrations second time: %v", err)
	}

	var count int
	err = db.QueryRowContext(ctx, "SELECT count(*) FROM migrations").Scan(&count)
	if err != nil {
		t.Fatalf("query migrations: %v", err)
	}
	if count != 2 {
		t.Errorf("expected exactly 2 migrations after idempotent run, got %d", count)
	}
}
