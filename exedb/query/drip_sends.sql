-- name: InsertDripSend :exec
INSERT INTO drip_sends (user_id, campaign, step, status, skip_reason, email_to, email_subject, email_body)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetDripSendsForUser :many
SELECT * FROM drip_sends
WHERE user_id = ? AND campaign = ?
ORDER BY created_at ASC;

-- name: HasDripSend :one
SELECT EXISTS (
    SELECT 1 FROM drip_sends
    WHERE user_id = ? AND campaign = ? AND step = ?
) AS has_send;

-- name: CountDripSendsSince :one
SELECT COUNT(*) FROM drip_sends
WHERE user_id = ? AND status = 'sent' AND created_at >= ?;

-- name: ListTrialUsersForDrip :many
-- Returns users who are on a self-serve signup trial or were recently on one
-- (for post-trial emails). Includes active signup-trial users and users whose
-- signup trial started within the last 21 days (to cover the day14 final
-- email). Excludes users who have upgraded to a paid plan, non-signup trials,
-- and child accounts whose effective plan resolves through a parent account.
SELECT
    u.user_id,
    u.email,
    u.region,
    ap.started_at AS trial_started_at,
    ap.trial_expires_at,
    CASE WHEN ap.ended_at IS NULL AND ap.plan_id LIKE 'trial:%' THEN 1 ELSE 0 END AS still_on_trial
FROM users u
JOIN accounts a ON a.created_by = u.user_id
JOIN account_plans ap ON ap.account_id = a.id
WHERE ap.plan_id LIKE 'trial:%'
  AND ap.started_at >= sqlc.arg(started_at_cutoff)
  AND ap.changed_by = 'system:stripeless_trial'
  AND a.parent_id IS NULL
  -- Only target users created after drip campaign deployment.
  AND u.created_at >= '2026-04-14'
  -- Exclude users created via logging into someone else's machine.
  AND u.created_for_login_with_exe = 0
  -- Exclude users who have upgraded: they have an active non-trial, non-basic plan.
  AND NOT EXISTS (
      SELECT 1 FROM account_plans ap2
      WHERE ap2.account_id = a.id
        AND ap2.ended_at IS NULL
        AND ap2.plan_id NOT LIKE 'trial:%'
        AND ap2.plan_id NOT LIKE 'basic:%'
        AND ap2.plan_id != 'restricted'
  )
  -- Exclude users who have active billing (i.e. paid/subscribed).
  AND NOT EXISTS (
      SELECT 1 FROM billing_events be
      WHERE be.account_id = a.id
        AND be.event_type = 'active'
        AND be.id = (
            SELECT be2.id FROM billing_events be2
            WHERE be2.account_id = a.id
            ORDER BY be2.event_at DESC, be2.id DESC
            LIMIT 1
        )
  )
ORDER BY ap.started_at ASC;

-- name: CountBoxesEverForUser :one
-- Count all boxes a user has ever had (current + deleted).
SELECT
    (SELECT COUNT(*) FROM boxes WHERE created_by_user_id = ?1)
    + (SELECT COUNT(*) FROM deleted_boxes WHERE user_id = ?1)
AS total;

-- name: HasUserUsedShareLinks :one
SELECT EXISTS (
    SELECT 1 FROM box_share_links WHERE created_by_user_id = ?
) AS has_shared;

-- name: HasUserUsedShelley :one
-- Check if user has used shelley (recorded as a user_event).
SELECT EXISTS (
    SELECT 1 FROM user_events WHERE user_id = ? AND event = 'used_repl'
) AS has_used;
