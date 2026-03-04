-- name: InsertIntegrationAttachment :exec
INSERT INTO integration_attachments (integration_id, box_id)
VALUES (?, ?);

-- name: DeleteIntegrationAttachment :exec
DELETE FROM integration_attachments WHERE integration_id = ? AND box_id = ?;

-- name: ListIntegrationAttachments :many
SELECT *
FROM integration_attachments
WHERE integration_id = ?
ORDER BY created_at DESC, rowid DESC;

-- name: DeleteIntegrationAttachmentsByBoxID :exec
DELETE FROM integration_attachments WHERE box_id = ?;
