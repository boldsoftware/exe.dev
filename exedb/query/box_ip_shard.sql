-- name: ListIPShardsForUser :many
SELECT ip_shard
FROM box_ip_shard
WHERE user_id = ?
ORDER BY ip_shard ASC;

-- name: GetBoxIPShard :one
SELECT ip_shard
FROM box_ip_shard
WHERE box_id = ?;

-- name: InsertBoxIPShard :exec
INSERT INTO box_ip_shard (box_id, user_id, ip_shard)
VALUES (?, ?, ?);

-- name: DeleteBoxIPShard :exec
DELETE FROM box_ip_shard WHERE box_id = ?;

-- name: GetIPShardByBoxName :one
SELECT s.ip_shard
FROM box_ip_shard s
JOIN boxes b ON b.id = s.box_id
WHERE b.name = ?;

-- name: GetBoxByUserAndShard :one
SELECT b.*
FROM box_ip_shard s
JOIN boxes b ON b.id = s.box_id
WHERE s.user_id = ? AND s.ip_shard = ?;
