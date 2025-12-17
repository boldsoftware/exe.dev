-- name: GetPreferredExelet :one
SELECT value FROM server_meta WHERE key = 'preferred_exelet';

-- name: SetPreferredExelet :exec
INSERT INTO server_meta (key, value, updated_at) VALUES ('preferred_exelet', ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;

-- name: ClearPreferredExelet :exec
DELETE FROM server_meta WHERE key = 'preferred_exelet';
