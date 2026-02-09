-- account_credit_ledger is an append-only ledger tracking credit transactions.
-- Balance = SUM(amount) for a given account_id.
-- Positive amounts are credit purchases, negative amounts are usage deductions.
-- stripe_event_id provides idempotency for Stripe-sourced credits.
CREATE TABLE account_credit_ledger (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id TEXT NOT NULL REFERENCES accounts(id),
    amount INTEGER NOT NULL,
    stripe_event_id TEXT UNIQUE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_account_credit_ledger_account ON account_credit_ledger(account_id);
