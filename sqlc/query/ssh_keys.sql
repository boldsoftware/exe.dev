-- name: InsertSSHKeyForEmailUser :exec
INSERT INTO ssh_keys (user_id, public_key)
VALUES ((SELECT user_id FROM users WHERE email = ?), ?)
ON CONFLICT(public_key) DO NOTHING;
