-- name: DeleteEmailVerificationByToken :exec
DELETE FROM email_verifications WHERE token = ?;

-- name: InsertEmailVerification :exec
INSERT INTO email_verifications (token, email, user_id, expires_at, verification_code, invite_code_id, is_new_user, redirect_url, return_host, response_mode, callback_uri)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetEmailVerificationByToken :one
SELECT * FROM email_verifications
WHERE token = ?;

-- name: InsertOrReplaceEmailVerification :exec
INSERT OR REPLACE INTO email_verifications (token, user_id, email, expires_at, verification_code, invite_code_id, redirect_url, return_host, response_mode, callback_uri)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetEmailVerificationByPartialToken :one
SELECT user_id
FROM email_verifications
WHERE token = ? AND expires_at > datetime('now')
LIMIT 1;

-- name: UpdateEmailVerificationCode :exec
UPDATE email_verifications SET verification_code = ? WHERE token = ?;

-- name: IncrementEmailVerificationCodeAttempts :execresult
UPDATE email_verifications SET verification_code_attempts = verification_code_attempts + 1 WHERE token = ?;

-- name: GetEmailVerificationByEmail :one
SELECT * FROM email_verifications
WHERE email = ? AND verification_code IS NOT NULL AND expires_at > datetime('now')
ORDER BY created_at DESC
LIMIT 1;
