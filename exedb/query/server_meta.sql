-- name: GetPreferredExelet :one
SELECT value FROM server_meta WHERE key = 'preferred_exelet';

-- name: SetPreferredExelet :exec
INSERT INTO server_meta (key, value, updated_at) VALUES ('preferred_exelet', ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;

-- name: ClearPreferredExelet :exec
DELETE FROM server_meta WHERE key = 'preferred_exelet';

-- name: GetNewThrottleEnabled :one
SELECT value FROM server_meta WHERE key = 'new_throttle_enabled';

-- name: SetNewThrottleEnabled :exec
INSERT INTO server_meta (key, value, updated_at) VALUES ('new_throttle_enabled', ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;

-- name: GetNewThrottleEmailPatterns :one
SELECT value FROM server_meta WHERE key = 'new_throttle_email_patterns';

-- name: SetNewThrottleEmailPatterns :exec
INSERT INTO server_meta (key, value, updated_at) VALUES ('new_throttle_email_patterns', ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;

-- name: GetNewThrottleMessage :one
SELECT value FROM server_meta WHERE key = 'new_throttle_message';

-- name: SetNewThrottleMessage :exec
INSERT INTO server_meta (key, value, updated_at) VALUES ('new_throttle_message', ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;

-- name: GetLoginCreationDisabled :one
SELECT value FROM server_meta WHERE key = 'login_creation_disabled';

-- name: SetLoginCreationDisabled :exec
INSERT INTO server_meta (key, value, updated_at) VALUES ('login_creation_disabled', ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;

-- name: GetSignupPOWEnabled :one
SELECT value FROM server_meta WHERE key = 'signup_pow_enabled';

-- name: SetSignupPOWEnabled :exec
INSERT INTO server_meta (key, value, updated_at) VALUES ('signup_pow_enabled', ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;

-- name: GetIPAbuseFilterDisabled :one
SELECT value FROM server_meta WHERE key = 'ip_abuse_filter_disabled';

-- name: SetIPAbuseFilterDisabled :exec
INSERT INTO server_meta (key, value, updated_at) VALUES ('ip_abuse_filter_disabled', ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;

-- name: GetGLBRolloutPrefixes :one
SELECT value FROM server_meta WHERE key = 'glb_rollout_prefixes';

-- name: SetGLBRolloutPrefixes :exec
INSERT INTO server_meta (key, value, updated_at) VALUES ('glb_rollout_prefixes', ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;

-- name: GetLastBouncesPoll :one
SELECT value FROM server_meta WHERE key = 'last_bounces_poll';

-- name: SetLastBouncesPoll :exec
INSERT INTO server_meta (key, value, updated_at) VALUES ('last_bounces_poll', ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;
