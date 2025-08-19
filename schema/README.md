# Database Schema Migrations

This directory contains database migration files for exe.dev.

## How to Add a Migration

1. Create a new file with the naming convention `XXX_description.sql` where:
   - `XXX` is a 3-digit number in the range 001-999
   - `description` is a brief description of what the migration does
   - Example: `003_add_user_preferences.sql`

2. The migration number must be unique (see test in `migrations_test.go`)

3. Write your SQL statements in the file. The migration system will execute the entire file as-is.

4. Migrations are automatically executed in numerical order when the server starts

## Migration System Behavior

- On first run (when the `migrations` table doesn't exist), all migrations are executed
- On subsequent runs, only new migrations that haven't been executed are run
- The system tracks executed migrations in the `migrations` table
- Migrations are executed in alphabetical order of filenames

## Important Notes

- Never edit existing migration files after they've been deployed
- Always add new migrations with higher numbers
- Migration files are embedded in the Go binary at build time
- Each migration should be idempotent when possible
- Use `IF NOT EXISTS` clauses for CREATE statements when appropriate
