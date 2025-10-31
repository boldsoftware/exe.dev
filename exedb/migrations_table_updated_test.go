package exedb

import (
	"context"
	"database/sql"
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
