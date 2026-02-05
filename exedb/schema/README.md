# Database Schema Migrations

This directory contains database migration files for exe.dev.

## How to Add a SQL Migration

1. Create a new file with the naming convention `XXX-description.sql` where:
   - `XXX` is a 3-digit number higher than the current highest
   - `description` is a brief description of what the migration does
   - Example: `079-add-user-preferences.sql`

2. The migration number must be unique (see test in `migrations_test.go`)

3. Write your SQL statements in the file. The migration system will execute the entire file as-is.

4. Migrations are automatically executed in numerical order when the server starts

## How to Add a Code Migration

For complex migrations that require Go logic (e.g., computing derived values from
existing data), use a code migration:

1. Create an **empty** SQL file in this directory: `XXX-description.sql`
   - This establishes the migration's position in the sequence

2. Create a Go file in the `exedb` package with the same name but `.go` extension:
   - SQL file: `schema/079-backfill-something.sql` (empty)
   - Go file: `exedb/079-backfill-something.go`

3. Add an entry to `codeMigrations` in `exedb.go`:

```go
var codeMigrations = map[int]func(tx *sql.Tx) error{
    79: backfillSomething,  // add your entry here
}
```

4. Define the migration function in your Go file:

```go
package exedb

import "database/sql"

func backfillSomething(tx *sql.Tx) error {
    // Migration logic here using tx
    // Do NOT commit or rollback - the framework handles that
    return nil
}
```

Code migrations receive a transaction that includes recording the migration.
Do not commit or rollback; the framework handles that. Use error returns to signal failure.

## Migration System Behavior

- On first run (when the `migrations` table doesn't exist), all migrations are executed
- On subsequent runs, only new migrations that haven't been executed are run
- The system tracks executed migrations in the `migrations` table
- Migrations are executed in numerical order
- Empty SQL files require a corresponding `codeMigrations` entry
- Non-empty SQL files must not have a `codeMigrations` entry
- **Base migrations** (files ending in `-base.sql`) are skipped on existing databases

## Consolidating Migrations

To speed up test runs, you can consolidate all migrations into a single base file:

```bash
go run ./cmd/consolidate-migrations
```

This tool:
1. Creates a temporary database and runs all migrations
2. Dumps the schema to a new base file (e.g., `078-base.sql`, numbered after the last migration)
3. Pre-populates the migrations table with all consolidated migration records
4. Deletes the old migration files
5. Deletes code migration Go files
6. Clears the `codeMigrations` map in `exedb.go`

The generated base migration is automatically skipped on existing databases
(RunMigrations skips `-base.sql` files when any migrations have already been executed).

**Important**: Before consolidating, ensure production has all migrations that will be deleted.

## Important Notes

- Never edit existing migration files after they've been deployed
- Always add new migrations with higher numbers than the current base
- SQL migration files are embedded in the Go binary at build time
- Each migration should be idempotent when possible
- Use `IF NOT EXISTS` clauses for CREATE statements when appropriate
