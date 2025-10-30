-- name: GetAuthTokenInfo :one
SELECT *
FROM auth_tokens
WHERE token = ?;

-- name: UpdateAuthTokenUsedAt :exec
UPDATE auth_tokens SET used_at = CURRENT_TIMESTAMP WHERE token = ?;
