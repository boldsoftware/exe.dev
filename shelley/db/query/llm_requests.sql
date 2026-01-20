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
    duration_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;
