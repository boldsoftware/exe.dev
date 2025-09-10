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
