-- Migration 016: Remove IP range from allocs
-- IP ranges are no longer needed since all containers use the default bridge network
-- with port isolation applied at container creation time

-- SQLite doesn't support DROP COLUMN, so we need to recreate the table
CREATE TABLE allocs_new (
    alloc_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    alloc_type TEXT NOT NULL DEFAULT 'medium',
    region TEXT NOT NULL DEFAULT 'aws-us-west-2',
    ctrhost TEXT NOT NULL,  -- Container host where this alloc's resources are
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    stripe_customer_id TEXT,
    billing_email TEXT,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

-- Copy data to new table (excluding ip_range)
INSERT INTO allocs_new (alloc_id, user_id, alloc_type, region, ctrhost, created_at, stripe_customer_id, billing_email)
SELECT alloc_id, user_id, alloc_type, region, ctrhost, created_at, stripe_customer_id, billing_email
FROM allocs;

-- Drop old table and rename new one
DROP TABLE allocs;
ALTER TABLE allocs_new RENAME TO allocs;

-- Recreate indexes (excluding the ip_range index)
CREATE INDEX IF NOT EXISTS idx_allocs_user ON allocs(user_id);
CREATE INDEX IF NOT EXISTS idx_allocs_region ON allocs(region);
CREATE INDEX IF NOT EXISTS idx_allocs_ctrhost ON allocs(ctrhost);

-- Insert migration entry
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (016, '016_remove_ip_range');