-- name: GetUserIDByEmail :one
SELECT user_id FROM users WHERE canonical_email = ?;

-- name: InsertUser :exec
INSERT INTO users (user_id, email, canonical_email, created_for_login_with_exe, region) VALUES (?, ?, ?, ?, ?);

-- name: GetUserWithDetails :one
SELECT *
FROM users
WHERE user_id = ?;

-- name: GetUserByEmail :one
SELECT *
FROM users
WHERE canonical_email = ?;

-- name: GetEmailByUserID :one
SELECT email FROM users WHERE user_id = ?;

-- name: ListAllUsers :many
SELECT * FROM users ORDER BY created_at DESC;

-- name: SetUserRootSupport :exec
UPDATE users SET root_support = ? WHERE user_id = ?;

-- name: GetUserRootSupport :one
SELECT root_support FROM users WHERE user_id = ?;

-- name: CountUsersByType :many
-- CountUsersByType returns user counts grouped by login vs dev in a single scan.
SELECT created_for_login_with_exe, COUNT(*) AS count FROM users GROUP BY created_for_login_with_exe;

-- name: CountUsersByRegion :many
SELECT region, COUNT(*) AS count FROM users GROUP BY region;

-- name: ListPDXUsers :many
SELECT * FROM users WHERE region = 'pdx' ORDER BY created_at DESC, user_id DESC;

-- name: GetUserNewVMCreationDisabled :one
SELECT new_vm_creation_disabled FROM users WHERE user_id = ?;

-- name: SetUserNewVMCreationDisabled :exec
UPDATE users SET new_vm_creation_disabled = ? WHERE user_id = ?;

-- name: SetUserDiscord :exec
UPDATE users SET discord_id = ?, discord_username = ? WHERE user_id = ?;

-- name: GetAndIncrementNextSSHKeyNumber :one
UPDATE users SET next_ssh_key_number = next_ssh_key_number + 1
WHERE user_id = ?
RETURNING next_ssh_key_number - 1 AS key_number;

-- name: GetUserIsLockedOut :one
SELECT is_locked_out FROM users WHERE user_id = ?;

-- name: SetUserIsLockedOut :exec
UPDATE users SET is_locked_out = ? WHERE user_id = ?;

-- name: SetUserLimits :exec
UPDATE users SET limits = ? WHERE user_id = ?;

-- name: SetUserCgroupOverrides :exec
UPDATE users SET cgroup_overrides = ? WHERE user_id = ?;

-- name: SetUserNewsletterSubscribed :exec
UPDATE users SET newsletter_subscribed = ? WHERE user_id = ?;

-- name: DeleteUser :exec
DELETE FROM users WHERE user_id = ?;

-- name: SetUserAuthProvider :exec
UPDATE users SET auth_provider = ?, auth_provider_id = ? WHERE user_id = ?;

-- name: GetUserAuthProvider :one
SELECT auth_provider, auth_provider_id FROM users WHERE user_id = ?;

-- name: SetUserRegion :exec
UPDATE users SET region = ? WHERE user_id = ?;

-- name: SetUserRegionCAS :execresult
UPDATE users SET region = ? WHERE user_id = ? AND region = ?;

-- name: GetUserByDiscordUsername :one
SELECT * FROM users WHERE discord_username = ?;

-- name: UpdateUserEmail :exec
UPDATE users SET email = ?, canonical_email = ? WHERE user_id = ?;

-- name: GetAllUserCgroupOverrides :many
SELECT user_id, cgroup_overrides FROM users WHERE cgroup_overrides IS NOT NULL;
