-- name: GetModels :many
SELECT * FROM models ORDER BY created_at ASC;

-- name: GetModel :one
SELECT * FROM models WHERE model_id = ?;

-- name: CreateModel :one
INSERT INTO models (model_id, display_name, provider_type, endpoint, api_key, model_name, max_tokens, tags)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateModel :one
UPDATE models
SET display_name = ?,
    provider_type = ?,
    endpoint = ?,
    api_key = ?,
    model_name = ?,
    max_tokens = ?,
    tags = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE model_id = ?
RETURNING *;

-- name: DeleteModel :exec
DELETE FROM models WHERE model_id = ?;
