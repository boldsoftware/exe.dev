package exedb

//go:generate go tool sqlc generate

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"

	"exe.dev/sqlite"
)

//go:embed schema/*.sql
var migrationFS embed.FS

// codeMigrations maps migration numbers to code migration functions.
// Each code migration must have a corresponding empty .sql file in schema/
// to establish ordering. Add entries directly to this map.
// Code migrations receive a transaction; they must not commit or rollback.
var codeMigrations = map[int]func(tx *sql.Tx) error{
	60: testCodeMigration,
	62: backfillSSHFingerprints,
}

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
func RunMigrations(slog *slog.Logger, db *sql.DB) error {
	// Read all migration files
	entries, err := migrationFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("failed to read schema directory: %w", err)
	}

	// Filter and validate migration files
	var migrations []migration
	migrationPattern := regexp.MustCompile(`^(\d{3})-.*\.sql$`)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !migrationPattern.MatchString(entry.Name()) {
			continue
		}

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
		codeFn, hasCode := codeMigrations[number]

		if isEmpty && !hasCode {
			return fmt.Errorf("empty migration file %s has no corresponding code migration in codeMigrations[%d]", entry.Name(), number)
		}
		if !isEmpty && hasCode {
			return fmt.Errorf("migration file %s is not empty but has code migration in codeMigrations[%d]", entry.Name(), number)
		}

		migrations = append(migrations, migration{
			number: number,
			name:   entry.Name(),
			isCode: isEmpty,
			codeFn: codeFn,
		})
	}

	// Check for code migrations without corresponding SQL files
	sqlNumbers := make(map[int]bool)
	for _, m := range migrations {
		sqlNumbers[m.number] = true
	}
	for number := range codeMigrations {
		if !sqlNumbers[number] {
			return fmt.Errorf("code migration %d has no corresponding SQL file in schema/", number)
		}
	}

	// Sort migrations by number
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].number < migrations[j].number
	})

	// Get executed migrations
	executedMigrations := make(map[int]bool)
	var tableName string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='migrations'").Scan(&tableName)
	if err == nil {
		// Migrations table exists, load executed migrations
		rows, err := db.Query("SELECT migration_number FROM migrations")
		if err != nil {
			return fmt.Errorf("failed to query executed migrations: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var migrationNumber int
			if err := rows.Scan(&migrationNumber); err != nil {
				return fmt.Errorf("failed to scan migration number: %w", err)
			}
			executedMigrations[migrationNumber] = true
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to check migrations table: %w", err)
	} else {
		// Migrations table doesn't exist - executedMigrations remains empty
		slog.Info("migrations table not found, running all migrations")
	}

	// Run any migrations that haven't been executed
	for _, m := range migrations {
		if executedMigrations[m.number] {
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

	_, err = tx.Exec("INSERT INTO migrations (migration_number, migration_name) VALUES (?, ?)", m.number, m.name)
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

// InitDataSubdir ensures data_subdir is set in db's server_meta,
// creating a random subdirectory name if it doesn't exist.
func InitDataSubdir(log *slog.Logger, db *sqlite.DB) (string, error) {
	var dataSubdir string

	// Use a transaction to read and potentially write
	err := db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		// Try to get existing data_subdir
		err := tx.QueryRow("SELECT value FROM server_meta WHERE key = ?", "data_subdir").Scan(&dataSubdir)
		if err == nil {
			if dataSubdir == "" {
				return fmt.Errorf("data_subdir is empty in server_meta")
			}
			log.DebugContext(ctx, "using existing data_subdir", "subdir", dataSubdir)
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("failed to query data_subdir from server_meta: %w", err)
		}

		// Not found. Create one.
		dataSubdir = crand.Text()
		_, err = tx.Exec(`
				INSERT INTO server_meta (key, value, updated_at)
				VALUES (?, ?, CURRENT_TIMESTAMP)
				ON CONFLICT(key) DO UPDATE SET
					value = excluded.value,
					updated_at = CURRENT_TIMESTAMP
			`, "data_subdir", dataSubdir)
		if err != nil {
			return fmt.Errorf("failed to set data_subdir in server_meta: %w", err)
		}

		log.InfoContext(ctx, "initialized new data_subdir", "subdir", dataSubdir)
		return nil
	})
	if err != nil {
		return "", err
	}
	return dataSubdir, nil
}
