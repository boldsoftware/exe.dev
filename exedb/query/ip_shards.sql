-- name: ListNetActuateIPShards :many
SELECT * FROM netactuate_ip_shards
ORDER BY shard;

-- name: UpsertNetActuateIPShard :exec
INSERT INTO netactuate_ip_shards (shard, public_ip, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT (shard) DO UPDATE SET
    public_ip = excluded.public_ip,
    updated_at = CURRENT_TIMESTAMP;

-- name: DeleteNetActuateIPShard :exec
DELETE FROM netactuate_ip_shards WHERE shard = ?;
