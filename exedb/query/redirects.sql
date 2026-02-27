-- name: InsertRedirect :exec
INSERT INTO redirects (key, target, expires_at)
VALUES (?, ?, ?);

-- name: GetRedirect :one
SELECT target FROM redirects WHERE key = ? AND expires_at > ?;

-- name: CleanupExpiredRedirects :exec
DELETE FROM redirects WHERE expires_at < ?;
