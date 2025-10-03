-- name: GetPendingSSHKeyByToken :one
SELECT *
FROM pending_ssh_keys
WHERE token = ?;

-- name: DeletePendingSSHKeyByToken :exec
DELETE FROM pending_ssh_keys WHERE token = ?;

-- name: GetPendingSSHKeyEmailByPublicKey :one
SELECT user_email FROM pending_ssh_keys WHERE public_key = ?;

-- name: InsertPendingSSHKey :exec
INSERT INTO pending_ssh_keys (token, public_key, user_email, expires_at)
VALUES (?, ?, ?, ?);
