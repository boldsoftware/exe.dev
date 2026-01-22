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

	expectedNumbers := migrationNumbersFromFS(t)
	recordedMigrations := readMigrationsTable(t, db)

	if len(recordedMigrations) != len(expectedNumbers) {
		t.Fatalf("expected %d migrations recorded, got %d", len(expectedNumbers), len(recordedMigrations))
	}

	for _, number := range expectedNumbers {
		if _, ok := recordedMigrations[number]; !ok {
			t.Fatalf("migration %03d missing from migrations table", number)
		}
	}

	if err := RunMigrations(log, db); err != nil {
		t.Fatalf("second migrations run failed: %v", err)
	}

	rerunMigrations := readMigrationsTable(t, db)
	if len(rerunMigrations) != len(recordedMigrations) {
		t.Fatalf("expected %d migrations after rerun, got %d", len(recordedMigrations), len(rerunMigrations))
	}

	for number, name := range recordedMigrations {
		rerunName, ok := rerunMigrations[number]
		if !ok {
			t.Fatalf("migration %03d missing after rerun", number)
		}
		if rerunName != name {
			t.Fatalf("migration %03d name changed from %q to %q after rerun", number, name, rerunName)
		}
	}
}

func migrationNumbersFromFS(t *testing.T) []int {
	t.Helper()

	entries, err := migrationFS.ReadDir("schema")
	if err != nil {
		t.Fatalf("failed to read embedded migrations: %v", err)
	}

	var numbers []int
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

		number, err := strconv.Atoi(name[:3])
		if err != nil {
			t.Fatalf("failed to parse migration number from %q: %v", name, err)
		}

		numbers = append(numbers, number)
	}

	sort.Ints(numbers)
	return numbers
}

func readMigrationsTable(t *testing.T, db *sql.DB) map[int]string {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), "SELECT migration_number, migration_name FROM migrations")
	if err != nil {
		t.Fatalf("failed to query migrations table: %v", err)
	}
	defer rows.Close()

	migrations := make(map[int]string)
	for rows.Next() {
		var number int
		var name string
		if err := rows.Scan(&number, &name); err != nil {
			t.Fatalf("failed to scan migration row: %v", err)
		}
		migrations[number] = name
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
	err = db.QueryRow("SELECT COUNT(*) FROM migrations WHERE migration_number = 999").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query migrations table: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected migration 999 to not be recorded, but it was")
	}
}

func TestCodeMigrationRuns(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "code_migrations.db")
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

	// Verify the code migration ran by checking for its table and data
	var message string
	err = db.QueryRow("SELECT message FROM code_migration_test WHERE id = 1").Scan(&message)
	if err != nil {
		t.Fatalf("failed to query code_migration_test table: %v", err)
	}
	if message != "code migration ran successfully" {
		t.Fatalf("unexpected message: %q", message)
	}

	// Verify the code migration is recorded in the migrations table
	migrations := readMigrationsTable(t, db)
	if name, ok := migrations[60]; !ok {
		t.Fatal("code migration 060 not recorded in migrations table")
	} else if name != "060-test-code-migration.sql" {
		t.Fatalf("unexpected migration name: %q", name)
	}

	// Verify running migrations again doesn't re-run the code migration
	if err := RunMigrations(log, db); err != nil {
		t.Fatalf("second migrations run failed: %v", err)
	}

	// Check the table still has exactly one row (not duplicated)
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM code_migration_test").Scan(&count)
	if err != nil {
		t.Fatalf("failed to count code_migration_test rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row in code_migration_test, got %d", count)
	}
}
