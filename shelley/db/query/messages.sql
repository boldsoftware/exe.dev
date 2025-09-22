-- name: CreateMessage :one
INSERT INTO messages (message_id, conversation_id, type, llm_data, user_data, usage_data)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetMessage :one
SELECT * FROM messages
WHERE message_id = ?;

-- name: ListMessages :many
SELECT * FROM messages
WHERE conversation_id = ?
ORDER BY created_at ASC;

-- name: ListMessagesPaginated :many
SELECT * FROM messages
WHERE conversation_id = ?
ORDER BY created_at ASC
LIMIT ? OFFSET ?;

-- name: ListMessagesByType :many
SELECT * FROM messages
WHERE conversation_id = ? AND type = ?
ORDER BY created_at ASC;

-- name: GetLatestMessage :one
SELECT * FROM messages
WHERE conversation_id = ?
ORDER BY created_at DESC
LIMIT 1;

-- name: DeleteMessage :exec
DELETE FROM messages
WHERE message_id = ?;

-- name: DeleteConversationMessages :exec
DELETE FROM messages
WHERE conversation_id = ?;

-- name: CountMessagesInConversation :one
SELECT COUNT(*) FROM messages
WHERE conversation_id = ?;

-- name: CountMessagesByType :one
SELECT COUNT(*) FROM messages
WHERE conversation_id = ? AND type = ?;

-- name: ListMessagesSince :many
SELECT * FROM messages
WHERE conversation_id = ? AND created_at > ?
ORDER BY created_at ASC;
