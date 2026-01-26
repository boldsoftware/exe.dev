-- name: InsertAccount :exec
INSERT INTO accounts (id, created_by) VALUES (?, ?);

-- name: ActivateAccount :exec
-- ActivateAccount marks an account as active after Stripe checkout completes.
-- Inserts an 'active' billing event for the account owned by the given user.
-- Timestamp should be normalized to Time10 format by caller.
INSERT INTO billing_events (account_id, event_type, event_at)
SELECT id, 'active', ?2 FROM accounts WHERE created_by = ?1;

-- name: GetAccount :one
SELECT id, created_by, created_at FROM accounts WHERE id = ?;

-- name: GetAccountByUserID :one
SELECT id, created_by, created_at FROM accounts WHERE created_by = ?;

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
SELECT id, created_by, created_at FROM accounts;

-- name: GetUserBillingStatus :one
-- Returns the user's billing information for determining payment status.
-- billing_status is NULL if no billing events exist, or the most recent event_type ('active' or 'canceled').
-- Uses parse_timestamp() to handle various timestamp formats including Go's time.String().
-- Uses id DESC as tiebreaker when timestamps are equal.
SELECT e.event_type AS billing_status,
u.created_at,
u.billing_exemption,
u.billing_trial_ends_at
FROM users u
LEFT JOIN accounts a ON a.created_by = u.user_id
LEFT JOIN billing_events e ON e.account_id = a.id AND e.id = (
    SELECT e2.id FROM billing_events e2
    WHERE e2.account_id = a.id
    ORDER BY parse_timestamp(e2.event_at) DESC, e2.id DESC
    LIMIT 1
)
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

-- name: GetUserPlanCategory :one
-- GetUserPlanCategory determines the user's plan category for LLM gateway credit limits.
-- Returns 'friend' if user has billing_exemption='free'
-- Returns 'has_billing' if user has an active billing account (most recent billing event is 'active')
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
        ELSE 'no_billing'
    END
FROM users u WHERE u.user_id = ?1;
