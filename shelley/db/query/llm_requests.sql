-- name: InsertLLMRequest :one
INSERT INTO llm_requests (
    conversation_id,
    model,
    provider,
    url,
    request_body,
    response_body,
    status_code,
    error,
    duration_ms,
    prefix_request_id,
    prefix_length
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetLastRequestForConversation :one
SELECT * FROM llm_requests
WHERE conversation_id = ?
ORDER BY id DESC
LIMIT 1;

-- name: GetLLMRequestByID :one
SELECT * FROM llm_requests WHERE id = ?;

-- name: ListRecentLLMRequests :many
SELECT
    r.id,
    r.conversation_id,
    r.model,
    m.display_name as model_display_name,
    r.provider,
    r.url,
    LENGTH(r.request_body) as request_body_length,
    LENGTH(r.response_body) as response_body_length,
    r.status_code,
    r.error,
    r.duration_ms,
    r.created_at,
    r.prefix_request_id,
    r.prefix_length
FROM llm_requests r
LEFT JOIN models m ON r.model = m.model_id
ORDER BY r.id DESC
LIMIT ?;

-- name: GetLLMRequestBody :one
SELECT request_body FROM llm_requests WHERE id = ?;

-- name: GetLLMResponseBody :one
SELECT response_body FROM llm_requests WHERE id = ?;


