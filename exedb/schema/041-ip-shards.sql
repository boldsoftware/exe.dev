-- IP shard to public IP address mapping.
-- This is the canonical source of truth for sNNN.exe.xyz A records.
-- The embedded nameserver uses this to answer A record queries.
--
-- Shards 1-25 map to public IPs. Each shard corresponds to an ENI
-- on the EC2 instance, discovered at boot via EC2 metadata.
CREATE TABLE ip_shards (
    shard INTEGER PRIMARY KEY CHECK (shard BETWEEN 1 AND 25),
    public_ip TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
