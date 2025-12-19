-- name: CreateConversation :one
INSERT INTO conversations (conversation_id, slug, user_initiated, cwd)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetConversation :one
SELECT * FROM conversations
WHERE conversation_id = ?;

-- name: GetConversationBySlug :one
SELECT * FROM conversations
WHERE slug = ?;

-- name: ListConversations :many
SELECT * FROM conversations
WHERE archived = FALSE
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: ListArchivedConversations :many
SELECT * FROM conversations
WHERE archived = TRUE
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: SearchConversations :many
SELECT * FROM conversations
WHERE slug LIKE '%' || ? || '%' AND archived = FALSE
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: SearchArchivedConversations :many
SELECT * FROM conversations
WHERE slug LIKE '%' || ? || '%' AND archived = TRUE
ORDER BY updated_at DESC
LIMIT ? OFFSET ?;

-- name: UpdateConversationSlug :one
UPDATE conversations
SET slug = ?, updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?
RETURNING *;

-- name: UpdateConversationTimestamp :exec
UPDATE conversations
SET updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?;

-- name: DeleteConversation :exec
DELETE FROM conversations
WHERE conversation_id = ?;

-- name: CountConversations :one
SELECT COUNT(*) FROM conversations WHERE archived = FALSE;

-- name: CountArchivedConversations :one
SELECT COUNT(*) FROM conversations WHERE archived = TRUE;

-- name: ArchiveConversation :one
UPDATE conversations
SET archived = TRUE, updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?
RETURNING *;

-- name: UnarchiveConversation :one
UPDATE conversations
SET archived = FALSE, updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?
RETURNING *;

-- name: UpdateConversationCwd :one
UPDATE conversations
SET cwd = ?, updated_at = CURRENT_TIMESTAMP
WHERE conversation_id = ?
RETURNING *;
