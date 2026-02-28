-- Invite code pool management

-- name: AddInviteCodeToPool :exec
INSERT INTO invite_code_pool (code) VALUES (?);

-- name: DrawInviteCodeFromPool :one
-- Removes and returns a random code from the pool
DELETE FROM invite_code_pool WHERE code = (
    SELECT code FROM invite_code_pool ORDER BY RANDOM() LIMIT 1
) RETURNING code;

-- Invite code management

-- name: CreateInviteCode :one
-- Creates an invite code (drawn from pool beforehand)
INSERT INTO invite_codes (code, plan_type, assigned_to_user_id, assigned_by, assigned_for, is_batch)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetInviteCodeByCode :one
SELECT * FROM invite_codes WHERE code = ?;

-- name: GetInviteCodeByID :one
SELECT * FROM invite_codes WHERE id = ?;

-- name: UseInviteCode :exec
-- Marks an invite code as used by a user
UPDATE invite_codes SET used_by_user_id = ?, used_at = CURRENT_TIMESTAMP
WHERE id = ? AND used_by_user_id IS NULL;

-- name: ListUnusedInviteCodesForUser :many
-- Lists all unused invite codes assigned to a specific user
SELECT * FROM invite_codes
WHERE assigned_to_user_id = ? AND used_by_user_id IS NULL
ORDER BY assigned_at DESC;

-- name: CountUnusedInviteCodesForUser :one
-- Counts unused AND unallocated invites for the user (available to allocate)
SELECT COUNT(*) FROM invite_codes
WHERE assigned_to_user_id = ? AND used_by_user_id IS NULL AND allocated_at IS NULL;

-- name: GetNextUnallocatedInviteForUser :one
-- Gets the next unallocated invite code for a user
SELECT * FROM invite_codes
WHERE assigned_to_user_id = ? AND used_by_user_id IS NULL AND allocated_at IS NULL
ORDER BY assigned_at ASC
LIMIT 1;

-- name: AllocateInviteCode :exec
-- Marks an invite code as allocated (shown to the user)
UPDATE invite_codes SET allocated_at = CURRENT_TIMESTAMP
WHERE id = ? AND allocated_at IS NULL;

-- name: ListUnusedSystemInviteCodes :many
-- Lists all unused system invite codes (not assigned to any user), excluding batch codes
SELECT * FROM invite_codes
WHERE assigned_to_user_id IS NULL AND used_by_user_id IS NULL AND is_batch = 0
ORDER BY assigned_at DESC;

-- name: CountUnallocatedInviteCodesByUser :many
-- Counts unallocated invite codes grouped by user (matches what users see in web UI)
SELECT assigned_to_user_id, COUNT(*) as count FROM invite_codes
WHERE assigned_to_user_id IS NOT NULL AND used_by_user_id IS NULL AND allocated_at IS NULL
GROUP BY assigned_to_user_id;

-- name: GetInviteCodeStatsForUser :one
-- Counts invite status totals for a specific inviter (all-time codes assigned to user)
SELECT
    COUNT(*) AS total_all_time_given,
    COUNT(CASE WHEN allocated_at IS NOT NULL THEN 1 END) AS allocated_count,
    COUNT(CASE WHEN used_by_user_id IS NOT NULL THEN 1 END) AS accepted_count
FROM invite_codes
WHERE assigned_to_user_id = ?;

-- User billing exemption

-- name: SetUserBillingExemption :exec
UPDATE users SET
    billing_exemption = ?,
    billing_trial_ends_at = ?,
    signed_up_with_invite_id = ?
WHERE user_id = ?;

-- name: GetUserBillingExemption :one
SELECT billing_exemption, billing_trial_ends_at, signed_up_with_invite_id
FROM users WHERE user_id = ?;

-- name: ListAllInviteCodesWithEmails :many
-- Lists all invite codes with giver and recipient emails for debug page
SELECT
    ic.id,
    ic.code,
    ic.plan_type,
    ic.assigned_to_user_id,
    giver.email AS giver_email,
    ic.assigned_at,
    ic.assigned_by,
    ic.assigned_for,
    ic.used_by_user_id,
    recipient.email AS recipient_email,
    ic.used_at,
    ic.allocated_at
FROM invite_codes ic
LEFT JOIN users giver ON ic.assigned_to_user_id = giver.user_id
LEFT JOIN users recipient ON ic.used_by_user_id = recipient.user_id
ORDER BY ic.id DESC;
