-- name: GetExe1Token :one
SELECT exe0 FROM exe1_tokens WHERE exe1 = ? AND expires_at >= ?;

-- name: GetExe1TokenByExe0 :one
SELECT exe1 FROM exe1_tokens WHERE exe0 = ? AND expires_at >= ?;

-- name: InsertExe1Token :exec
INSERT INTO exe1_tokens (exe1, exe0, expires_at) VALUES (?, ?, ?);

-- name: DeleteExpiredExe1Tokens :exec
DELETE FROM exe1_tokens WHERE expires_at < ?;
