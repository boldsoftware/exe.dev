-- name: UpsertPushToken :exec
INSERT INTO push_tokens (user_id, token, platform, environment)
VALUES (?, ?, ?, ?)
ON CONFLICT(token) DO UPDATE SET
    user_id = excluded.user_id,
    environment = excluded.environment,
    last_used_at = CURRENT_TIMESTAMP;

-- name: DeletePushToken :exec
DELETE FROM push_tokens WHERE token = ? AND user_id = ?;

-- name: GetPushTokensByUserID :many
SELECT * FROM push_tokens WHERE user_id = ? ORDER BY created_at DESC;

-- name: HasPushTokens :one
SELECT EXISTS(SELECT 1 FROM push_tokens WHERE user_id = ?) AS has_tokens;
