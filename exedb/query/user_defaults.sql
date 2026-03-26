-- name: GetUserDefaults :one
SELECT * FROM user_defaults WHERE user_id = ?;

-- name: UpsertUserDefaultNewVMEmail :exec
INSERT INTO user_defaults (user_id, new_vm_email, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(user_id) DO UPDATE SET
    new_vm_email = excluded.new_vm_email,
    updated_at = CURRENT_TIMESTAMP;

-- name: DeleteUserDefaultNewVMEmail :exec
UPDATE user_defaults SET new_vm_email = NULL, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?;

-- name: UpsertUserDefaultAnycastNetwork :exec
INSERT INTO user_defaults (user_id, anycast_network, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(user_id) DO UPDATE SET
    anycast_network = excluded.anycast_network,
    updated_at = CURRENT_TIMESTAMP;

-- name: DeleteUserDefaultAnycastNetwork :exec
UPDATE user_defaults SET anycast_network = NULL, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?;

-- name: UpsertUserDefaultGitHubIntegration :exec
INSERT INTO user_defaults (user_id, github_integration, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(user_id) DO UPDATE SET
    github_integration = excluded.github_integration,
    updated_at = CURRENT_TIMESTAMP;

-- name: DeleteUserDefaultGitHubIntegration :exec
UPDATE user_defaults SET github_integration = NULL, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?;

-- name: UpsertUserDefaultNewSetupScript :exec
INSERT INTO user_defaults (user_id, new_setup_script, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(user_id) DO UPDATE SET
    new_setup_script = excluded.new_setup_script,
    updated_at = CURRENT_TIMESTAMP;

-- name: DeleteUserDefaultNewSetupScript :exec
UPDATE user_defaults SET new_setup_script = NULL, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?;
