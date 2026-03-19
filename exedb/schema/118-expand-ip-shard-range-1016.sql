-- Expand IP shard CHECK constraints from BETWEEN 1 AND 253 to BETWEEN 1 AND 1016
-- to support the full /22 anycast block (4 x 254 = 1016 usable IPs).

-- Recreate box_ip_shard with wider constraint
CREATE TABLE box_ip_shard_new (
    box_id INTEGER NOT NULL,
    user_id TEXT NOT NULL,
    ip_shard INTEGER NOT NULL,
    PRIMARY KEY (box_id, user_id),
    UNIQUE(ip_shard, box_id, user_id),
    CHECK (ip_shard BETWEEN 1 AND 1016),
    FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
INSERT INTO box_ip_shard_new SELECT * FROM box_ip_shard;
DROP TABLE box_ip_shard;
ALTER TABLE box_ip_shard_new RENAME TO box_ip_shard;
CREATE INDEX idx_box_ip_shard_user ON box_ip_shard(user_id);
CREATE INDEX idx_box_ip_shard_shard ON box_ip_shard(ip_shard);

-- Recreate ip_shards with wider constraint
CREATE TABLE ip_shards_new (
    shard INTEGER PRIMARY KEY CHECK (shard BETWEEN 1 AND 1016),
    public_ip TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO ip_shards_new SELECT * FROM ip_shards;
DROP TABLE ip_shards;
ALTER TABLE ip_shards_new RENAME TO ip_shards;

-- Recreate aws_ip_shards with wider constraint
CREATE TABLE aws_ip_shards_new (
    shard INTEGER PRIMARY KEY CHECK (shard BETWEEN 1 AND 1016),
    public_ip TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO aws_ip_shards_new SELECT * FROM aws_ip_shards;
DROP TABLE aws_ip_shards;
ALTER TABLE aws_ip_shards_new RENAME TO aws_ip_shards;

-- Recreate latitude_ip_shards with wider constraint
CREATE TABLE latitude_ip_shards_new (
    shard INTEGER PRIMARY KEY CHECK (shard BETWEEN 1 AND 1016),
    public_ip TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO latitude_ip_shards_new SELECT * FROM latitude_ip_shards;
DROP TABLE latitude_ip_shards;
ALTER TABLE latitude_ip_shards_new RENAME TO latitude_ip_shards;

-- Recreate netactuate_ip_shards with wider constraint
CREATE TABLE netactuate_ip_shards_new (
    shard INTEGER PRIMARY KEY CHECK (shard BETWEEN 1 AND 1016),
    public_ip TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO netactuate_ip_shards_new SELECT * FROM netactuate_ip_shards;
DROP TABLE netactuate_ip_shards;
ALTER TABLE netactuate_ip_shards_new RENAME TO netactuate_ip_shards;
