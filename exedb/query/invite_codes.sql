-- Invite code pool management

-- name: AddInviteCodeToPool :exec
INSERT INTO invite_code_pool (code) VALUES (?);

-- name: CountInviteCodePool :one
SELECT COUNT(*) FROM invite_code_pool;

-- name: DrawInviteCodeFromPool :one
-- Removes and returns a random code from the pool
DELETE FROM invite_code_pool WHERE code = (
    SELECT code FROM invite_code_pool ORDER BY RANDOM() LIMIT 1
) RETURNING code;

-- Invite code management

-- name: CreateInviteCode :one
-- Creates an invite code (drawn from pool beforehand)
INSERT INTO invite_codes (code, plan_type, assigned_to_user_id, assigned_by, assigned_for)
VALUES (?, ?, ?, ?, ?)
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
-- Lists all unused system invite codes (not assigned to any user)
SELECT * FROM invite_codes
WHERE assigned_to_user_id IS NULL AND used_by_user_id IS NULL
ORDER BY assigned_at DESC;

-- name: CountUnusedSystemInviteCodes :one
SELECT COUNT(*) FROM invite_codes
WHERE assigned_to_user_id IS NULL AND used_by_user_id IS NULL;

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
