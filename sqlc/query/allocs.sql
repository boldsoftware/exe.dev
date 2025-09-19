-- name: AllocExistsForUser :one
SELECT EXISTS(SELECT 1 FROM allocs WHERE user_id = ?);

-- name: InsertAlloc :exec
INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost, billing_account_id)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetAllocsByHost :many
SELECT alloc_id, user_id, alloc_type, region, ctrhost, created_at, billing_account_id
FROM allocs
WHERE ctrhost = ?;

-- name: GetAllocByUserID :one
SELECT alloc_id, user_id, alloc_type, region, ctrhost, created_at, billing_account_id
FROM allocs
WHERE user_id = ?
LIMIT 1;

-- name: GetCtrhostByAllocID :one
SELECT ctrhost FROM allocs WHERE alloc_id = ?;

-- name: GetAllocBillingInfo :one
SELECT billing_account_id
FROM allocs WHERE alloc_id = ?;

-- name: UpdateAllocBillingAccount :exec
UPDATE allocs SET billing_account_id = ? WHERE alloc_id = ?;
