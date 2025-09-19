-- Create billing_accounts table to separate billing concerns from resource allocation
CREATE TABLE IF NOT EXISTS billing_accounts (
    billing_account_id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    billing_email TEXT,
    stripe_customer_id TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Create index on stripe_customer_id for lookups
CREATE INDEX IF NOT EXISTS idx_billing_accounts_stripe_customer ON billing_accounts(stripe_customer_id);

-- Add billing_account_id reference to allocs table
-- Note: This may fail if the column already exists, but that's expected during development
-- In production, migrations should only run once
ALTER TABLE allocs ADD COLUMN billing_account_id TEXT NOT NULL REFERENCES billing_accounts(billing_account_id);
ALTER TABLE allocs DROP COLUMN billing_email; -- Moved to billing_accounts
ALTER TABLE allocs DROP COLUMN stripe_customer_id; -- Moved to billing_accounts
-- Create index on billing_account_id in allocs for efficient lookups
CREATE INDEX IF NOT EXISTS idx_allocs_billing_account ON allocs(billing_account_id);

ALTER TABLE users ADD COLUMN default_billing_account_id TEXT NOT NULL REFERENCES billing_accounts(billing_account_id);
CREATE INDEX IF NOT EXISTS idx_users_default_billing_account ON users(default_billing_account_id);

-- Create credits table for usage credit tracking
CREATE TABLE IF NOT EXISTS usage_credits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    billing_account_id TEXT NOT NULL,
    amount REAL NOT NULL,
    payment_method TEXT NOT NULL,
    payment_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    data TEXT, -- JSON for additional payment-specific data
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (billing_account_id) REFERENCES billing_accounts(billing_account_id) ON DELETE CASCADE
);

-- Create index on billing_account_id for efficient balance queries
CREATE INDEX IF NOT EXISTS idx_usage_credits_billing_account ON usage_credits(billing_account_id);

-- Create debits table for usage debit tracking
CREATE TABLE IF NOT EXISTS usage_debits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    billing_account_id TEXT NOT NULL,
    model TEXT NOT NULL,
    message_id TEXT NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (billing_account_id) REFERENCES billing_accounts(billing_account_id) ON DELETE CASCADE
);

-- Create index on billing_account_id for efficient balance queries
CREATE INDEX IF NOT EXISTS idx_usage_debits_billing_account ON usage_debits(billing_account_id);

-- Create index on message_id for deduplication
CREATE INDEX IF NOT EXISTS idx_usage_debits_message_id ON usage_debits(message_id);

-- Record this migration as completed
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (023, '023-billing-accounts');