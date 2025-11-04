-- name: BoxWithNameExists :one
SELECT EXISTS ( SELECT 1 FROM boxes WHERE name = ? );

-- name: BoxWithOwnerNamed :one
SELECT * FROM boxes WHERE name = ? AND boxes.created_by_user_id = ?;

-- name: SSHKeyForBoxNamed :one
SELECT ssh_server_identity_key FROM boxes WHERE name = ?;

-- name: BoxNamed :one
-- This is not a secure API!
-- Whenever possible, use an alternative method that also checks the alloc/user and/or returns less data.
SELECT * FROM boxes WHERE name = ?;

-- name: InsertBox :execlastid
INSERT INTO boxes (
    ctrhost, name, status, image, container_id, created_by_user_id, routes
) VALUES (?, ?, ?, ?, NULL, ?, ?);

-- name: BoxesForUser :many
SELECT *
FROM boxes
WHERE created_by_user_id = ?
ORDER BY updated_at DESC, id DESC;

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
SELECT m.id, m.name, m.status, COALESCE(m.image, '') as image,
       COALESCE(m.container_id, '') as container_id, m.created_by_user_id,
       m.created_at, m.updated_at, m.last_started_at,
       COALESCE(m.creation_log, '') as creation_log
FROM boxes m
WHERE m.created_by_user_id = ?
ORDER BY m.updated_at DESC;

-- name: GetBoxesByHost :many
SELECT b.*
FROM boxes b
WHERE b.ctrhost = ? AND b.status != 'failed';

-- name: GetBoxSSHDetails :one
SELECT m.ssh_port, m.ssh_client_private_key, m.ssh_server_identity_key, m.ctrhost, m.ssh_user
FROM boxes m
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
SELECT * FROM boxes WHERE name = ? AND created_by_user_id = ?;

-- name: GetBoxOwnerEmailByContainerID :one
SELECT u.email
FROM boxes b
JOIN users u ON u.user_id = b.created_by_user_id
WHERE b.container_id = ?;

-- name: DeleteBox :exec
DELETE FROM boxes WHERE id = ?;

-- name: UpdateBoxRoutes :exec
UPDATE boxes SET routes = ? WHERE name = ? AND created_by_user_id = ?;
