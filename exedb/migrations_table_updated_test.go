package exedb

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"exe.dev/sqlite"
	"exe.dev/tslog"
	_ "modernc.org/sqlite"
)

func TestRunMigrationsUpdateTable(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "migrations.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Fatalf("failed to close database: %v", cerr)
		}
	})

	t.Logf("using temporary database at %q", dbPath)

	if err := sqlite.InitDB(db, 1); err != nil {
		t.Fatalf("failed to initialize sqlite database: %v", err)
	}

	log := tslog.Slogger(t)

	if err := RunMigrations(log, db); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	expectedNames := migrationNamesFromFS(t)
	recordedMigrations := readMigrationsTable(t, db)

	// Verify all migration files have corresponding entries in the table.
	for _, name := range expectedNames {
		if _, ok := recordedMigrations[name]; !ok {
			t.Fatalf("migration %q missing from migrations table", name)
		}
	}

	if err := RunMigrations(log, db); err != nil {
		t.Fatalf("second migrations run failed: %v", err)
	}

	rerunMigrations := readMigrationsTable(t, db)
	if len(rerunMigrations) != len(recordedMigrations) {
		t.Fatalf("expected %d migrations after rerun, got %d", len(recordedMigrations), len(rerunMigrations))
	}

	for name, number := range recordedMigrations {
		rerunNumber, ok := rerunMigrations[name]
		if !ok {
			t.Fatalf("migration %q missing after rerun", name)
		}
		if rerunNumber != number {
			t.Fatalf("migration %q number changed from %d to %d after rerun", name, number, rerunNumber)
		}
	}
}

func TestRunMigrationsRepairsMissingBillingCredits(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "missing-billing-credits.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Fatalf("failed to close database: %v", cerr)
		}
	})

	if err := sqlite.InitDB(db, 1); err != nil {
		t.Fatalf("failed to initialize sqlite database: %v", err)
	}

	log := tslog.Slogger(t)

	if err := RunMigrations(log, db); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	if _, err := db.Exec("DROP TABLE billing_credits"); err != nil {
		t.Fatalf("failed to drop billing_credits: %v", err)
	}
	if _, err := db.Exec("DELETE FROM migrations WHERE migration_name IN ('085-account-credit-hourly-upsert.sql', '087-rename-account-credit-ledger-to-billing-credits.sql')"); err != nil {
		t.Fatalf("failed to delete migration 085/087 records: %v", err)
	}

	if err := RunMigrations(log, db); err != nil {
		t.Fatalf("failed to rerun migrations with missing billing_credits: %v", err)
	}

	var tableName string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='billing_credits'").Scan(&tableName)
	if err != nil {
		t.Fatalf("expected billing_credits table to exist after migration rerun: %v", err)
	}

	rows, err := db.Query("PRAGMA table_info(billing_credits)")
	if err != nil {
		t.Fatalf("failed to read billing_credits columns: %v", err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var (
			cid        int
			columnName string
			columnType string
			notNull    int
			defaultVal sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultVal, &primaryKey); err != nil {
			t.Fatalf("failed to scan billing_credits column info: %v", err)
		}
		columns[columnName] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("failed iterating billing_credits columns: %v", err)
	}

	for _, column := range []string{"id", "account_id", "amount", "stripe_event_id", "created_at", "hour_bucket", "credit_type"} {
		if !columns[column] {
			t.Fatalf("missing expected billing_credits column %q after migration rerun", column)
		}
	}

	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='index' AND name='idx_billing_credits_account_hour_type'").Scan(&tableName)
	if err != nil {
		t.Fatalf("expected idx_billing_credits_account_hour_type index to exist: %v", err)
	}

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM migrations WHERE migration_name = '085-account-credit-hourly-upsert.sql'").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query migration 085 record: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected migration 085 to be recorded once, got %d", count)
	}

	err = db.QueryRow("SELECT COUNT(*) FROM migrations WHERE migration_name = '087-rename-account-credit-ledger-to-billing-credits.sql'").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query migration 087 record: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected migration 087 to be recorded once, got %d", count)
	}
}

func TestNoDuplicateMigrationFilenames(t *testing.T) {
	t.Parallel()

	entries, err := migrationFS.ReadDir("schema")
	if err != nil {
		t.Fatalf("failed to read embedded migrations: %v", err)
	}

	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") || len(name) < 8 {
			continue
		}
		// Verify it matches the migration pattern
		if _, err := strconv.Atoi(name[:3]); err != nil {
			continue
		}
		if seen[name] {
			t.Fatalf("duplicate migration filename: %q", name)
		}
		seen[name] = true
	}
}

func TestDuplicateMigrationNumbersAllowed(t *testing.T) {
	t.Parallel()

	// Verify the migration system correctly sorts same-number migrations lexicographically.
	// This test just verifies the sort logic, not actual migration execution.
	migrations := []migration{
		{number: 100, name: "100-beta.sql"},
		{number: 100, name: "100-alpha.sql"},
		{number: 99, name: "099-first.sql"},
		{number: 101, name: "101-last.sql"},
	}

	sort.Slice(migrations, func(i, j int) bool {
		if migrations[i].number != migrations[j].number {
			return migrations[i].number < migrations[j].number
		}
		return migrations[i].name < migrations[j].name
	})

	expected := []string{"099-first.sql", "100-alpha.sql", "100-beta.sql", "101-last.sql"}
	for i, m := range migrations {
		if m.name != expected[i] {
			t.Fatalf("position %d: expected %q, got %q", i, expected[i], m.name)
		}
	}
}

func migrationNamesFromFS(t *testing.T) []string {
	t.Helper()

	entries, err := migrationFS.ReadDir("schema")
	if err != nil {
		t.Fatalf("failed to read embedded migrations: %v", err)
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			t.Fatalf("unexpected migration file name %q", name)
		}
		if len(name) < 8 { // 3 digits + hyphen + at least one char + .sql
			t.Fatalf("migration file name %q is too short", name)
		}

		if _, err := strconv.Atoi(name[:3]); err != nil {
			t.Fatalf("failed to parse migration number from %q: %v", name, err)
		}

		names = append(names, name)
	}

	sort.Strings(names)
	return names
}

func readMigrationsTable(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), "SELECT migration_name, migration_number FROM migrations")
	if err != nil {
		t.Fatalf("failed to query migrations table: %v", err)
	}
	defer rows.Close()

	migrations := make(map[string]int)
	for rows.Next() {
		var name string
		var number int
		if err := rows.Scan(&name, &number); err != nil {
			t.Fatalf("failed to scan migration row: %v", err)
		}
		migrations[name] = number
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("migrations row iteration failed: %v", err)
	}

	return migrations
}

func TestCodeMigrationRollback(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "rollback.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Fatalf("failed to close database: %v", cerr)
		}
	})

	if err := sqlite.InitDB(db, 1); err != nil {
		t.Fatalf("failed to initialize sqlite database: %v", err)
	}

	log := tslog.Slogger(t)

	// Run normal migrations first to set up the migrations table
	if err := RunMigrations(log, db); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Create a migration that makes changes then fails
	failingMigration := migration{
		number: 999,
		name:   "999-failing-migration.sql",
		isCode: true,
		codeFn: func(tx *sql.Tx) error {
			// Create a table - this should be rolled back
			_, err := tx.Exec(`CREATE TABLE rollback_test (id INTEGER PRIMARY KEY)`)
			if err != nil {
				return err
			}
			// Return an error to trigger rollback
			return errors.New("intentional failure")
		},
	}

	// Run the failing migration
	err = runMigration(log, db, failingMigration)
	if err == nil {
		t.Fatal("expected migration to fail")
	}
	if !strings.Contains(err.Error(), "intentional failure") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the table was NOT created (rolled back)
	var tableName string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='rollback_test'").Scan(&tableName)
	if err != sql.ErrNoRows {
		t.Fatalf("expected rollback_test table to not exist, but it does (err=%v)", err)
	}

	// Verify the migration was NOT recorded
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM migrations WHERE migration_name = '999-failing-migration.sql'").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query migrations table: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected migration 999 to not be recorded, but it was")
	}
}
