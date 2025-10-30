-- name: BoxWithNameExists :one
SELECT EXISTS ( SELECT 1 FROM boxes WHERE name = ? );

-- name: BoxWithOwnerNamed :one
SELECT * FROM boxes WHERE name = ? AND boxes.alloc_id = (SELECT allocs.alloc_id FROM allocs WHERE allocs.user_id = ?);

-- name: SSHKeyForBoxNamed :one
SELECT ssh_server_identity_key FROM boxes WHERE name = ?;

-- name: BoxNamed :one
-- This is not a secure API!
-- Whenever possible, use an alternative method that also checks the alloc/user and/or returns less data.
SELECT * FROM boxes WHERE name = ?;

-- name: InsertBox :execlastid
INSERT INTO boxes (
    alloc_id, name, status, image, container_id, created_by_user_id, routes
) VALUES (?, ?, ?, ?, NULL, ?, ?);

-- name: UpdateBoxContainerAndStatus :exec
UPDATE boxes SET 
    container_id = ?,
    status = ?,
    ssh_server_identity_key = ?,
    ssh_authorized_keys = ?,
    ssh_client_private_key = ?,
    ssh_port = ?,
    ssh_user = ?
WHERE id = ?;

-- name: UpdateBoxContainerIDAndStatus :exec
UPDATE boxes SET container_id = ?, status = 'running' WHERE id = ?;

-- name: GetBoxesForUserDashboard :many
SELECT m.id, m.alloc_id, m.name, m.status, COALESCE(m.image, '') as image,
       COALESCE(m.container_id, '') as container_id, m.created_by_user_id,
       m.created_at, m.updated_at, m.last_started_at,
       COALESCE(m.creation_log, '') as creation_log
FROM boxes m
JOIN allocs a ON m.alloc_id = a.alloc_id
WHERE a.user_id = ?
ORDER BY m.updated_at DESC;

-- name: GetBoxesByHost :many
SELECT b.*
FROM boxes b
INNER JOIN allocs a ON b.alloc_id = a.alloc_id
WHERE a.ctrhost = ? AND b.status != 'failed';

-- name: GetBoxSSHDetails :one
SELECT m.ssh_port, m.ssh_client_private_key, m.ssh_server_identity_key, a.ctrhost, m.ssh_user
FROM boxes m
JOIN allocs a ON m.alloc_id = a.alloc_id
WHERE m.id = ?;

-- name: GetBoxDetailsForSetup :one
SELECT container_id, created_by_user_id, name, image
FROM boxes WHERE id = ?;

-- name: UpdateBoxSSHDetails :exec
UPDATE boxes SET
    ssh_server_identity_key = ?, ssh_authorized_keys = ?,
    ssh_client_private_key = ?, ssh_port = ?
WHERE id = ?;

-- name: UpdateBoxStatus :exec
UPDATE boxes
SET status = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: GetBoxByNameAndAlloc :one
SELECT * FROM boxes WHERE name = ? AND alloc_id = ?;

-- name: DeleteBox :exec
DELETE FROM boxes WHERE id = ?;

-- name: UpdateBoxStatusRunning :exec
UPDATE boxes SET status = 'running', last_started_at = CURRENT_TIMESTAMP
WHERE name = ?;

-- name: UpdateBoxStatusStopped :exec
UPDATE boxes SET status = 'stopped'
WHERE name = ?;

-- name: GetBoxIDAndAllocByName :one
SELECT id, alloc_id FROM boxes WHERE name = ?;

-- name: UpdateBoxRoutes :exec
UPDATE boxes SET routes = ? WHERE name = ? AND alloc_id = ?;

-- name: UpdateBoxStatusRunningByID :exec
UPDATE boxes SET status = 'running', updated_at = ? WHERE id = ?;
