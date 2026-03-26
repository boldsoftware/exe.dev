-- name: InsertPendingRegistration :exec
INSERT INTO pending_registrations (token, email, invite_code_id, expires_at, account_id)
VALUES (?, ?, ?, ?, ?);

-- name: GetPendingRegistrationByToken :one
SELECT * FROM pending_registrations
WHERE token = ?;

-- name: DeletePendingRegistrationByToken :exec
DELETE FROM pending_registrations WHERE token = ?;

-- name: GetUnexpiredPendingRegistrationByEmail :one
SELECT * FROM pending_registrations
WHERE email = ? AND expires_at > ? AND account_id IS NOT NULL
LIMIT 1;
