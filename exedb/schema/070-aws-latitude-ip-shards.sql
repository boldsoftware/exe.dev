-- Regional IP shard tables for AWS and Latitude.
-- These tables enable execore to handle connections from both AWS and Latitude
-- sshpiper instances during the migration period.
--
-- The existing ip_shards table remains the source of truth for DNS serving.
-- aws_ip_shards and latitude_ip_shards are used by execore for IP-to-shard mapping.

-- AWS IP shards: shard to AWS public IP mapping.
-- Used by execore to map AWS private IPs (after NAT) back to shards.
CREATE TABLE aws_ip_shards (
    shard INTEGER PRIMARY KEY CHECK (shard BETWEEN 1 AND 25),
    public_ip TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Copy existing ip_shards to aws_ip_shards.
-- At migration time, ip_shards contains AWS IPs, so this initializes aws_ip_shards correctly.
INSERT INTO aws_ip_shards (shard, public_ip, created_at, updated_at)
SELECT shard, public_ip, created_at, updated_at FROM ip_shards;

-- Latitude IP shards: shard to Latitude public IP mapping.
-- Used by execore to map Latitude public IPs (no NAT) to shards.
-- Initially empty; will be populated manually or via debug UI before migration.
CREATE TABLE latitude_ip_shards (
    shard INTEGER PRIMARY KEY CHECK (shard BETWEEN 1 AND 25),
    public_ip TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
