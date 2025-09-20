-- Waitlist table for capturing early interest
CREATE TABLE IF NOT EXISTS waitlist (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL,
    remote_ip TEXT,
    json TEXT, -- JSON blob, e.g. {"meaning": ["Joy", ...]}
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_waitlist_email ON waitlist(email);

-- Record this migration as completed
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (024, '024-waitlist');
