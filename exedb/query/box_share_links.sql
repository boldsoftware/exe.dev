-- name: CreateBoxShareLink :one
INSERT INTO box_share_links (
    box_id,
    share_token,
    created_by_user_id
) VALUES (?, ?, ?)
RETURNING *;

-- name: GetBoxShareLinksByBoxID :many
SELECT * FROM box_share_links
WHERE box_id = ? AND created_by_user_id = ?
ORDER BY created_at DESC;

-- name: GetBoxShareLinkByTokenAndBoxID :one
SELECT * FROM box_share_links
WHERE share_token = ? AND box_id = ?;

-- name: DeleteBoxShareLinkByBoxAndToken :exec
DELETE FROM box_share_links
WHERE box_id = ? AND share_token = ?;

-- name: IncrementShareLinkUsage :exec
UPDATE box_share_links
SET 
    use_count = use_count + 1,
    last_used_at = CURRENT_TIMESTAMP
WHERE share_token = ?;

-- name: CountBoxShareLinks :one
SELECT COUNT(*) as link_count
FROM box_share_links
WHERE box_id = ?;

-- name: GetAllBoxShareLinksByBoxID :many
SELECT bsl.*, u.email as created_by_email FROM box_share_links bsl
JOIN users u ON bsl.created_by_user_id = u.user_id
WHERE box_id = ?
ORDER BY created_at DESC;

-- name: CountBoxShareLinksByUser :many
SELECT bsl.box_id, COUNT(*) as link_count
FROM box_share_links bsl
JOIN boxes b ON bsl.box_id = b.id
WHERE b.created_by_user_id = ? AND b.status != 'failed'
GROUP BY bsl.box_id;

-- name: GetBoxShareLinksByUser :many
SELECT bsl.box_id, bsl.share_token, b.name as box_name
FROM box_share_links bsl
JOIN boxes b ON bsl.box_id = b.id
WHERE b.created_by_user_id = sqlc.arg(user_id)
AND bsl.created_by_user_id = sqlc.arg(user_id)
AND b.status != 'failed'
ORDER BY bsl.box_id, bsl.created_at DESC;
