-- name: GetGitHubAccount :one
SELECT * FROM github_accounts WHERE user_id = ? AND installation_id = ?;

-- name: GetGitHubAccountByTarget :one
SELECT * FROM github_accounts WHERE user_id = ? AND target_login = ?;

-- name: ListGitHubAccounts :many
SELECT * FROM github_accounts WHERE user_id = ? ORDER BY created_at;

-- name: InsertGitHubAccount :exec
INSERT INTO github_accounts (user_id, github_login, installation_id, target_login, access_token, refresh_token) VALUES (?, ?, ?, ?, ?, ?);

-- name: DeleteGitHubAccount :exec
DELETE FROM github_accounts WHERE user_id = ? AND installation_id = ?;

-- name: DeleteAllGitHubAccounts :exec
DELETE FROM github_accounts WHERE user_id = ?;
