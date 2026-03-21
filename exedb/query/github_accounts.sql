-- name: GetGitHubAccount :one
SELECT * FROM github_accounts WHERE user_id = ? AND installation_id = ?;

-- name: GetGitHubAccountByTarget :one
SELECT * FROM github_accounts WHERE user_id = ? AND target_login = ?;

-- name: ListGitHubAccounts :many
SELECT * FROM github_accounts WHERE user_id = ? ORDER BY created_at;

-- name: UpsertGitHubAccount :exec
INSERT INTO github_accounts (user_id, github_login, installation_id, target_login, access_token, refresh_token, token_renewed_at, access_token_expires_at, refresh_token_expires_at)
VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?)
ON CONFLICT (user_id, target_login) DO UPDATE SET
    github_login = excluded.github_login,
    installation_id = excluded.installation_id,
    access_token = excluded.access_token,
    refresh_token = excluded.refresh_token,
    token_renewed_at = CURRENT_TIMESTAMP,
    access_token_expires_at = excluded.access_token_expires_at,
    refresh_token_expires_at = excluded.refresh_token_expires_at;

-- name: DeleteGitHubAccount :exec
DELETE FROM github_accounts WHERE user_id = ? AND installation_id = ?;

-- name: DeleteAllGitHubAccounts :exec
DELETE FROM github_accounts WHERE user_id = ?;

-- name: UpdateGitHubAccountTokens :exec
UPDATE github_accounts
SET access_token = ?,
    refresh_token = ?,
    token_renewed_at = CURRENT_TIMESTAMP,
    access_token_expires_at = ?,
    refresh_token_expires_at = ?
WHERE user_id = ? AND installation_id = ?;

-- name: ListAllGitHubAccounts :many
SELECT ga.*, u.email
FROM github_accounts ga
JOIN users u ON u.user_id = ga.user_id
ORDER BY ga.token_renewed_at DESC NULLS LAST;

-- name: ListGitHubAccountsNeedingRenewal :many
SELECT * FROM github_accounts
WHERE refresh_token != ''
  AND (
    -- Never renewed: needs renewal.
    token_renewed_at IS NULL
    -- Has a known expiry: renew when less than 30 days remain on the refresh token.
    OR (refresh_token_expires_at IS NOT NULL AND refresh_token_expires_at < datetime('now', '+30 days'))
    -- No known expiry (legacy rows): renew every 30 days to keep the refresh token alive.
    OR (refresh_token_expires_at IS NULL AND token_renewed_at < datetime('now', '-30 days'))
  )
ORDER BY refresh_token_expires_at ASC NULLS FIRST
LIMIT ?;
