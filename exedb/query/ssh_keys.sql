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
SELECT * FROM ssh_keys
WHERE user_id = ?
ORDER BY id ASC;

-- name: GetEmailBySSHKey :one
SELECT u.email
FROM ssh_keys s
JOIN users u ON s.user_id = u.user_id
WHERE s.public_key = ?;

-- name: GetUserIDBySSHKey :one
SELECT user_id FROM ssh_keys WHERE public_key = ?;

-- name: InsertSSHKey :exec
INSERT INTO ssh_keys (user_id, public_key, comment, fingerprint) VALUES (?, ?, ?, ?);

-- name: GetUserWithSSHKey :one
SELECT u.*
FROM users u
JOIN ssh_keys s ON u.user_id = s.user_id
WHERE s.public_key = ?;

-- name: InsertSSHKeyIfNotExists :execresult
INSERT INTO ssh_keys (user_id, public_key, comment, fingerprint)
VALUES (?, ?, ?, ?)
ON CONFLICT(public_key) DO NOTHING;

-- name: UpdateSSHKeyLastUsed :exec
-- Callers should deduplicate per UTC day, but this is also safe to call repeatedly.
UPDATE ssh_keys SET last_used_at = DATE('now') WHERE public_key = ? AND (last_used_at IS NULL OR last_used_at < DATE('now'));

-- name: GetSSHKeyByFingerprint :one
SELECT user_id, public_key FROM ssh_keys WHERE fingerprint = ?;

-- name: GetSSHKeysForUserByComment :many
SELECT * FROM ssh_keys WHERE user_id = ? AND comment = ?;

-- name: GetSSHKeysForUserByFingerprint :many
SELECT * FROM ssh_keys WHERE user_id = ? AND fingerprint = ?;

-- name: DeleteSSHKeyByID :exec
DELETE FROM ssh_keys WHERE id = ? AND user_id = ?;

-- name: UpdateSSHKeyComment :exec
UPDATE ssh_keys SET comment = ? WHERE id = ? AND user_id = ?;
