-- name: UseCredits :one
-- UseCredits inserts a deduction into the credit ledger and returns the new balance.
-- amount should be negative for deductions. Negative balances are allowed.
INSERT INTO account_credit_ledger (account_id, amount, hour_bucket, credit_type)
VALUES (?1, ?2, strftime('%Y-%m-%d %H:00:00', CURRENT_TIMESTAMP), ?3)
ON CONFLICT(account_id, hour_bucket, credit_type)
DO UPDATE SET amount = account_credit_ledger.amount + excluded.amount
RETURNING CAST((SELECT COALESCE(SUM(amount), 0) FROM account_credit_ledger WHERE account_id = ?1) AS INTEGER);

-- name: GetCreditBalance :one
-- GetCreditBalance returns the current credit balance for an account.
SELECT CAST(COALESCE(SUM(amount), 0) AS INTEGER) AS balance FROM account_credit_ledger WHERE account_id = ?;

-- name: SyncCreditLedger :exec
-- SyncCreditLedger adds credits to the ledger for a Stripe event, idempotent via UNIQUE stripe_event_id.
INSERT OR IGNORE INTO account_credit_ledger (account_id, amount, stripe_event_id)
VALUES (?1, ?2, ?3);
