-- name: InsertBillingAccount :exec
INSERT INTO billing_accounts (billing_account_id, name, billing_email, stripe_customer_id)
VALUES (?, ?, ?, ?);

-- name: GetBillingAccount :one
SELECT billing_account_id, name, billing_email, stripe_customer_id, created_at, updated_at
FROM billing_accounts
WHERE billing_account_id = ?;

-- name: GetBillingAccountByStripeCustomer :one
SELECT billing_account_id, name, billing_email, stripe_customer_id, created_at, updated_at
FROM billing_accounts
WHERE stripe_customer_id = ?;

-- name: UpdateBillingAccountEmail :exec
UPDATE billing_accounts 
SET billing_email = ?, updated_at = CURRENT_TIMESTAMP
WHERE billing_account_id = ?;

-- name: UpdateBillingAccountStripe :exec
UPDATE billing_accounts 
SET stripe_customer_id = ?, updated_at = CURRENT_TIMESTAMP
WHERE billing_account_id = ?;

-- name: GetBillingAccountByAllocID :one
SELECT ba.*
FROM billing_accounts ba
JOIN allocs a ON ba.billing_account_id = a.billing_account_id
WHERE a.alloc_id = ?;

-- name: LinkAllocToBillingAccount :exec
UPDATE allocs
SET billing_account_id = ?
WHERE alloc_id = ?;

-- name: DeleteBillingAccount :exec
DELETE FROM billing_accounts
WHERE billing_account_id = ?;