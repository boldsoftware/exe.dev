-- name: CreateBoxShareLink :one
INSERT INTO box_share_links (
    box_id,
    share_token,
    created_by_user_id
) VALUES (?, ?, ?)
RETURNING *;

-- name: GetBoxShareLinksByBoxID :many
SELECT * FROM box_share_links
WHERE box_id = ?
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
