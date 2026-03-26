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

-- name: GiftCredits :exec
-- GiftCredits inserts a gift credit for a billing account.
-- The gift_id unique index provides idempotency; duplicates are silently ignored.
INSERT OR IGNORE INTO billing_credits (account_id, amount, credit_type, gift_id, note)
VALUES (?, ?, 'gift', ?, ?);

-- name: GetCreditState :one
-- GetCreditState returns the credit breakdown (gift, usage, paid, total) for an account.
-- Used (usage) rows are negative in the DB; the caller should negate the used value.
SELECT
    CAST(COALESCE(SUM(CASE WHEN credit_type = 'gift' THEN amount ELSE 0 END), 0) AS INTEGER) AS gift,
    CAST(COALESCE(SUM(CASE WHEN credit_type = 'usage' THEN amount ELSE 0 END), 0) AS INTEGER) AS used,
    CAST(COALESCE(SUM(CASE WHEN credit_type IS NULL AND stripe_event_id IS NOT NULL THEN amount ELSE 0 END), 0) AS INTEGER) AS paid,
    CAST(COALESCE(SUM(amount), 0) AS INTEGER) AS total
FROM billing_credits
WHERE account_id = ?;

-- name: ListGiftCredits :many
-- ListGiftCredits returns all gift credits for an account, most recent first.
SELECT amount, note, gift_id, created_at
FROM billing_credits
WHERE account_id = ? AND credit_type = 'gift'
ORDER BY created_at DESC, id DESC;

-- name: InsertPaidCredits :exec
-- InsertPaidCredits records a paid credit from a Stripe payment.
-- The stripe_event_id unique index provides idempotency; duplicates are silently ignored.
INSERT OR IGNORE INTO billing_credits (account_id, amount, stripe_event_id)
VALUES (?, ?, ?);
