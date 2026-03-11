-- name: GetGitHubAccount :one
SELECT * FROM github_accounts WHERE user_id = ?;

-- name: InsertGitHubAccount :exec
INSERT INTO github_accounts (user_id, github_login, installation_id, access_token, refresh_token) VALUES (?, ?, ?, ?, ?);

-- name: DeleteGitHubAccount :exec
DELETE FROM github_accounts WHERE user_id = ?;
