-- name: InsertPasskey :exec
INSERT INTO passkeys (user_id, credential_id, public_key, sign_count, aaguid, name, flags)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetPasskeysByUserID :many
SELECT id, user_id, credential_id, public_key, sign_count, aaguid, name, created_at, last_used_at, flags
FROM passkeys
WHERE user_id = ?
ORDER BY created_at DESC;

-- name: GetPasskeyByCredentialID :one
SELECT id, user_id, credential_id, public_key, sign_count, aaguid, name, created_at, last_used_at, flags
FROM passkeys
WHERE credential_id = ?;

-- name: UpdatePasskeySignCount :exec
UPDATE passkeys
SET sign_count = ?, last_used_at = CURRENT_TIMESTAMP
WHERE credential_id = ? AND user_id = ?;

-- name: DeletePasskey :exec
DELETE FROM passkeys WHERE id = ? AND user_id = ?;

-- name: InsertPasskeyChallenge :exec
INSERT INTO passkey_challenges (challenge, session_data, user_id, expires_at)
VALUES (?, ?, ?, ?);

-- name: GetPasskeyChallenge :one
SELECT challenge, session_data, user_id, expires_at
FROM passkey_challenges
WHERE challenge = ?;

-- name: DeletePasskeyChallenge :exec
DELETE FROM passkey_challenges WHERE challenge = ?;

-- name: CleanupExpiredPasskeyChallenges :exec
DELETE FROM passkey_challenges WHERE expires_at < ?;
