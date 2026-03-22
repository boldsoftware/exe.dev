-- name: GetGitHubUserToken :one
SELECT * FROM github_user_tokens WHERE user_id = ? AND github_login = ?;

-- name: ListGitHubUserTokens :many
SELECT * FROM github_user_tokens WHERE user_id = ? ORDER BY created_at;

-- name: UpsertGitHubUserToken :exec
INSERT INTO github_user_tokens (user_id, github_login, access_token, refresh_token, token_renewed_at, access_token_expires_at, refresh_token_expires_at)
VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?)
ON CONFLICT (user_id, github_login) DO UPDATE SET
    access_token = excluded.access_token,
    refresh_token = excluded.refresh_token,
    token_renewed_at = CURRENT_TIMESTAMP,
    access_token_expires_at = excluded.access_token_expires_at,
    refresh_token_expires_at = excluded.refresh_token_expires_at;

-- name: DeleteAllGitHubUserTokens :exec
DELETE FROM github_user_tokens WHERE user_id = ?;

-- name: DeleteOrphanedGitHubUserTokens :exec
DELETE FROM github_user_tokens AS t
WHERE t.user_id = ?
  AND NOT EXISTS (
    SELECT 1 FROM github_installations i
    WHERE i.user_id = t.user_id AND i.github_login = t.github_login
  );

-- name: UpdateGitHubUserToken :exec
UPDATE github_user_tokens
SET access_token = ?,
    refresh_token = ?,
    token_renewed_at = CURRENT_TIMESTAMP,
    access_token_expires_at = ?,
    refresh_token_expires_at = ?
WHERE user_id = ? AND github_login = ?;

-- name: ListGitHubUserTokensNeedingRenewal :many
SELECT * FROM github_user_tokens
WHERE refresh_token != ''
  AND (
    token_renewed_at IS NULL
    OR (refresh_token_expires_at IS NOT NULL AND refresh_token_expires_at < datetime('now', '+30 days'))
    OR (refresh_token_expires_at IS NULL AND token_renewed_at < datetime('now', '-30 days'))
  )
ORDER BY refresh_token_expires_at ASC NULLS FIRST
LIMIT ?;

-- name: ListAllGitHubUserTokens :many
SELECT t.*, u.email
FROM github_user_tokens t
JOIN users u ON u.user_id = t.user_id
ORDER BY t.token_renewed_at DESC NULLS LAST;

-- name: GetGitHubInstallation :one
SELECT * FROM github_installations WHERE user_id = ? AND github_app_installation_id = ?;

-- name: GetGitHubInstallationByTarget :one
SELECT * FROM github_installations WHERE user_id = ? AND github_account_login = ?;

-- name: ListGitHubInstallations :many
SELECT * FROM github_installations WHERE user_id = ? ORDER BY created_at;

-- name: UpsertGitHubInstallation :exec
INSERT INTO github_installations (user_id, github_login, github_app_installation_id, github_account_login)
VALUES (?, ?, ?, ?)
ON CONFLICT (user_id, github_app_installation_id) DO UPDATE SET
    github_login = excluded.github_login,
    github_account_login = excluded.github_account_login;

-- name: DeleteGitHubInstallationByTarget :exec
DELETE FROM github_installations WHERE user_id = ? AND github_account_login = ? AND github_app_installation_id != ?;

-- name: DeleteGitHubInstallation :exec
DELETE FROM github_installations WHERE user_id = ? AND github_app_installation_id = ?;

-- name: DeleteAllGitHubInstallations :exec
DELETE FROM github_installations WHERE user_id = ?;

-- name: ListAllGitHubInstallationsWithTokens :many
SELECT i.*, t.access_token, t.refresh_token, t.token_renewed_at, t.access_token_expires_at, t.refresh_token_expires_at
FROM github_installations i
JOIN github_user_tokens t ON t.user_id = i.user_id AND t.github_login = i.github_login
ORDER BY i.created_at;
