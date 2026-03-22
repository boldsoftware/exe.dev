-- name: UseCredits :one
-- UseCredits inserts a deduction into the credit ledger and returns the new balance.
-- amount should be negative for deductions. Negative balances are allowed.
INSERT INTO billing_credits (account_id, amount, hour_bucket, credit_type)
VALUES (?1, ?2, strftime('%Y-%m-%d %H:00:00', CURRENT_TIMESTAMP), ?3)
ON CONFLICT(account_id, hour_bucket, credit_type)
DO UPDATE SET amount = billing_credits.amount + excluded.amount
RETURNING CAST((SELECT COALESCE(SUM(amount), 0) FROM billing_credits WHERE account_id = ?1) AS INTEGER);
-- name: ListBillingCreditsForAccount :many
SELECT id, account_id, amount, stripe_event_id, created_at, hour_bucket, credit_type, gift_id, note
FROM billing_credits WHERE account_id = ?
ORDER BY id DESC;
