package exedb

//go:generate go tool sqlc generate

import (
	"bytes"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed schema/*.sql
var migrationFS embed.FS

// codeMigrations maps migration filenames to code migration functions.
// Each code migration must have a corresponding empty .sql file in schema/
// to establish ordering. Add entries directly to this map.
// Code migrations receive a transaction; they must not commit or rollback.
var codeMigrations = map[string]func(tx *sql.Tx) error{}

// SSHDetails holds SSH connection information for a machine
type SSHDetails struct {
	Port       int
	PrivateKey string
	HostKey    string
	Ctrhost    *string // Container host where this container runs
	User       string  // User to connect as (from Docker image USER directive)
}

// a migration represents a single migration (SQL or code).
type migration struct {
	number int
	name   string
	isCode bool
	codeFn func(tx *sql.Tx) error
}

// RunMigrations executes database migrations in order.
// SQL files establish ordering. Empty SQL files indicate code migrations.
// Migrations are sorted by number, then lexicographically for the same number.
// Each filename is migrated at most once.
func RunMigrations(slog *slog.Logger, db *sql.DB) error {
	// Read all migration files
	entries, err := migrationFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("failed to read schema directory: %w", err)
	}

	// Filter and validate migration files
	var migrations []migration
	migrationPattern := regexp.MustCompile(`^(\d{3})-.*\.sql$`)
	seenFilenames := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !migrationPattern.MatchString(entry.Name()) {
			continue
		}

		if seenFilenames[entry.Name()] {
			return fmt.Errorf("duplicate migration filename: %s", entry.Name())
		}
		seenFilenames[entry.Name()] = true

		matches := migrationPattern.FindStringSubmatch(entry.Name())
		number, err := strconv.Atoi(matches[1])
		if err != nil {
			return fmt.Errorf("failed to parse migration number from %s: %w", entry.Name(), err)
		}

		content, err := migrationFS.ReadFile("schema/" + entry.Name())
		if err != nil {
			return fmt.Errorf("failed to read migration file %s: %w", entry.Name(), err)
		}

		isEmpty := len(bytes.TrimSpace(content)) == 0
		codeFn, hasCode := codeMigrations[entry.Name()]

		if isEmpty && !hasCode {
			return fmt.Errorf("empty migration file %s has no corresponding code migration in codeMigrations[%q]", entry.Name(), entry.Name())
		}
		if !isEmpty && hasCode {
			return fmt.Errorf("migration file %s is not empty but has code migration in codeMigrations[%q]", entry.Name(), entry.Name())
		}

		migrations = append(migrations, migration{
			number: number,
			name:   entry.Name(),
			isCode: isEmpty,
			codeFn: codeFn,
		})
	}

	// Check for code migrations without corresponding SQL files
	for name := range codeMigrations {
		if !seenFilenames[name] {
			return fmt.Errorf("code migration %q has no corresponding SQL file in schema/", name)
		}
	}

	// Sort migrations by number, then lexicographically for the same number
	sort.Slice(migrations, func(i, j int) bool {
		if migrations[i].number != migrations[j].number {
			return migrations[i].number < migrations[j].number
		}
		return migrations[i].name < migrations[j].name
	})

	// Get executed migrations (tracked by filename)
	executedMigrations := make(map[string]bool)
	var tableName string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='migrations'").Scan(&tableName)
	if err == nil {
		// Migrations table exists, load executed migrations by name
		rows, err := db.Query("SELECT migration_name FROM migrations")
		if err != nil {
			return fmt.Errorf("failed to query executed migrations: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return fmt.Errorf("failed to scan migration name: %w", err)
			}
			executedMigrations[name] = true
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to check migrations table: %w", err)
	} else {
		// Migrations table doesn't exist - executedMigrations remains empty
		slog.Info("migrations table not found, running all migrations")
	}

	// Run any migrations that haven't been executed
	dbAlreadyExists := len(executedMigrations) > 0
	for _, m := range migrations {
		if executedMigrations[m.name] {
			continue
		}

		// Skip base migrations if the database already exists.
		// Base migrations are consolidated snapshots that include all prior migrations.
		// They should only run on fresh databases.
		if dbAlreadyExists && isBaseMigration(m.name) {
			slog.Info("skipping base migration on existing database", "file", m.name)
			continue
		}

		if err := runMigration(slog, db, m); err != nil {
			return err
		}
	}

	return nil
}

// runMigration executes a single migration (SQL or code) within a transaction,
// including recording it in the migrations table.
func runMigration(slog *slog.Logger, db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction for migration %s: %w", m.name, err)
	}
	defer tx.Rollback()

	if m.isCode {
		slog.Info("running code migration", "file", m.name, "number", m.number)
		if err := m.codeFn(tx); err != nil {
			return fmt.Errorf("failed to execute code migration %s: %w", m.name, err)
		}
	} else {
		slog.Info("running sql migration", "file", m.name, "number", m.number)
		if err := executeMigrationTx(tx, m.name); err != nil {
			return fmt.Errorf("failed to execute migration %s: %w", m.name, err)
		}
	}

	_, err = tx.Exec("INSERT INTO migrations (migration_name, migration_number) VALUES (?, ?)", m.name, m.number)
	if err != nil {
		return fmt.Errorf("failed to record migration %s in migrations table: %w", m.name, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit migration %s: %w", m.name, err)
	}
	return nil
}

// executeMigrationTx executes a single SQL migration file within the given transaction.
func executeMigrationTx(tx *sql.Tx, filename string) error {
	content, err := migrationFS.ReadFile("schema/" + filename)
	if err != nil {
		return fmt.Errorf("failed to read migration file %s: %w", filename, err)
	}

	_, err = tx.Exec(string(content))
	if err != nil {
		return fmt.Errorf("failed to execute migration %s: %w", filename, err)
	}

	return nil
}

// isBaseMigration reports whether filename indicates a base migration (e.g., "078-base.sql").
// Base migrations are consolidated schema snapshots (e.g., "078-base.sql").
func isBaseMigration(filename string) bool {
	return strings.HasSuffix(filename, "-base.sql")
}
