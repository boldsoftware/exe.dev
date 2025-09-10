-- name: AllocExistsForUser :one
SELECT EXISTS(SELECT 1 FROM allocs WHERE user_id = ?);

-- name: InsertAlloc :exec
INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost)
VALUES (?, ?, ?, ?, ?);
