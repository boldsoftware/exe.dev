package exedb

//go:generate go tool sqlc generate

import (
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

// SSHDetails holds SSH connection information for a machine
type SSHDetails struct {
	Port       int
	PrivateKey string
	HostKey    string
	Ctrhost    *string // Container host where this container runs
	User       string  // User to connect as (from Docker image USER directive)
}

// RunMigrations executes database migrations in order
func RunMigrations(slog *slog.Logger, db *sql.DB) error {
	// Read all migration files
	entries, err := migrationFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("failed to read schema directory: %w", err)
	}

	// Filter and validate migration files
	var migrations []string
	migrationPattern := regexp.MustCompile(`^(\d{3})-.*\.sql$`)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !migrationPattern.MatchString(entry.Name()) {
			continue
		}
		migrations = append(migrations, entry.Name())
	}

	// Sort migrations by number
	sort.Strings(migrations)

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
	for _, migration := range migrations {
		// Extract migration number from filename (e.g., "001-base.sql" -> 001)
		matches := migrationPattern.FindStringSubmatch(migration)
		if len(matches) != 2 {
			return fmt.Errorf("invalid migration filename format: %s", migration)
		}

		migrationNumber, err := strconv.Atoi(matches[1])
		if err != nil {
			return fmt.Errorf("failed to parse migration number from %s: %w", migration, err)
		}

		if !executedMigrations[migrationNumber] {
			slog.Info("running migration", "file", migration, "number", migrationNumber)
			if err := executeMigration(db, migration); err != nil {
				return fmt.Errorf("failed to execute migration %s: %w", migration, err)
			}

			_, err = db.Exec("INSERT INTO migrations (migration_number, migration_name) VALUES (?, ?)", migrationNumber, migration)
			if err != nil {
				return fmt.Errorf("failed to record migration %s in migrations table: %w", migration, err)
			}
		}
	}

	return nil
}

// executeMigration executes a single migration file
func executeMigration(db *sql.DB, filename string) error {
	content, err := migrationFS.ReadFile("schema/" + filename)
	if err != nil {
		return fmt.Errorf("failed to read migration file %s: %w", filename, err)
	}

	_, err = db.Exec(string(content))
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
