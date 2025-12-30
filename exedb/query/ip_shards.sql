-- name: UpsertIPShard :exec
INSERT INTO ip_shards (shard, public_ip, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT (shard) DO UPDATE SET
    public_ip = excluded.public_ip,
    updated_at = CURRENT_TIMESTAMP;

-- name: GetShardPublicIP :one
SELECT public_ip
FROM ip_shards
WHERE shard = ?;

-- name: ListIPShards :many
SELECT shard, public_ip
FROM ip_shards
ORDER BY shard;

-- name: CountIPShards :one
SELECT COUNT(*) FROM ip_shards;
