# Database Schema Migrations

This directory contains database migration files for exe.dev.

## How to Add a SQL Migration

1. Create a new file with the naming convention `XXX-description.sql` where:
   - `XXX` is a 3-digit number in the range 001-999
   - `description` is a brief description of what the migration does
   - Example: `003-add-user-preferences.sql`

2. The migration number must be unique (see test in `migrations_test.go`)

3. Write your SQL statements in the file. The migration system will execute the entire file as-is.

4. Migrations are automatically executed in numerical order when the server starts

## How to Add a Code Migration

For complex migrations that require Go logic (e.g., computing derived values from
existing data), use a code migration:

1. Create an **empty** SQL file in this directory: `XXX-description.sql`
   - This establishes the migration's position in the sequence

2. Create a Go file in the `exedb` package with the same name but `.go` extension:
   - SQL file: `schema/061-add-ssh-fingerprints.sql` (empty)
   - Go file: `exedb/061-add-ssh-fingerprints.go`

3. Add an entry to `codeMigrations` in `exedb.go`:

```go
var codeMigrations = map[int]func(tx *sql.Tx) error{
    60: testCodeMigration,
    61: addSSHFingerprints,  // add your entry here
}
```

4. Define the migration function in your Go file:

```go
package exedb

import "database/sql"

func addSSHFingerprints(tx *sql.Tx) error {
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

## Important Notes

- Never edit existing migration files after they've been deployed
- Always add new migrations with higher numbers
- SQL migration files are embedded in the Go binary at build time
- Each migration should be idempotent when possible
- Use `IF NOT EXISTS` clauses for CREATE statements when appropriate
