-- Normalize GitHub accounts: split into github_user_tokens (one row per user+login)
-- and github_installations (one row per user+installation).
-- This fixes token rotation bugs caused by duplicating tokens across rows.

-- 1. Create github_user_tokens table.
CREATE TABLE github_user_tokens (
    user_id TEXT NOT NULL,
    github_login TEXT NOT NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT NOT NULL DEFAULT '',
    token_renewed_at DATETIME,
    access_token_expires_at DATETIME,
    refresh_token_expires_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, github_login),
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

-- 2. Populate github_user_tokens from github_accounts.
-- For each (user_id, github_login) group, pick the row with the most recent
-- token_renewed_at (using created_at as tiebreaker).
INSERT INTO github_user_tokens (user_id, github_login, access_token, refresh_token, token_renewed_at, access_token_expires_at, refresh_token_expires_at, created_at)
SELECT user_id, github_login, access_token, refresh_token, token_renewed_at, access_token_expires_at, refresh_token_expires_at, created_at
FROM github_accounts
WHERE rowid IN (
    SELECT rowid FROM github_accounts ga
    WHERE ga.rowid = (
        SELECT g2.rowid FROM github_accounts g2
        WHERE g2.user_id = ga.user_id AND g2.github_login = ga.github_login
        ORDER BY g2.token_renewed_at DESC NULLS LAST, g2.created_at DESC NULLS LAST
        LIMIT 1
    )
);

-- 3. Create github_installations table.
CREATE TABLE github_installations (
    user_id TEXT NOT NULL,
    github_login TEXT NOT NULL,
    github_app_installation_id INTEGER NOT NULL,
    github_account_login TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, github_app_installation_id),
    FOREIGN KEY (user_id, github_login) REFERENCES github_user_tokens(user_id, github_login) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_github_installations_user_account
    ON github_installations(user_id, github_account_login);

-- 4. Populate github_installations from github_accounts.
INSERT INTO github_installations (user_id, github_login, github_app_installation_id, github_account_login, created_at)
SELECT user_id, github_login, installation_id, target_login, created_at
FROM github_accounts;

-- 5. Drop the old table.
DROP TABLE github_accounts;
