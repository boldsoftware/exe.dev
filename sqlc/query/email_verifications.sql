-- name: DeleteEmailVerificationByToken :exec
DELETE FROM email_verifications WHERE token = ?;

-- name: InsertEmailVerification :exec
INSERT INTO email_verifications (token, email, user_id, expires_at)
VALUES (?, ?, ?, ?);

-- name: GetEmailVerificationByToken :one
SELECT user_id, email, expires_at
FROM email_verifications
WHERE token = ?;

-- name: InsertOrReplaceEmailVerification :exec
INSERT OR REPLACE INTO email_verifications (token, user_id, email, expires_at)
VALUES (?, ?, ?, ?);

-- name: GetEmailVerificationByEmail :one
SELECT user_id, token
FROM email_verifications
WHERE email = ?;

-- name: GetEmailVerificationByPartialToken :one
SELECT user_id, token
FROM email_verifications
WHERE token LIKE ? AND expires_at > datetime('now')
LIMIT 1;
