-- name: BoxWithNameExists :one
SELECT EXISTS ( SELECT 1 FROM boxes WHERE name = ? );

-- name: BoxWithOwnerNamed :one
SELECT * FROM boxes WHERE name = ? AND boxes.created_by_user_id = ?;

-- name: BoxNamed :one
-- This is not a secure API!
-- Whenever possible, use an alternative method that also checks the alloc/user and/or returns less data.
SELECT * FROM boxes WHERE name = ?;

-- name: InsertBox :execlastid
INSERT INTO boxes (
    ctrhost, name, status, image, container_id, created_by_user_id, routes, region, allocated_cpus
) VALUES (?, ?, ?, ?, NULL, ?, ?, ?, ?);

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

-- name: GetBoxesForUserDashboard :many
SELECT m.id, m.name, m.status, COALESCE(m.image, '') as image,
       COALESCE(m.container_id, '') as container_id, m.created_by_user_id,
       m.created_at, m.updated_at, m.last_started_at,
       COALESCE(m.creation_log, '') as creation_log, m.routes, m.region
FROM boxes m
WHERE m.created_by_user_id = ? AND m.status != 'failed'
ORDER BY m.updated_at DESC;

-- name: GetBoxesByHost :many
SELECT b.*
FROM boxes b
WHERE b.ctrhost = ? AND b.status != 'failed';

-- name: GetBoxSSHDetails :one
SELECT m.ssh_port, m.ssh_client_private_key, m.ssh_server_identity_key, m.ctrhost, m.ssh_user
FROM boxes m
WHERE m.id = ?;

-- name: UpdateBoxStatus :exec
UPDATE boxes
SET status = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: UpdateBoxMigration :exec
UPDATE boxes
SET ctrhost = ?, ssh_port = ?, status = ?, region = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: UpdateBoxSSHPort :exec
UPDATE boxes
SET ssh_port = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: GetBoxByNameAndAlloc :one
SELECT * FROM boxes WHERE name = ? AND created_by_user_id = ?;

-- name: GetBoxOwnerByContainerID :one
SELECT u.user_id, u.email, b.support_access_allowed
FROM boxes b
JOIN users u ON u.user_id = b.created_by_user_id
WHERE b.container_id = ?;

-- name: DeleteBox :exec
DELETE FROM boxes WHERE id = ?;

-- name: UpdateBoxRoutes :exec
UPDATE boxes SET routes = ? WHERE name = ? AND created_by_user_id = ?;

-- name: SetBoxSupportAccessAllowed :exec
UPDATE boxes SET support_access_allowed = ? WHERE id = ?;

-- name: GetBoxByNameWithSupportAccess :one
SELECT * FROM boxes WHERE name = ? AND support_access_allowed = 1;

-- name: CountBoxesForUser :one
SELECT COUNT(*) FROM boxes WHERE created_by_user_id = ?;

-- name: CountBoxes :one
SELECT COUNT(*) FROM boxes;

-- name: CountUsersWithBoxes :one
SELECT COUNT(DISTINCT created_by_user_id) FROM boxes;

-- name: UpdateBoxCreationLog :exec
UPDATE boxes SET creation_log = ? WHERE name = ?;

-- name: ListAllBoxesWithOwner :many
SELECT b.name, b.status, b.ctrhost, b.container_id, b.created_by_user_id as owner_user_id, u.email as owner_email, b.region, b.support_access_allowed
FROM boxes b
JOIN users u ON u.user_id = b.created_by_user_id
ORDER BY b.name;

-- name: UpdateBoxName :exec
UPDATE boxes SET name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND created_by_user_id = ?;

-- name: UpdateBoxNameByID :exec
UPDATE boxes SET name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: GetBoxByID :one
SELECT * FROM boxes WHERE id = ?;

-- name: UpdateBoxAllocatedCPUs :exec
UPDATE boxes SET allocated_cpus = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: GetBoxesWithNullAllocatedCPUs :many
SELECT * FROM boxes
WHERE allocated_cpus IS NULL AND container_id IS NOT NULL AND status != 'failed'
ORDER BY id
LIMIT ?;

-- name: SetBoxCgroupOverrides :exec
UPDATE boxes SET cgroup_overrides = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: GetBoxByContainerID :one
SELECT * FROM boxes WHERE container_id = ?;

-- name: CountBoxesByRegionAndStatus :many
SELECT region, status, COUNT(*) AS count FROM boxes GROUP BY region, status;

-- name: GetUsersWithOutOfRegionBoxes :many
SELECT u.user_id, u.email, u.region AS user_region, COUNT(*) AS box_count
FROM users u
JOIN boxes b ON b.created_by_user_id = u.user_id
WHERE u.region != b.region AND b.status IN ('running', 'starting')
GROUP BY u.user_id, u.email, u.region
ORDER BY u.region, u.email;
