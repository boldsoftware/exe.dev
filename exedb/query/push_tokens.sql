-- name: UpsertPushToken :exec
INSERT INTO push_tokens (user_id, token, platform)
VALUES (?, ?, ?)
ON CONFLICT(token) DO UPDATE SET
    user_id = excluded.user_id,
    last_used_at = CURRENT_TIMESTAMP;

-- name: DeletePushToken :exec
DELETE FROM push_tokens WHERE token = ? AND user_id = ?;

-- name: DeletePushTokensByUserID :exec
DELETE FROM push_tokens WHERE user_id = ?;

-- name: GetPushTokensByUserID :many
SELECT * FROM push_tokens WHERE user_id = ? ORDER BY created_at DESC;

-- name: UpdatePushTokenLastUsed :exec
UPDATE push_tokens SET last_used_at = CURRENT_TIMESTAMP WHERE token = ?;

-- name: HasPushTokens :one
SELECT EXISTS(SELECT 1 FROM push_tokens WHERE user_id = ?) AS has_tokens;
