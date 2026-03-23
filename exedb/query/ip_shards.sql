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
SELECT * FROM ip_shards
ORDER BY shard;

-- name: ListAWSIPShards :many
SELECT * FROM aws_ip_shards
ORDER BY shard;

-- name: ListLatitudeIPShards :many
SELECT * FROM latitude_ip_shards
ORDER BY shard;

-- name: UpsertLatitudeIPShard :exec
INSERT INTO latitude_ip_shards (shard, public_ip, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT (shard) DO UPDATE SET
    public_ip = excluded.public_ip,
    updated_at = CURRENT_TIMESTAMP;

-- name: GetLatitudeShardPublicIP :one
SELECT public_ip
FROM latitude_ip_shards
WHERE shard = ?;

-- name: DeleteLatitudeIPShard :exec
DELETE FROM latitude_ip_shards WHERE shard = ?;

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
