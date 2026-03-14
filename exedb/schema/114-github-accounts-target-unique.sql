-- Deduplicate github_accounts rows that share the same target_login,
-- keeping the row with the highest (most recent) installation_id.
DELETE FROM github_accounts
WHERE rowid NOT IN (
    SELECT MAX(rowid) FROM github_accounts
    GROUP BY user_id, target_login
);

-- Prevent future duplicates: one installation per target account per user.
CREATE UNIQUE INDEX IF NOT EXISTS idx_github_accounts_user_target
    ON github_accounts(user_id, target_login);
