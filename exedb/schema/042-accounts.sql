-- accounts table stores billing/organization accounts
--
-- TODO: Add account_admins join table for multi-admin support. The created_by
-- column is a temporary artifact until then.
CREATE TABLE accounts (
    id TEXT PRIMARY KEY,
    created_by TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (created_by) REFERENCES users(user_id)
);
