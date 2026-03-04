-- Switch migrations table primary key from migration_number to migration_name.
-- This allows multiple migrations to share the same number prefix.
CREATE TABLE migrations_new (
    migration_name TEXT PRIMARY KEY,
    migration_number INTEGER NOT NULL,
    executed_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO migrations_new (migration_name, migration_number, executed_at)
    SELECT migration_name, migration_number, executed_at FROM migrations;
DROP TABLE migrations;
ALTER TABLE migrations_new RENAME TO migrations;
