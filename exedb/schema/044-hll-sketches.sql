-- HyperLogLog sketches for unique user tracking
CREATE TABLE hll_sketches (
    key TEXT PRIMARY KEY,
    data BLOB NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
