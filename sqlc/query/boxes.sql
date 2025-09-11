-- name: GetBoxesForAlloc :many
SELECT id, alloc_id, name, status, image, container_id, created_by_user_id, created_at, updated_at, last_started_at, routes,
       ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key, ssh_host_certificate, ssh_client_private_key, ssh_port, ssh_user
FROM boxes
WHERE alloc_id = ?
ORDER BY name;

-- name: GetBoxByName :one
SELECT id, alloc_id, name, status, image, container_id, created_by_user_id, created_at, updated_at, last_started_at, routes,
       ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key, ssh_host_certificate, ssh_client_private_key, ssh_port, ssh_user
FROM boxes
WHERE name = ?;

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
    ssh_ca_public_key = ?,
    ssh_host_certificate = ?,
    ssh_client_private_key = ?,
    ssh_port = ?,
    ssh_user = ?
WHERE id = ?;

-- name: UpdateBoxContainerIDAndStatus :exec
UPDATE boxes SET container_id = ?, status = 'running' WHERE id = ?;

-- name: GetBoxesForUserDashboard :many
SELECT m.id, m.alloc_id, m.name, m.status, COALESCE(m.image, '') as image,
       COALESCE(m.container_id, '') as container_id, m.created_by_user_id,
       m.created_at, m.updated_at, m.last_started_at
FROM boxes m
JOIN allocs a ON m.alloc_id = a.alloc_id
WHERE a.user_id = ?
ORDER BY m.updated_at DESC;

-- name: GetBoxesByHost :many
SELECT
    b.id, b.alloc_id, b.name, b.status, b.image, b.container_id,
    b.created_by_user_id, b.created_at, b.updated_at, b.last_started_at,
    b.routes, b.ssh_server_identity_key, b.ssh_authorized_keys,
    b.ssh_ca_public_key, b.ssh_host_certificate, b.ssh_client_private_key,
    b.ssh_port, b.ssh_user
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
    ssh_server_identity_key = ?, ssh_authorized_keys = ?, ssh_ca_public_key = ?,
    ssh_host_certificate = ?, ssh_client_private_key = ?, ssh_port = ?
WHERE id = ?;

-- name: UpdateBoxStatus :exec
UPDATE boxes
SET status = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: GetBoxByNameAndAlloc :one
SELECT id, alloc_id, name, status, image, container_id,
       created_by_user_id, created_at, updated_at,
       last_started_at, routes
FROM boxes
WHERE name = ? AND alloc_id = ?;
