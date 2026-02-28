-- name: InsertOAuthState :exec
INSERT INTO oauth_states (state, provider, email, user_id, is_new_user, invite_code_id, team_invite_token, redirect_url, return_host, login_with_exe, ssh_verification_token, hostname, prompt, image, expires_at, sso_provider_id, response_mode, callback_uri)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ConsumeOAuthState :one
DELETE FROM oauth_states WHERE state = ? AND expires_at > CURRENT_TIMESTAMP RETURNING *;

-- name: CleanupExpiredOAuthStates :exec
DELETE FROM oauth_states WHERE expires_at < ?;
