-- Server metadata key-value table for server-scoped configuration
CREATE TABLE IF NOT EXISTS server_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Index for the migrations table
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (018, '018-server-meta');