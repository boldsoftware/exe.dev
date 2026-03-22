-- name: InsertAppToken :exec
INSERT INTO app_tokens (token, user_id, name, expires_at)
VALUES (?, ?, ?, ?);

-- name: GetAppTokenInfo :one
SELECT *
FROM app_tokens
WHERE token = ?;

-- name: UpdateAppTokenLastUsed :exec
UPDATE app_tokens SET last_used_at = CURRENT_TIMESTAMP WHERE token = ?;

-- name: DeleteAppToken :exec
DELETE FROM app_tokens WHERE token = ? AND user_id = ?;

-- name: GetAppTokensByUserID :many
SELECT *
FROM app_tokens
WHERE user_id = ?
ORDER BY created_at DESC, rowid DESC;
