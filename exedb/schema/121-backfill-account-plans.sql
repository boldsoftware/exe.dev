-- Backfill: ensure every user has exactly one accounts row and one account_plans row,
-- and team member accounts have parent_id set to the billing owner's account.
--
-- Step 1: Insert an account for every user that doesn't have one yet.
-- Uses created_by = user_id join. Idempotent: INSERT OR IGNORE.
INSERT OR IGNORE INTO accounts (id, created_by)
SELECT 'exe_' || lower(hex(randomblob(8))), user_id
FROM users
WHERE user_id NOT IN (SELECT created_by FROM accounts);

-- Step 1b: Set parent_id for team members. Each team member's account points
-- to the billing owner's account. The billing owner's account stays top-level (parent_id = NULL).
UPDATE accounts
SET parent_id = (
    SELECT owner_acct.id
    FROM team_members tm_member
    JOIN team_members tm_owner ON tm_owner.team_id = tm_member.team_id AND tm_owner.role = 'billing_owner'
    JOIN accounts owner_acct ON owner_acct.created_by = tm_owner.user_id
    WHERE tm_member.user_id = accounts.created_by
    AND tm_member.role != 'billing_owner'
)
WHERE parent_id IS NULL
AND created_by IN (
    SELECT user_id FROM team_members WHERE role != 'billing_owner'
);

-- Step 2: Insert an account_plans row (plan=basic, changed_by=system:backfill) for every
-- account that has no active plan yet (ended_at IS NULL).
-- The plan waterfall is applied in a CASE expression matching the same priority as GetPlanVersion:
--   canceled               -> basic
--   billing_exemption=free + explicit overrides -> vip (overrides not available in SQL, skip to friend)
--   billing_exemption=free -> friend
--   team billing active    -> handled by parent_id (step 1b), child keeps own plan
--   has_billing (active billing event) -> individual
--   billing_exemption=trial + valid trial -> trial (with trial_expires_at)
--   created_at < 2026-01-06 23:10:00 -> grandfathered
--   default -> basic
--
-- NOTE: VIP (friend + explicit overrides) cannot be detected purely in SQL without the
-- user_llm_credit table overrides query. Those users will be assigned 'friend' here and
-- can be manually corrected. The plan is the source of truth going forward.
INSERT INTO account_plans (account_id, plan_id, started_at, trial_expires_at, changed_by)
SELECT
    a.id AS account_id,
    CASE
        -- Canceled: most recent billing event is 'canceled'
        WHEN (
            SELECT e.event_type FROM billing_events e
            WHERE e.account_id = a.id
            ORDER BY e.id DESC LIMIT 1
        ) = 'canceled' THEN 'basic'
        -- Friend (billing_exemption = 'free')
        WHEN u.billing_exemption = 'free' THEN 'friend'
        -- Team billing owners get 'individual' here; step 4 will close it and add 'team'
        -- Individual: has active billing (most recent event is 'active')
        WHEN (
            SELECT e.event_type FROM billing_events e
            WHERE e.account_id = a.id
            ORDER BY e.id DESC LIMIT 1
        ) = 'active' THEN 'individual'
        -- Trial: billing_exemption = 'trial' with a future trial expiry
        WHEN u.billing_exemption = 'trial'
            AND u.billing_trial_ends_at IS NOT NULL
            AND u.billing_trial_ends_at > CURRENT_TIMESTAMP THEN 'trial'
        -- Grandfathered: created before billing-required date (2026-01-06 23:10:00 UTC)
        WHEN u.created_at < '2026-01-06 23:10:00' THEN 'grandfathered'
        -- Default: basic
        ELSE 'basic'
    END AS plan_id,
    COALESCE(u.created_at, CURRENT_TIMESTAMP) AS started_at,
    -- Carry over trial_expires_at for trial users
    CASE
        WHEN u.billing_exemption = 'trial'
            AND u.billing_trial_ends_at IS NOT NULL
            AND u.billing_trial_ends_at > CURRENT_TIMESTAMP
        THEN u.billing_trial_ends_at
        ELSE NULL
    END AS trial_expires_at,
    'system:backfill' AS changed_by
FROM users u
JOIN accounts a ON a.created_by = u.user_id
WHERE NOT EXISTS (
    SELECT 1 FROM account_plans ap
    WHERE ap.account_id = a.id AND ap.ended_at IS NULL
);

-- Step 3: Record expired trials. Users who had billing_exemption='trial' but their
-- trial_ends_at has already passed get a closed trial row (ended_at = trial_expires_at)
-- plus their current plan from step 2. This preserves the trial history.
INSERT INTO account_plans (account_id, plan_id, started_at, ended_at, trial_expires_at, changed_by)
SELECT
    a.id AS account_id,
    'trial' AS plan_id,
    COALESCE(u.created_at, CURRENT_TIMESTAMP) AS started_at,
    u.billing_trial_ends_at AS ended_at,
    u.billing_trial_ends_at AS trial_expires_at,
    'system:backfill' AS changed_by
FROM users u
JOIN accounts a ON a.created_by = u.user_id
WHERE u.billing_exemption = 'trial'
    AND u.billing_trial_ends_at IS NOT NULL
    AND u.billing_trial_ends_at <= CURRENT_TIMESTAMP;

-- Step 4: Team billing owners. Close their 'individual' plan and insert a 'team' plan.
-- This records the history: they were individual, then upgraded to team.
-- Close the individual plan (set ended_at to now).
UPDATE account_plans
SET ended_at = CURRENT_TIMESTAMP
WHERE ended_at IS NULL
AND plan_id = 'individual'
AND account_id IN (
    SELECT a.id FROM accounts a
    JOIN team_members tm ON tm.user_id = a.created_by
    WHERE tm.role = 'billing_owner'
);

-- Insert the active team plan.
INSERT INTO account_plans (account_id, plan_id, started_at, changed_by)
SELECT
    a.id AS account_id,
    'team' AS plan_id,
    CURRENT_TIMESTAMP AS started_at,
    'user:change' AS changed_by
FROM accounts a
JOIN team_members tm ON tm.user_id = a.created_by
WHERE tm.role = 'billing_owner'
AND NOT EXISTS (
    SELECT 1 FROM account_plans ap
    WHERE ap.account_id = a.id AND ap.plan_id = 'team' AND ap.ended_at IS NULL
);
