-- name: AllocExistsForUser :one
SELECT EXISTS(SELECT 1 FROM allocs WHERE user_id = ?);

-- name: InsertAlloc :exec
INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost, billing_email)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetAllocsByHost :many
SELECT alloc_id, user_id, alloc_type, region, ctrhost, created_at, stripe_customer_id, billing_email
FROM allocs
WHERE ctrhost = ?;
