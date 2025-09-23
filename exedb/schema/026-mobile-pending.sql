-- Mobile pending VM creations (tie email verification to requested box)
CREATE TABLE IF NOT EXISTS mobile_pending_vm (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    hostname TEXT NOT NULL,
    description TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_mobile_pending_user ON mobile_pending_vm(user_id, created_at);

-- Record migration as executed
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (026, '026_mobile_pending');
