-- name: GetNotificationChannels :many
SELECT * FROM notification_channels ORDER BY created_at ASC;

-- name: GetNotificationChannel :one
SELECT * FROM notification_channels WHERE channel_id = ?;

-- name: GetEnabledNotificationChannels :many
SELECT * FROM notification_channels WHERE enabled = 1 ORDER BY created_at ASC;

-- name: CreateNotificationChannel :one
INSERT INTO notification_channels (channel_id, channel_type, display_name, enabled, config)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateNotificationChannel :one
UPDATE notification_channels
SET display_name = ?,
    enabled = ?,
    config = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE channel_id = ?
RETURNING *;

-- name: DeleteNotificationChannel :exec
DELETE FROM notification_channels WHERE channel_id = ?;
