-- Support multiple GitHub App installations per user.
-- Each installation is on a different account (personal or org).
-- target_login is the GitHub account/org the app was installed on.

CREATE TABLE github_accounts_new (
    user_id TEXT NOT NULL,
    github_login TEXT NOT NULL,
    installation_id INTEGER NOT NULL,
    target_login TEXT NOT NULL DEFAULT '',
    access_token TEXT NOT NULL,
    refresh_token TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, installation_id),
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

INSERT INTO github_accounts_new (user_id, github_login, installation_id, target_login, access_token, refresh_token, created_at)
    SELECT user_id, github_login, installation_id, '', access_token, refresh_token, created_at
    FROM github_accounts;

DROP TABLE github_accounts;
ALTER TABLE github_accounts_new RENAME TO github_accounts;
