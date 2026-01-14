-- name: InsertAccount :exec
INSERT INTO accounts (id, created_by, billing_status) VALUES (?, ?, 'pending');

-- name: ActivateAccount :exec
-- ActivateAccount marks an account as active after Stripe checkout completes.
UPDATE accounts SET billing_status = 'active' WHERE created_by = ?;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = ?;

-- name: UserIsPaying :one
-- UserIsPaying checks if a user has billing information on file.
-- Users with an active account record have completed the Stripe subscription flow.
SELECT COUNT(*) > 0 FROM accounts WHERE created_by = ? AND billing_status = 'active';

-- name: GetAccountByUserID :one
SELECT * FROM accounts WHERE created_by = ?;

-- name: ListAllAccounts :many
SELECT * FROM accounts;

-- name: UserNeedsBilling :one
-- UserNeedsBilling checks if a user needs to add billing before creating VMs.
-- Returns true only if:
-- 1. The user doesn't have an ACTIVE account record (pending accounts don't count)
-- 2. AND the user was created on or after 2026-01-06 23:10:00 UTC (3:10pm PST) (billing requirement date)
-- 3. AND the user doesn't have a 'free' billing exemption
-- 4. AND the user doesn't have an active 'trial' exemption (trial_ends_at in the future)
-- Legacy users created before this date are grandfathered and don't need billing.
-- Users with invite code exemptions may also bypass billing.
SELECT
    NOT EXISTS (SELECT 1 FROM accounts WHERE created_by = ?1 AND billing_status = 'active')
    AND u.created_at >= '2026-01-06 23:10:00'
    AND (
        u.billing_exemption IS NULL
        OR (u.billing_exemption = 'trial' AND u.billing_trial_ends_at <= datetime('now'))
    )
FROM users u WHERE u.user_id = ?1;

-- name: CountAccountsByBillingStatus :one
-- CountAccountsByBillingStatus counts accounts with the given billing status.
SELECT COUNT(*) FROM accounts WHERE billing_status = ?;
