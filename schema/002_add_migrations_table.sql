-- Add migrations table to track which migrations have been executed
CREATE TABLE IF NOT EXISTS migrations (
    migration_number INTEGER PRIMARY KEY,
    migration_name TEXT NOT NULL,
    executed_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Insert migration entries into the table
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (001, '001_base');
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (002, '002_add_migrations_table');
