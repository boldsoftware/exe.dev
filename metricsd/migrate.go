package metricsd

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	number int
	name   string
}

// RunMigrations applies all unapplied migrations to the DuckDB database.
// Migrations are numbered SQL files in the migrations/ directory.
// The first migration must create the migrations tracking table.
func RunMigrations(ctx context.Context, db *sql.DB) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations directory: %w", err)
	}

	pattern := regexp.MustCompile(`^(\d{3})-.*\.sql$`)
	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := pattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		number, err := strconv.Atoi(matches[1])
		if err != nil {
			return fmt.Errorf("parse migration number from %s: %w", entry.Name(), err)
		}
		migrations = append(migrations, migration{number: number, name: entry.Name()})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].number < migrations[j].number
	})

	// Check which migrations have already been applied.
	// The migrations table might not exist yet (fresh DB).
	applied := make(map[int]bool)
	if hasMigrationsTable(ctx, db) {
		rows, err := db.QueryContext(ctx, "SELECT migration_number FROM migrations")
		if err != nil {
			return fmt.Errorf("query applied migrations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var n int
			if err := rows.Scan(&n); err != nil {
				return fmt.Errorf("scan migration number: %w", err)
			}
			applied[n] = true
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate applied migrations: %w", err)
		}
	}

	for _, m := range migrations {
		if applied[m.number] {
			continue
		}
		if err := runMigration(ctx, db, m); err != nil {
			return err
		}
	}

	return nil
}

func hasMigrationsTable(ctx context.Context, db *sql.DB) bool {
	var name string
	err := db.QueryRowContext(ctx,
		"SELECT table_name FROM information_schema.tables WHERE table_name = 'migrations'",
	).Scan(&name)
	return err == nil
}

func runMigration(ctx context.Context, db *sql.DB, m migration) error {
	content, err := migrationFS.ReadFile("migrations/" + m.name)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", m.name, err)
	}

	slog.InfoContext(ctx, "running metricsd migration", "file", m.name, "number", m.number)

	// DuckDB doesn't support multi-statement transactions the same way SQLite does,
	// so execute each statement separately, then record.
	stmts := splitStatements(string(content))
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("execute migration %s: %w\nstatement: %s", m.name, err, stmt)
		}
	}

	// Record migration as applied
	if _, err := db.ExecContext(ctx,
		"INSERT INTO migrations (migration_number, migration_name) VALUES (?, ?)",
		m.number, m.name,
	); err != nil {
		return fmt.Errorf("record migration %s: %w", m.name, err)
	}

	return nil
}

// splitStatements splits SQL text on semicolons, trimming whitespace and
// dropping empty entries.
func splitStatements(sql string) []string {
	parts := strings.Split(sql, ";")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
