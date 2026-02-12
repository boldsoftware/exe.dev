-- name: CreateMessage :one
INSERT INTO messages (message_id, conversation_id, sequence_id, type, llm_data, user_data, usage_data, display_data, excluded_from_context)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetNextSequenceID :one
SELECT COALESCE(MAX(sequence_id), 0) + 1 
FROM messages 
WHERE conversation_id = ?;

-- name: GetMessage :one
SELECT * FROM messages
WHERE message_id = ?;

-- name: ListMessages :many
SELECT * FROM messages
WHERE conversation_id = ?
ORDER BY sequence_id ASC;

-- name: ListMessagesForContext :many
SELECT * FROM messages
WHERE conversation_id = ? AND excluded_from_context = FALSE
ORDER BY sequence_id ASC;

-- name: ListMessagesPaginated :many
SELECT * FROM messages
WHERE conversation_id = ?
ORDER BY sequence_id ASC
LIMIT ? OFFSET ?;

-- name: ListMessagesByType :many
SELECT * FROM messages
WHERE conversation_id = ? AND type = ?
ORDER BY sequence_id ASC;

-- name: GetLatestMessage :one
SELECT * FROM messages
WHERE conversation_id = ?
ORDER BY sequence_id DESC
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
WHERE conversation_id = ? AND sequence_id > ?
ORDER BY sequence_id ASC;

-- name: UpdateMessageUserData :exec
UPDATE messages SET user_data = ? WHERE message_id = ?;
