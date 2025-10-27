-- Box IP shard assignments for per-user sNNN targets
CREATE TABLE box_ip_shard (
    box_id INTEGER NOT NULL,
    user_id TEXT NOT NULL,
    ip_shard INTEGER NOT NULL,
    PRIMARY KEY (box_id, user_id),
    UNIQUE(ip_shard, box_id, user_id),
    CHECK (ip_shard BETWEEN 1 AND 25),
    FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

CREATE INDEX idx_box_ip_shard_user ON box_ip_shard(user_id);
CREATE INDEX idx_box_ip_shard_shard ON box_ip_shard(ip_shard);
