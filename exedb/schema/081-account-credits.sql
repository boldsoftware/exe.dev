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

-- Expand billing_events.event_type to include 'credit_purchase'.
-- SQLite does not support ALTER CHECK, so we recreate the table.
CREATE TABLE billing_events_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id TEXT NOT NULL,
    event_type TEXT NOT NULL CHECK (event_type IN ('active', 'canceled', 'credit_purchase')),
    event_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (account_id) REFERENCES accounts(id)
);
INSERT INTO billing_events_new SELECT * FROM billing_events;
DROP TABLE billing_events;
ALTER TABLE billing_events_new RENAME TO billing_events;
CREATE INDEX idx_billing_events_account_event_at ON billing_events(account_id, event_at DESC);
CREATE UNIQUE INDEX idx_billing_events_unique ON billing_events(account_id, event_type, event_at);
