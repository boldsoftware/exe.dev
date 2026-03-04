-- name: InsertIntegration :exec
INSERT INTO integrations (integration_id, owner_user_id, type, config, name)
VALUES (?, ?, ?, ?, ?);

-- name: UpdateIntegrationName :exec
UPDATE integrations SET name = ? WHERE integration_id = ? AND owner_user_id = ?;

-- name: GetIntegration :one
SELECT *
FROM integrations
WHERE integration_id = ?;

-- name: GetIntegrationByOwnerAndName :one
SELECT *
FROM integrations
WHERE owner_user_id = ? AND name = ?;

-- name: ListIntegrationsByUser :many
SELECT *
FROM integrations
WHERE owner_user_id = ?
ORDER BY created_at DESC, rowid DESC;

-- name: DeleteIntegration :exec
DELETE FROM integrations WHERE integration_id = ? AND owner_user_id = ?;

-- name: GetAttachedIntegrationByOwnerNameAndBoxID :one
SELECT integrations.*
FROM integrations
JOIN integration_attachments ON integrations.integration_id = integration_attachments.integration_id
WHERE integrations.owner_user_id = ? AND integrations.name = ? AND integration_attachments.box_id = ?;
