CREATE TABLE proxy_bearer_tokens (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    box_id INTEGER NOT NULL,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE,
    FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE
);

CREATE INDEX idx_proxy_bearer_tokens_box_id ON proxy_bearer_tokens(box_id);
CREATE INDEX idx_proxy_bearer_tokens_user_id ON proxy_bearer_tokens(user_id);
CREATE INDEX idx_proxy_bearer_tokens_expires_at ON proxy_bearer_tokens(expires_at);
