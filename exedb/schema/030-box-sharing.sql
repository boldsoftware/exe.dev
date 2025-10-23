-- Box sharing tables for sharing HTTPS proxy access

-- Pending shares: invitations sent to emails before user registration
CREATE TABLE pending_box_shares (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    box_id INTEGER NOT NULL,
    shared_with_email TEXT NOT NULL,
    shared_by_user_id TEXT NOT NULL,
    message TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE,
    FOREIGN KEY (shared_by_user_id) REFERENCES users(user_id) ON DELETE CASCADE,
    UNIQUE(box_id, shared_with_email)
);

CREATE INDEX idx_pending_box_shares_box ON pending_box_shares(box_id);
CREATE INDEX idx_pending_box_shares_email ON pending_box_shares(shared_with_email);

-- Box shares: active shares with registered users
CREATE TABLE box_shares (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    box_id INTEGER NOT NULL,
    shared_with_user_id TEXT NOT NULL,
    shared_by_user_id TEXT NOT NULL,
    message TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE,
    FOREIGN KEY (shared_with_user_id) REFERENCES users(user_id) ON DELETE CASCADE,
    FOREIGN KEY (shared_by_user_id) REFERENCES users(user_id) ON DELETE CASCADE,
    UNIQUE(box_id, shared_with_user_id)
);

CREATE INDEX idx_box_shares_box ON box_shares(box_id);
CREATE INDEX idx_box_shares_user ON box_shares(shared_with_user_id);

-- Share links: anonymous access via URL parameter
CREATE TABLE box_share_links (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    box_id INTEGER NOT NULL,
    share_token TEXT NOT NULL UNIQUE,
    created_by_user_id TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    use_count INTEGER DEFAULT 0,
    FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE,
    FOREIGN KEY (created_by_user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

CREATE INDEX idx_box_share_links_box ON box_share_links(box_id);
CREATE INDEX idx_box_share_links_token ON box_share_links(share_token);

-- Email rate limiting: track emails sent per user per day
CREATE TABLE user_daily_email_counts (
    user_id TEXT NOT NULL,
    date TEXT NOT NULL,
    email_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, date),
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

CREATE INDEX idx_daily_email_counts_user_date ON user_daily_email_counts(user_id, date);
