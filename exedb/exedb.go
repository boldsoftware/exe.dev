package exedb

import (
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
)

//go:embed schema/*.sql
var migrationFS embed.FS

// SSHDetails holds SSH connection information for a machine
type SSHDetails struct {
	Port       int
	PrivateKey string
	HostKey    string
	DockerHost *string // DOCKER_HOST value where this container runs
}

// RunMigrations executes database migrations in order
func RunMigrations(db *sql.DB) error {
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
	} else if err != sql.ErrNoRows {
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
