-- name: CreateBoxShare :one
INSERT INTO box_shares (
    box_id,
    shared_with_user_id,
    shared_by_user_id,
    message
) VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetBoxSharesByBoxID :many
SELECT bs.*, u.email as shared_with_user_email
FROM box_shares bs
JOIN users u ON bs.shared_with_user_id = u.user_id
WHERE bs.box_id = ?
ORDER BY bs.created_at DESC;

-- name: GetBoxesSharedWithUser :many
SELECT 
    b.id,
    b.name,
    b.status,
    b.image,
    b.created_at,
    b.routes,
    bs.message,
    bs.created_at as shared_at,
    owner.email as owner_email
FROM boxes b
JOIN box_shares bs ON b.id = bs.box_id
JOIN users owner ON b.created_by_user_id = owner.user_id
WHERE bs.shared_with_user_id = ?
ORDER BY bs.created_at DESC;

-- name: DeleteBoxShareByBoxAndUser :exec
DELETE FROM box_shares
WHERE box_id = ? AND shared_with_user_id = ?;

-- name: HasUserAccessToBox :one
SELECT COUNT(*) > 0 as has_access
FROM box_shares
WHERE box_id = ? AND shared_with_user_id = ?;

-- name: CountBoxShares :one
SELECT COUNT(*) as share_count
FROM box_shares
WHERE box_id = ?;
