-- name: InsertSSHKeyForEmailUser :exec
INSERT INTO ssh_keys (user_id, public_key, comment, fingerprint)
SELECT u.user_id, ? as public_key, ? as comment, ? as fingerprint
FROM users u WHERE u.email = ?;

-- name: InsertSSHKeyForEmailUserIfNotExists :execresult
INSERT INTO ssh_keys (user_id, public_key, comment, fingerprint)
SELECT u.user_id, ? as public_key, ? as comment, ? as fingerprint
FROM users u WHERE u.email = ?
ON CONFLICT(public_key) DO NOTHING;

-- name: GetSSHKeysForUser :many
SELECT public_key, comment, added_at, last_used_at
FROM ssh_keys
WHERE user_id = ?
ORDER BY added_at DESC;

-- name: GetEmailBySSHKey :one
SELECT u.email
FROM ssh_keys s
JOIN users u ON s.user_id = u.user_id
WHERE s.public_key = ?;

-- name: GetUserIDBySSHKey :one
SELECT user_id FROM ssh_keys WHERE public_key = ?;

-- name: InsertSSHKey :exec
INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?);

-- name: GetUserWithSSHKey :one
SELECT u.*
FROM users u
JOIN ssh_keys s ON u.user_id = s.user_id
WHERE s.public_key = ?;

-- name: InsertSSHKeyIfNotExists :execresult
INSERT INTO ssh_keys (user_id, public_key, comment, fingerprint)
VALUES (?, ?, ?, ?)
ON CONFLICT(public_key) DO NOTHING;

-- name: GetSSHKeysForUserByEmail :many
SELECT public_key FROM ssh_keys WHERE user_id = (SELECT user_id FROM users WHERE email = ?) ORDER BY public_key;

-- name: DeleteSSHKeyForUser :one
DELETE FROM ssh_keys
WHERE user_id = ?
  AND public_key = ?
RETURNING 1 AS deleted;

-- name: UpdateSSHKeyLastUsed :exec
UPDATE ssh_keys SET last_used_at = CURRENT_TIMESTAMP WHERE public_key = ?;

-- name: GetSSHKeyByFingerprint :one
SELECT user_id, public_key FROM ssh_keys WHERE fingerprint = ?;
