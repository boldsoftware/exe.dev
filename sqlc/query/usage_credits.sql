-- name: InsertUsageCredit :exec
INSERT INTO usage_credits (billing_account_id, amount, payment_method, payment_id, status, data)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetUsageCreditsByBillingAccount :many
SELECT id, billing_account_id, amount, payment_method, payment_id, status, data, created_at
FROM usage_credits
WHERE billing_account_id = ?
ORDER BY created_at DESC;

-- name: GetUsageCredit :one
SELECT id, billing_account_id, amount, payment_method, payment_id, status, data, created_at
FROM usage_credits
WHERE id = ?;

-- name: UpdateUsageCreditStatus :exec
UPDATE usage_credits
SET status = ?
WHERE id = ?;

-- name: GetCreditBalanceForBillingAccount :one
SELECT COALESCE(SUM(amount), 0.0)
FROM usage_credits
WHERE billing_account_id = ? AND status = 'completed';