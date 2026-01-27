-- name: GetUserDefaults :one
SELECT * FROM user_defaults WHERE user_id = ?;

-- name: UpsertUserDefaultNewVMEmail :exec
INSERT INTO user_defaults (user_id, new_vm_email, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(user_id) DO UPDATE SET
    new_vm_email = excluded.new_vm_email,
    updated_at = CURRENT_TIMESTAMP;

-- name: DeleteUserDefaultNewVMEmail :exec
UPDATE user_defaults SET new_vm_email = NULL, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?;
