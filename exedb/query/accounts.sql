-- name: InsertAccount :exec
INSERT INTO accounts (id, created_by) VALUES (?, ?);

-- name: InsertAccountPlan :exec
INSERT INTO account_plans (account_id, plan_id, started_at, trial_expires_at, changed_by)
VALUES (?, ?, ?, ?, ?);

-- name: GetActiveAccountPlan :one
SELECT account_id, plan_id, started_at, ended_at, trial_expires_at, changed_by, created_at
FROM account_plans
WHERE account_id = ? AND ended_at IS NULL;

-- name: CloseAccountPlan :exec
UPDATE account_plans SET ended_at = ?2 WHERE account_id = ?1 AND ended_at IS NULL;

-- name: ListAccountPlanHistory :many
SELECT account_id, plan_id, started_at, ended_at, trial_expires_at, changed_by, created_at
FROM account_plans
WHERE account_id = ?
ORDER BY started_at DESC;

-- name: GetActivePlanForUser :one
SELECT
    COALESCE(parent_ap.plan_id, own_ap.plan_id) AS plan_id,
    COALESCE(parent_ap.account_id, own_ap.account_id) AS account_id,
    COALESCE(parent_ap.trial_expires_at, own_ap.trial_expires_at) AS trial_expires_at
FROM users u
JOIN accounts a ON a.created_by = u.user_id
JOIN account_plans own_ap ON own_ap.account_id = a.id AND own_ap.ended_at IS NULL
LEFT JOIN accounts parent_acct ON parent_acct.id = a.parent_id
LEFT JOIN account_plans parent_ap ON parent_ap.account_id = parent_acct.id AND parent_ap.ended_at IS NULL
WHERE u.user_id = ?;

-- name: ActivateAccount :exec
-- ActivateAccount marks an account as active after Stripe checkout completes.
-- Inserts an 'active' billing event for the account owned by the given user.
-- Timestamp should be normalized to Time10 format by caller.
INSERT INTO billing_events (account_id, event_type, event_at)
SELECT id, 'active', ?2 FROM accounts WHERE created_by = ?1;

-- name: GetAccount :one
SELECT id, created_by, created_at, parent_id, status FROM accounts WHERE id = ?;

-- name: GetAccountByUserID :one
SELECT id, created_by, created_at, parent_id, status FROM accounts WHERE created_by = ?;

-- name: GetAccountWithBillingStatus :one
-- Returns account info with billing status derived from billing_events.
-- billing_status is 'pending' if no events, otherwise the most recent event_type.
SELECT a.id, a.created_by, a.created_at,
    CAST(COALESCE(
        (SELECT e.event_type FROM billing_events e
         WHERE e.account_id = a.id
         ORDER BY parse_timestamp(e.event_at) DESC, e.id DESC LIMIT 1),
        'pending'
    ) AS TEXT) AS billing_status
FROM accounts a WHERE a.created_by = ?;

-- name: ListAllAccounts :many
SELECT id, created_by, created_at, parent_id, status FROM accounts;

-- name: GetUserBillingStatus :one
-- Returns the user's billing information for determining payment status.
-- billing_status is 'active' if ANY account has active billing as its most recent event,
-- 'canceled' if ANY account has canceled as its most recent event (and none have active),
-- or empty string if no billing events exist.
-- This handles users with multiple accounts correctly by checking across all accounts.
SELECT
    CASE
        WHEN EXISTS (
            SELECT 1 FROM accounts a
            JOIN billing_events e ON e.account_id = a.id
            WHERE a.created_by = u.user_id
            AND e.event_type = 'active'
            AND e.id = (
                SELECT e2.id FROM billing_events e2
                WHERE e2.account_id = a.id
                ORDER BY parse_timestamp(e2.event_at) DESC, e2.id DESC
                LIMIT 1
            )
        ) THEN 'active'
        WHEN EXISTS (
            SELECT 1 FROM accounts a
            JOIN billing_events e ON e.account_id = a.id
            WHERE a.created_by = u.user_id
            AND e.event_type = 'canceled'
            AND e.id = (
                SELECT e2.id FROM billing_events e2
                WHERE e2.account_id = a.id
                ORDER BY parse_timestamp(e2.event_at) DESC, e2.id DESC
                LIMIT 1
            )
        ) THEN 'canceled'
        ELSE ''
    END AS billing_status,
    u.created_at,
    u.billing_exemption,
    u.billing_trial_ends_at
FROM users u
WHERE u.user_id = ?1;

-- name: CountAccountsByBillingStatus :one
-- CountAccountsByBillingStatus counts accounts with the given billing status.
-- For 'active' or 'canceled', counts accounts whose most recent billing event matches.
-- For 'pending', counts accounts with no billing events.
SELECT COUNT(*) FROM accounts a
WHERE (
    ?1 = 'pending' AND NOT EXISTS (SELECT 1 FROM billing_events WHERE account_id = a.id)
) OR (
    ?1 != 'pending' AND EXISTS (
        SELECT 1 FROM billing_events e1
        WHERE e1.account_id = a.id
        AND e1.event_type = ?1
        AND e1.id = (
            SELECT e2.id FROM billing_events e2
            WHERE e2.account_id = a.id
            ORDER BY parse_timestamp(e2.event_at) DESC, e2.id DESC
            LIMIT 1
        )
    )
);

-- name: DeleteAccountsByUserID :exec
DELETE FROM accounts WHERE created_by = ?;

-- name: GetTeamBillingOwnerAccountID :one
-- GetTeamBillingOwnerAccountID returns the billing account ID of the user's team billing_owner.
SELECT a.id
FROM accounts a
JOIN team_members tm_billing ON a.created_by = tm_billing.user_id
JOIN team_members tm_user ON tm_billing.team_id = tm_user.team_id
WHERE tm_user.user_id = @member_user_id
AND tm_billing.role = 'billing_owner';

-- name: GetUserPlanCategory :one
-- GetUserPlanCategory determines the user's plan category for LLM gateway credit limits.
-- Returns 'friend' if user has billing_exemption='free'
-- Returns 'has_billing' if user has an active billing account (most recent billing event is 'active'),
--   or if user's team billing_owner has an active billing account
-- Returns 'no_billing' otherwise
-- Note: 'custom' category is determined by checking for explicit overrides in code, not SQL.
SELECT
    CASE
        WHEN u.billing_exemption = 'free' THEN 'friend'
        WHEN EXISTS (
            SELECT 1 FROM accounts a
            JOIN billing_events e ON e.account_id = a.id
            WHERE a.created_by = ?1
            AND e.event_type = 'active'
            AND e.id = (
                SELECT e2.id FROM billing_events e2
                WHERE e2.account_id = a.id
                ORDER BY parse_timestamp(e2.event_at) DESC, e2.id DESC
                LIMIT 1
            )
        ) THEN 'has_billing'
        WHEN EXISTS (
            SELECT 1 FROM team_members tm_user
            JOIN team_members tm_billing ON tm_user.team_id = tm_billing.team_id
            JOIN accounts a ON a.created_by = tm_billing.user_id
            JOIN billing_events e ON e.account_id = a.id
            WHERE tm_user.user_id = ?1
            AND tm_billing.role = 'billing_owner'
            AND e.event_type = 'active'
            AND e.id = (
                SELECT e2.id FROM billing_events e2
                WHERE e2.account_id = a.id
                ORDER BY parse_timestamp(e2.event_at) DESC, e2.id DESC
                LIMIT 1
            )
        ) THEN 'has_billing'
        ELSE 'no_billing'
    END
FROM users u WHERE u.user_id = ?1;

-- name: GetUserBilling :one
-- GetUserBilling returns a single row with the user's plan category, billing status,
-- account creation date, billing exemption, and trial end date.
-- Combines GetUserBillingStatus and GetUserPlanCategory into one round trip.
SELECT
    CASE
        WHEN u.billing_exemption = 'free' THEN 'friend'
        WHEN EXISTS (
            SELECT 1 FROM accounts a
            JOIN billing_events e ON e.account_id = a.id
            WHERE a.created_by = ?1
            AND e.event_type = 'active'
            AND e.id = (
                SELECT e2.id FROM billing_events e2
                WHERE e2.account_id = a.id
                ORDER BY parse_timestamp(e2.event_at) DESC, e2.id DESC
                LIMIT 1
            )
        ) THEN 'has_billing'
        WHEN EXISTS (
            SELECT 1 FROM team_members tm_user
            JOIN team_members tm_billing ON tm_user.team_id = tm_billing.team_id
            JOIN accounts a ON a.created_by = tm_billing.user_id
            JOIN billing_events e ON e.account_id = a.id
            WHERE tm_user.user_id = ?1
            AND tm_billing.role = 'billing_owner'
            AND e.event_type = 'active'
            AND e.id = (
                SELECT e2.id FROM billing_events e2
                WHERE e2.account_id = a.id
                ORDER BY parse_timestamp(e2.event_at) DESC, e2.id DESC
                LIMIT 1
            )
        ) THEN 'has_billing'
        ELSE 'no_billing'
    END AS category,
    CAST(COALESCE(
        CASE
            WHEN EXISTS (
                SELECT 1 FROM accounts a
                JOIN billing_events e ON e.account_id = a.id
                WHERE a.created_by = u.user_id
                AND e.event_type = 'active'
                AND e.id = (
                    SELECT e2.id FROM billing_events e2
                    WHERE e2.account_id = a.id
                    ORDER BY parse_timestamp(e2.event_at) DESC, e2.id DESC
                    LIMIT 1
                )
            ) THEN 'active'
            WHEN EXISTS (
                SELECT 1 FROM accounts a
                JOIN billing_events e ON e.account_id = a.id
                WHERE a.created_by = u.user_id
                AND e.event_type = 'canceled'
                AND e.id = (
                    SELECT e2.id FROM billing_events e2
                    WHERE e2.account_id = a.id
                    ORDER BY parse_timestamp(e2.event_at) DESC, e2.id DESC
                    LIMIT 1
                )
            ) THEN 'canceled'
        END,
    '') AS TEXT) AS billing_status,
    u.email,
    u.created_at,
    u.billing_exemption,
    u.billing_trial_ends_at
FROM users u
WHERE u.user_id = ?1;
