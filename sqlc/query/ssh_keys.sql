-- name: InsertSSHKeyForEmailUser :exec
INSERT INTO ssh_keys (user_id, public_key)
VALUES ((SELECT user_id FROM users WHERE email = ?), ?)
ON CONFLICT(public_key) DO NOTHING;

-- name: GetSSHKeysForUser :many
SELECT public_key
FROM ssh_keys
WHERE user_id = ?
ORDER BY added_at DESC;

-- name: GetEmailBySSHKey :one
SELECT u.email
FROM ssh_keys s
JOIN users u ON s.user_id = u.user_id
WHERE s.public_key = ?;
