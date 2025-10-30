-- name: CreatePendingBoxShare :one
INSERT INTO pending_box_shares (
    box_id,
    shared_with_email,
    shared_by_user_id,
    message
) VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetPendingBoxSharesByBoxID :many
SELECT * FROM pending_box_shares
WHERE box_id = ?
ORDER BY created_at DESC;

-- name: GetPendingBoxSharesByEmail :many
SELECT pbs.*, b.name as box_name, u.email as owner_email
FROM pending_box_shares pbs
JOIN boxes b ON pbs.box_id = b.id
JOIN users u ON pbs.shared_by_user_id = u.user_id
WHERE pbs.shared_with_email = ?
ORDER BY pbs.created_at DESC;

-- name: DeletePendingBoxShare :exec
DELETE FROM pending_box_shares
WHERE id = ?;

-- name: DeletePendingBoxShareByBoxAndEmail :exec
DELETE FROM pending_box_shares
WHERE box_id = ? AND shared_with_email = ?;

-- name: CountPendingBoxShares :one
SELECT COUNT(*) as share_count
FROM pending_box_shares
WHERE box_id = ?;
