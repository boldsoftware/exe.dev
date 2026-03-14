-- name: GetGitHubAccount :one
SELECT * FROM github_accounts WHERE user_id = ? AND installation_id = ?;

-- name: GetGitHubAccountByTarget :one
SELECT * FROM github_accounts WHERE user_id = ? AND target_login = ?;

-- name: ListGitHubAccounts :many
SELECT * FROM github_accounts WHERE user_id = ? ORDER BY created_at;

-- name: UpsertGitHubAccount :exec
INSERT INTO github_accounts (user_id, github_login, installation_id, target_login, access_token, refresh_token)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (user_id, target_login) DO UPDATE SET
    github_login = excluded.github_login,
    installation_id = excluded.installation_id,
    access_token = excluded.access_token,
    refresh_token = excluded.refresh_token;

-- name: DeleteGitHubAccount :exec
DELETE FROM github_accounts WHERE user_id = ? AND installation_id = ?;

-- name: DeleteAllGitHubAccounts :exec
DELETE FROM github_accounts WHERE user_id = ?;
