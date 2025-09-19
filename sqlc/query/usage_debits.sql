-- name: InsertUsageDebit :exec
INSERT INTO usage_debits (billing_account_id, model, message_id, input_tokens, cache_creation_input_tokens, cache_read_input_tokens, output_tokens, cost_usd)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetUsageDebitsByBillingAccount :many
SELECT id, billing_account_id, model, message_id, input_tokens, cache_creation_input_tokens, cache_read_input_tokens, output_tokens, cost_usd, created_at
FROM usage_debits
WHERE billing_account_id = ?
ORDER BY created_at DESC;

-- name: GetUsageDebit :one
SELECT id, billing_account_id, model, message_id, input_tokens, cache_creation_input_tokens, cache_read_input_tokens, output_tokens, cost_usd, created_at
FROM usage_debits
WHERE id = ?;

-- name: GetDebitBalanceForBillingAccount :one
SELECT COALESCE(SUM(cost_usd), 0.0)
FROM usage_debits
WHERE billing_account_id = ?;

-- name: GetUsageDebitByMessageID :one
SELECT id, billing_account_id, model, message_id, input_tokens, cache_creation_input_tokens, cache_read_input_tokens, output_tokens, cost_usd, created_at
FROM usage_debits
WHERE message_id = ?;