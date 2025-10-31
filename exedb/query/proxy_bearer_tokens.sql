-- name: InsertProxyBearerToken :exec
INSERT INTO proxy_bearer_tokens (token, user_id, box_id, expires_at)
VALUES (?, ?, ?, ?);

-- name: GetProxyBearerToken :one
SELECT token, user_id, box_id, expires_at, created_at, last_used_at
FROM proxy_bearer_tokens
WHERE token = ?;

-- name: UpdateProxyBearerTokenLastUsed :exec
UPDATE proxy_bearer_tokens
SET last_used_at = CURRENT_TIMESTAMP
WHERE token = ?;
