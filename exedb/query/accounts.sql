-- name: InsertAccount :exec
INSERT OR IGNORE INTO accounts (id, created_by) VALUES (?, ?);

-- name: InsertAccountPlan :exec
INSERT INTO account_plans (account_id, plan_id, started_at, trial_expires_at, changed_by)
VALUES (?, ?, ?, ?, ?);

-- name: UpsertAccountPlan :exec
-- UpsertAccountPlan inserts an account plan only if the account has no active plan.
INSERT OR IGNORE INTO account_plans (account_id, plan_id, started_at, trial_expires_at, changed_by)
VALUES (?, ?, ?, ?, ?);

-- name: SetTrialExpiresAt :exec
-- SetTrialExpiresAt updates trial_expires_at for an active Stripe-managed plan.
-- Only updates rows where changed_by='stripe:event' to avoid modifying invite trials.
UPDATE account_plans
SET trial_expires_at = ?2
WHERE account_id = ?1 AND ended_at IS NULL AND changed_by = 'stripe:event';

-- name: DebugSetTrialExpiresAt :exec
-- DebugSetTrialExpiresAt updates trial_expires_at for any active plan.
-- Used by the debug page to adjust trial expiry for testing.
UPDATE account_plans
SET trial_expires_at = ?2
WHERE account_id = ?1 AND ended_at IS NULL;

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
-- Resolves a user's effective plan. If parent_id is set, uses parent's plan.
-- trial_expires_at is excluded because COALESCE returns it as a string
-- that the Go SQLite driver cannot scan into *time.Time.
SELECT
    COALESCE(parent_ap.plan_id, own_ap.plan_id) AS plan_id,
    COALESCE(parent_ap.account_id, own_ap.account_id) AS account_id
FROM users u
JOIN accounts a ON a.created_by = u.user_id
JOIN account_plans own_ap ON own_ap.account_id = a.id AND own_ap.ended_at IS NULL
LEFT JOIN accounts parent_acct ON parent_acct.id = a.parent_id
LEFT JOIN account_plans parent_ap ON parent_ap.account_id = parent_acct.id AND parent_ap.ended_at IS NULL
WHERE u.user_id = ?;

-- name: ActivateAccount :exec
-- ActivateAccount marks an account as active after Stripe checkout completes.
-- Inserts an 'active' billing event for the account owned by the given user.
-- event_at is stored as YYYY-MM-DD HH:MM:SS in UTC by the driver.
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
         ORDER BY e.event_at DESC, e.id DESC LIMIT 1),
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
                ORDER BY e2.event_at DESC, e2.id DESC
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
                ORDER BY e2.event_at DESC, e2.id DESC
                LIMIT 1
            )
        ) THEN 'canceled'
        ELSE ''
    END AS billing_status,
    u.created_at
FROM users u
WHERE u.user_id = ?1;

-- name: CountAccountsBillingStatuses :many
-- CountAccountsBillingStatuses returns account counts grouped by billing status
-- (pending/active/canceled) in a single table scan.
SELECT billing_status, COUNT(*) AS count FROM (
    SELECT
        CAST(COALESCE(
            (SELECT e.event_type FROM billing_events e
             WHERE e.account_id = a.id
             ORDER BY e.event_at DESC, e.id DESC LIMIT 1),
            'pending'
        ) AS TEXT) AS billing_status
    FROM accounts a
) GROUP BY billing_status;

-- name: CountTrialsByKindAndStatus :many
-- Count stripeless trial accounts by kind (signup=7-day, invite=30-day) and
-- status (active, expired, converted).
SELECT kind, status, COUNT(*) AS count FROM (
    SELECT
        CASE
            WHEN ap.changed_by = 'system:signup' THEN 'signup'
            WHEN ap.changed_by LIKE 'invite:%' THEN 'invite'
            ELSE 'other'
        END AS kind,
        CASE
            WHEN EXISTS (
                SELECT 1 FROM account_plans ap2
                WHERE ap2.account_id = a.id
                  AND ap2.ended_at IS NULL
                  AND ap2.plan_id NOT LIKE 'trial:%'
                  AND ap2.plan_id NOT LIKE 'basic:%'
                  AND ap2.plan_id != 'restricted'
            ) THEN 'converted'
            WHEN ap.ended_at IS NULL AND ap.trial_expires_at > datetime('now') THEN 'active'
            ELSE 'expired'
        END AS status
    FROM accounts a
    JOIN account_plans ap ON ap.account_id = a.id
    WHERE ap.plan_id LIKE 'trial:%'
) GROUP BY kind, status;

-- name: SetAccountParentID :exec
-- Sets the parent_id on a user's account to link them to a team billing owner's account.
UPDATE accounts SET parent_id = ?2 WHERE created_by = ?1;

-- name: ClearAccountParentID :exec
-- Clears the parent_id on a user's account when they leave a team.
UPDATE accounts SET parent_id = NULL WHERE created_by = ?;

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
-- Returns 'friend' if user has account_plans.plan_id IN ('friend', 'free')
-- Returns 'has_billing' if user has an active billing account (most recent billing event is 'active'),
--   or if user's team billing_owner has an active billing account
-- Returns 'no_billing' otherwise
-- Note: 'custom' category is determined by checking for explicit overrides in code, not SQL.
SELECT
    CASE
        WHEN ap.plan_id IN ('friend', 'free') THEN 'friend'
        WHEN EXISTS (
            SELECT 1 FROM accounts a
            JOIN billing_events e ON e.account_id = a.id
            WHERE a.created_by = ?1
            AND e.event_type = 'active'
            AND e.id = (
                SELECT e2.id FROM billing_events e2
                WHERE e2.account_id = a.id
                ORDER BY e2.event_at DESC, e2.id DESC
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
                ORDER BY e2.event_at DESC, e2.id DESC
                LIMIT 1
            )
        ) THEN 'has_billing'
        ELSE 'no_billing'
    END
FROM users u
LEFT JOIN accounts a ON a.created_by = u.user_id
LEFT JOIN account_plans ap ON ap.account_id = a.id AND ap.ended_at IS NULL
WHERE u.user_id = ?1;

-- name: ListPlanVersionCounts :many
-- ListPlanVersionCounts returns all active plan versions with subscriber counts.
SELECT plan_id, COUNT(*) as cnt
FROM account_plans
WHERE ended_at IS NULL
GROUP BY plan_id
ORDER BY cnt DESC, plan_id;

-- name: ListActiveSubscribersByPlanID :many
-- ListActiveSubscribersByPlanID returns all account IDs with the given active plan.
SELECT account_id
FROM account_plans
WHERE plan_id = ? AND ended_at IS NULL
ORDER BY started_at;

-- name: CloseAccountPlansByPlanID :exec
-- CloseAccountPlansByPlanID closes all active plans with the given plan_id.
UPDATE account_plans SET ended_at = ?2 WHERE plan_id = ?1 AND ended_at IS NULL;

-- name: InsertAccountPlanMigration :exec
-- InsertAccountPlanMigration inserts a new plan row during a plan version migration.
INSERT INTO account_plans (account_id, plan_id, started_at, changed_by)
VALUES (?, ?, ?, ?);

-- name: GetUserBilling :one
-- GetUserBilling returns a single row with the user's plan category, billing status,
-- account creation date, billing exemption, and trial end date.
-- Combines GetUserBillingStatus and GetUserPlanCategory into one round trip.
SELECT
    CASE
        WHEN ap.plan_id IN ('friend', 'free') THEN 'friend'
        WHEN EXISTS (
            SELECT 1 FROM accounts a
            JOIN billing_events e ON e.account_id = a.id
            WHERE a.created_by = ?1
            AND e.event_type = 'active'
            AND e.id = (
                SELECT e2.id FROM billing_events e2
                WHERE e2.account_id = a.id
                ORDER BY e2.event_at DESC, e2.id DESC
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
                ORDER BY e2.event_at DESC, e2.id DESC
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
                    ORDER BY e2.event_at DESC, e2.id DESC
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
                    ORDER BY e2.event_at DESC, e2.id DESC
                    LIMIT 1
                )
            ) THEN 'canceled'
        END,
    '') AS TEXT) AS billing_status,
    u.email,
    u.created_at
FROM users u
LEFT JOIN accounts a ON a.created_by = u.user_id
LEFT JOIN account_plans ap ON ap.account_id = a.id AND ap.ended_at IS NULL
WHERE u.user_id = ?1;

-- name: CountAllAccounts :one
SELECT COUNT(*) FROM accounts;

-- name: CountAccountsWithoutActivePlan :one
-- CountAccountsWithoutActivePlan counts accounts that have no active (ended_at IS NULL) plan row.
SELECT COUNT(*) FROM accounts a
WHERE NOT EXISTS (
    SELECT 1 FROM account_plans ap
    WHERE ap.account_id = a.id AND ap.ended_at IS NULL
);

-- name: CountAccountsWithoutUser :one
-- CountAccountsWithoutUser counts accounts whose created_by user no longer exists.
SELECT COUNT(*) FROM accounts a
WHERE NOT EXISTS (
    SELECT 1 FROM users u WHERE u.user_id = a.created_by
);

-- name: GetUserIDByAccountID :one
-- Returns the user_id of the account owner.
SELECT created_by FROM accounts WHERE id = ?;

-- name: GetUserPlanData :one
-- GetUserPlanData returns all data needed to determine a user's plan category.
-- This is used by billing/entitlement.GetPlanForUser to compute the final PlanCategory.
SELECT
    -- Account plan info
    ap.plan_id,
    -- Team billing: true if user is on a team with an active billing owner
    CAST(EXISTS (
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
            ORDER BY e2.event_at DESC, e2.id DESC
            LIMIT 1
        )
    ) AS INTEGER) AS team_billing_active,
    -- Explicit overrides: VIP status is determined by plan_id prefix 'vip:'
    CASE WHEN ap.plan_id LIKE 'vip:%' THEN 1 ELSE 0 END AS has_explicit_overrides,
    ap.trial_expires_at,
    -- User info
    u.created_at,
    -- Billing status: active/canceled/empty
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
                    ORDER BY e2.event_at DESC, e2.id DESC
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
                    ORDER BY e2.event_at DESC, e2.id DESC
                    LIMIT 1
                )
            ) THEN 'canceled'
        END,
    '') AS TEXT) AS billing_status
FROM users u
LEFT JOIN accounts a ON a.created_by = u.user_id
LEFT JOIN account_plans ap ON ap.account_id = a.id AND ap.ended_at IS NULL
WHERE u.user_id = ?1;

-- name: HadTrial :one
-- HadTrial reports whether the account ever had any kind of trial:
-- stripeless trials (plan_id LIKE 'trial:%') or Stripe trials (trial_expires_at IS NOT NULL).
SELECT EXISTS (
    SELECT 1 FROM account_plans
    WHERE account_id = ? AND (plan_id LIKE 'trial:%' OR trial_expires_at IS NOT NULL)
) AS had_trial;

-- name: NextExpiredTrialUser :one
-- NextExpiredTrialUser returns the user ID with the oldest expired
-- stripeless-signup trial (changed_by = 'system:stripeless_trial').
-- Other trial kinds (Stripe, invite) are intentionally excluded: this
-- enforcer only handles self-serve stripeless trials. Candidates only --
-- the caller must verify entitlement via plan.ForUser before
-- transitioning anything. Returns sql.ErrNoRows if there are none.
SELECT a.created_by AS user_id
FROM account_plans ap
JOIN accounts a ON a.id = ap.account_id
WHERE ap.ended_at IS NULL
  AND ap.trial_expires_at IS NOT NULL
  AND ap.trial_expires_at <= datetime('now')
  AND ap.changed_by = 'system:stripeless_trial'
ORDER BY ap.trial_expires_at ASC
LIMIT 1;

-- name: NextTrialExpiry :one
-- NextTrialExpiry returns the earliest trial_expires_at for any active
-- stripeless-signup trial (changed_by = 'system:stripeless_trial') that
-- has not yet expired. Other trial kinds (Stripe-managed, invite) are
-- excluded: this enforcer only handles self-serve stripeless trials.
-- Returns sql.ErrNoRows if there are none.
SELECT trial_expires_at
FROM account_plans
WHERE ended_at IS NULL
  AND trial_expires_at > datetime('now')
  AND changed_by = 'system:stripeless_trial'
ORDER BY trial_expires_at ASC
LIMIT 1;


-- name: ActiveTrialUsers :many
-- ActiveTrialUsers returns all users with an active stripeless signup trial
-- (changed_by = 'system:stripeless_trial'), their expiry time, email, and the
-- count of running boxes they own. Stripe-managed and invite trials are
-- excluded because the trial expiry enforcer does not act on them. Used by
-- the debug dashboard.
SELECT
    u.user_id,
    u.email,
    ap.plan_id,
    ap.trial_expires_at,
    ap.changed_by,
    (SELECT COUNT(*) FROM boxes b
     WHERE b.created_by_user_id = u.user_id AND b.status = 'running') AS running_box_count
FROM account_plans ap
JOIN accounts a ON a.id = ap.account_id
JOIN users u ON u.user_id = a.created_by
WHERE ap.ended_at IS NULL
  AND ap.trial_expires_at IS NOT NULL
  AND ap.changed_by = 'system:stripeless_trial'
ORDER BY ap.trial_expires_at ASC;
