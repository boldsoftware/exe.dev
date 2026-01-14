-- name: DeleteEmailVerificationByToken :exec
DELETE FROM email_verifications WHERE token = ?;

-- name: InsertEmailVerification :exec
INSERT INTO email_verifications (token, email, user_id, expires_at, verification_code, invite_code_id)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetEmailVerificationByToken :one
SELECT user_id, email, expires_at, verification_code, invite_code_id
FROM email_verifications
WHERE token = ?;

-- name: InsertOrReplaceEmailVerification :exec
INSERT OR REPLACE INTO email_verifications (token, user_id, email, expires_at, verification_code, invite_code_id)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetEmailVerificationByPartialToken :one
SELECT user_id
FROM email_verifications
WHERE token = ? AND expires_at > datetime('now')
LIMIT 1;
