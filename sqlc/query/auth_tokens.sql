-- name: GetAuthTokenInfo :one
SELECT user_id, machine_name, expires_at, used_at
FROM auth_tokens
WHERE token = ?;

-- name: UpdateAuthTokenUsedAt :exec
UPDATE auth_tokens SET used_at = CURRENT_TIMESTAMP WHERE token = ?;
