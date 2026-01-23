-- name: InsertPendingRegistration :exec
INSERT INTO pending_registrations (token, email, invite_code_id, expires_at)
VALUES (?, ?, ?, ?);

-- name: GetPendingRegistrationByToken :one
SELECT * FROM pending_registrations
WHERE token = ?;

-- name: DeletePendingRegistrationByToken :exec
DELETE FROM pending_registrations WHERE token = ?;
