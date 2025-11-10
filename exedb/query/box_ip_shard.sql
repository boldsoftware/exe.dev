-- name: ListIPShardsForUser :many
SELECT ip_shard
FROM box_ip_shard
WHERE user_id = ?
ORDER BY ip_shard ASC;

-- name: InsertBoxIPShard :exec
INSERT INTO box_ip_shard (box_id, user_id, ip_shard)
VALUES (?, ?, ?);

-- name: DeleteBoxIPShard :exec
DELETE FROM box_ip_shard WHERE box_id = ?;
