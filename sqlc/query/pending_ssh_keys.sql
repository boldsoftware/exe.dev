-- name: GetPendingSSHKeyByToken :one
SELECT public_key, user_email, expires_at
FROM pending_ssh_keys
WHERE token = ?;

-- name: DeletePendingSSHKeyByToken :exec
DELETE FROM pending_ssh_keys WHERE token = ?;
