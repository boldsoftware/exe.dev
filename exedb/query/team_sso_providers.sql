-- name: InsertTeamSSOProvider :exec
INSERT INTO team_sso_providers (team_id, provider_type, issuer_url, client_id, client_secret, display_name, auth_url, token_url, userinfo_url, last_discovery_at)
VALUES (?, 'oidc', ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP);

-- name: UpdateTeamSSOProvider :exec
UPDATE team_sso_providers
SET issuer_url = ?, client_id = ?, client_secret = ?, display_name = ?,
    auth_url = ?, token_url = ?, userinfo_url = ?, last_discovery_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE team_id = ?;

-- name: DeleteTeamSSOProvider :exec
DELETE FROM team_sso_providers WHERE team_id = ?;

-- name: GetTeamSSOProvider :one
SELECT * FROM team_sso_providers WHERE team_id = ?;

-- name: GetTeamSSOProviderByIssuer :one
SELECT * FROM team_sso_providers WHERE issuer_url = ?;

-- name: GetTeamSSOProviderByID :one
SELECT * FROM team_sso_providers WHERE id = ?;
