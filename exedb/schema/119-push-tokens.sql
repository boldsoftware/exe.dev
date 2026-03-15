CREATE TABLE push_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    token TEXT NOT NULL,
    platform TEXT NOT NULL DEFAULT 'apns',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_push_tokens_token ON push_tokens(token);
CREATE INDEX idx_push_tokens_user ON push_tokens(user_id);
