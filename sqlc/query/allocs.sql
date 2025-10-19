-- name: AllocExistsForUser :one
SELECT EXISTS(SELECT 1 FROM allocs WHERE user_id = ?);

-- name: InsertAlloc :exec
INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost)
VALUES (?, ?, ?, ?, ?);

-- name: GetAllocsByHost :many
SELECT *
FROM allocs
WHERE ctrhost = ?;

-- name: GetAllocByUserID :one
SELECT *
FROM allocs
WHERE user_id = ?
LIMIT 1;

-- name: GetCtrhostByAllocID :one
SELECT ctrhost FROM allocs WHERE alloc_id = ?;
