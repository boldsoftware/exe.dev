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
    id, 
    conversation_id, 
    model, 
    provider, 
    url, 
    LENGTH(request_body) as request_body_length,
    LENGTH(response_body) as response_body_length,
    status_code, 
    error, 
    duration_ms, 
    created_at,
    prefix_request_id,
    prefix_length
FROM llm_requests 
ORDER BY id DESC 
LIMIT ?;

-- name: GetLLMRequestBody :one
SELECT request_body FROM llm_requests WHERE id = ?;

-- name: GetLLMResponseBody :one
SELECT response_body FROM llm_requests WHERE id = ?;


