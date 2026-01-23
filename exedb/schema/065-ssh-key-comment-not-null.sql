-- Make comment NOT NULL with empty string default (after backfill ensures no NULLs)
-- SQLite doesn't support ALTER COLUMN, so we need to recreate the table
CREATE TABLE ssh_keys_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    public_key TEXT UNIQUE NOT NULL,
    added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    comment TEXT NOT NULL DEFAULT '',
    fingerprint TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

INSERT INTO ssh_keys_new (id, user_id, public_key, added_at, last_used_at, comment, fingerprint)
SELECT id, user_id, public_key, added_at, last_used_at, comment, fingerprint
FROM ssh_keys;

DROP TABLE ssh_keys;
ALTER TABLE ssh_keys_new RENAME TO ssh_keys;

-- Recreate indexes
CREATE INDEX idx_ssh_keys_user_id ON ssh_keys(user_id);
CREATE UNIQUE INDEX idx_ssh_keys_public_key ON ssh_keys(public_key);
CREATE INDEX idx_ssh_keys_fingerprint ON ssh_keys(fingerprint);
