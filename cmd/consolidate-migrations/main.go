// Command consolidate-migrations creates a new base schema file from all
// existing migrations and deletes the old migration files.
//
// Usage: go run ./cmd/consolidate-migrations
//
// This tool:
// 1. Creates a temporary SQLite database
// 2. Runs all migrations to build the full schema
// 3. Dumps the schema to a new base file (numbered after the last migration)
// 4. Includes INSERT statements for all prior migration records
// 5. Deletes the old migration files
// 6. Deletes code migration Go files
// 7. Clears the codeMigrations map in exedb.go
//
// The generated base migration is automatically skipped on existing databases
// (RunMigrations skips base migrations when the database already has migrations).
//
// After running, the user must:
// - Ensure prod has all migrations that are being deleted
// - Commit the changes
package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// Find the schema directory
	schemaDir := "exedb/schema"
	if _, err := os.Stat(schemaDir); os.IsNotExist(err) {
		return fmt.Errorf("schema directory not found at %s - run from repo root", schemaDir)
	}

	// Find all migration files
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		return fmt.Errorf("failed to read schema directory: %w", err)
	}

	migrationPattern := regexp.MustCompile(`^(\d{3})-.*\.sql$`)
	var migrations []struct {
		number int
		name   string
		path   string
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := migrationPattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		number, _ := strconv.Atoi(matches[1])
		migrations = append(migrations, struct {
			number int
			name   string
			path   string
		}{
			number: number,
			name:   entry.Name(),
			path:   filepath.Join(schemaDir, entry.Name()),
		})
	}

	if len(migrations) == 0 {
		return fmt.Errorf("no migration files found")
	}

	// Sort by migration number
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].number < migrations[j].number
	})

	lastMigration := migrations[len(migrations)-1]
	fmt.Printf("Found %d migrations, last is %s\n", len(migrations), lastMigration.name)

	// Create temp directory and database
	tmpDir, err := os.MkdirTemp("", "consolidate-migrations-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Run all migrations manually (we can't import exedb due to circular deps)
	// First, find code migration files to know which ones are code-only
	codeMigrationPattern := regexp.MustCompile(`^(\d{3})-.*\.go$`)
	codeMigrationNumbers := make(map[int]bool)
	exedbEntries, err := os.ReadDir("exedb")
	if err != nil {
		return fmt.Errorf("failed to read exedb directory: %w", err)
	}
	for _, entry := range exedbEntries {
		matches := codeMigrationPattern.FindStringSubmatch(entry.Name())
		if matches != nil {
			number, _ := strconv.Atoi(matches[1])
			codeMigrationNumbers[number] = true
		}
	}

	fmt.Println("Running SQL migrations...")
	for _, m := range migrations {
		content, err := os.ReadFile(m.path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", m.name, err)
		}

		// Skip empty files (code migrations) - their schema effects are already
		// captured in subsequent SQL migrations or the base
		if len(bytes.TrimSpace(content)) == 0 {
			fmt.Printf("  Skipping code migration %s\n", m.name)
			continue
		}

		fmt.Printf("  Running %s\n", m.name)
		if _, err := db.Exec(string(content)); err != nil {
			return fmt.Errorf("failed to run migration %s: %w", m.name, err)
		}
	}
	db.Close()

	// Use sqlite3 to dump schema
	fmt.Println("Dumping schema...")
	cmd := exec.Command("sqlite3", dbPath, ".schema")
	schemaBytes, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to dump schema: %w", err)
	}

	// The new base migration number is one after the last migration
	newBaseNumber := lastMigration.number + 1

	// Build the new base file
	var buf bytes.Buffer
	buf.WriteString("-- Base schema snapshot created by consolidate-migrations.\n")
	buf.WriteString(fmt.Sprintf("-- Consolidates migrations 001 through %03d.\n", lastMigration.number))
	buf.WriteString("-- This migration is skipped on existing databases (only runs on fresh databases).\n")
	buf.WriteString("-- All schema changes must be done via new migrations.\n\n")

	// Write the schema, cleaning up SQLite artifacts
	schema := string(schemaBytes)

	// Remove sqlite_sequence table (internal SQLite tracking table)
	schema = regexp.MustCompile(`(?m)^CREATE TABLE sqlite_sequence\([^)]+\);?\n?`).ReplaceAllString(schema, "")

	// Fix foreign key references that point to old table names due to ALTER TABLE RENAME
	// SQLite preserves the original table name in foreign key references
	schema = strings.ReplaceAll(schema, `"original_boxes"`, `boxes`)

	schema = strings.TrimSpace(schema)
	buf.WriteString(schema)
	buf.WriteString("\n\n")

	// Add migration records so the migrations table is pre-populated.
	// Include ALL consolidated migrations so they're recorded in fresh databases.
	buf.WriteString("-- Pre-populate migrations table with all consolidated migrations.\n")
	for _, m := range migrations {
		buf.WriteString(fmt.Sprintf("INSERT INTO migrations (migration_name, migration_number) VALUES ('%s', %d);\n",
			m.name, m.number))
	}

	// Write the new base file
	newBaseName := fmt.Sprintf("%03d-base.sql", newBaseNumber)
	newBasePath := filepath.Join(schemaDir, newBaseName)
	if err := os.WriteFile(newBasePath, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("failed to write new base file: %w", err)
	}
	fmt.Printf("Created %s\n", newBasePath)

	// Delete old migration files
	fmt.Println("Deleting old migration files...")
	for _, m := range migrations {
		fmt.Printf("  Deleting %s\n", m.name)
		if err := os.Remove(m.path); err != nil {
			return fmt.Errorf("failed to delete %s: %w", m.path, err)
		}
	}

	// Delete code migration Go files
	fmt.Println("Deleting code migration Go files...")
	for _, entry := range exedbEntries {
		matches := codeMigrationPattern.FindStringSubmatch(entry.Name())
		if matches != nil {
			path := filepath.Join("exedb", entry.Name())
			fmt.Printf("  Deleting %s\n", entry.Name())
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("failed to delete %s: %w", path, err)
			}
		}
	}

	// Update codeMigrations map in exedb.go
	fmt.Println("Clearing codeMigrations map in exedb.go...")
	exedbGoPath := "exedb/exedb.go"
	exedbContent, err := os.ReadFile(exedbGoPath)
	if err != nil {
		return fmt.Errorf("failed to read exedb.go: %w", err)
	}

	// Replace the codeMigrations map with an empty one
	// Pattern: var codeMigrations = map[int]func(tx *sql.Tx) error{ ... }
	codeMigrationsRe := regexp.MustCompile(`(?s)(var codeMigrations = map\[string\]func\(tx \*sql\.Tx\) error)\{[^}]*\}`)
	newContent := codeMigrationsRe.ReplaceAll(exedbContent, []byte("$1{}"))

	if bytes.Equal(exedbContent, newContent) {
		fmt.Println("  Warning: codeMigrations map not found or already empty")
	} else {
		if err := os.WriteFile(exedbGoPath, newContent, 0o644); err != nil {
			return fmt.Errorf("failed to write exedb.go: %w", err)
		}
		fmt.Println("  Cleared codeMigrations map")
	}

	fmt.Println()
	fmt.Println("Done! Next steps:")
	fmt.Println("1. Ensure prod has all migrations that were just deleted")
	fmt.Println("2. Review the generated base file")
	fmt.Println("3. Run tests to verify: go test ./exedb/...")
	fmt.Println("4. Commit the changes")

	return nil
}
