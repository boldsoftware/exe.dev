-- Migration 014: Clean up container host references
-- This migration:
-- 1. Removes docker_host from machines table (should be determined from alloc)
-- 2. Renames docker_host to ctrhost in allocs table for clarity
-- 3. Makes ctrhost NOT NULL to ensure every alloc is assigned to a host

-- First, update any NULL docker_host values in allocs to a default
-- (This handles any existing allocs that don't have a host assigned)
UPDATE allocs SET docker_host = 'local' WHERE docker_host IS NULL OR docker_host = '';

-- Create new ctrhost column with NOT NULL constraint
ALTER TABLE allocs ADD COLUMN ctrhost TEXT NOT NULL DEFAULT '';

-- Copy data from docker_host to ctrhost
UPDATE allocs SET ctrhost = docker_host;

-- Remove the default after copying data
-- SQLite doesn't support ALTER COLUMN, so we need to recreate the table
CREATE TABLE allocs_new (
    alloc_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    alloc_type TEXT NOT NULL DEFAULT 'medium',
    region TEXT NOT NULL DEFAULT 'aws-us-west-2',
    ctrhost TEXT NOT NULL,  -- Container host where this alloc's resources are
    ip_range TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    stripe_customer_id TEXT,
    billing_email TEXT,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

-- Copy data to new table
INSERT INTO allocs_new (alloc_id, user_id, alloc_type, region, ctrhost, ip_range, created_at, stripe_customer_id, billing_email)
SELECT alloc_id, user_id, alloc_type, region, ctrhost, ip_range, created_at, stripe_customer_id, billing_email
FROM allocs;

-- Drop old table and rename new one
DROP TABLE allocs;
ALTER TABLE allocs_new RENAME TO allocs;

-- Recreate indexes
CREATE INDEX IF NOT EXISTS idx_allocs_user ON allocs(user_id);
CREATE INDEX IF NOT EXISTS idx_allocs_region ON allocs(region);
CREATE INDEX IF NOT EXISTS idx_allocs_ctrhost ON allocs(ctrhost);
CREATE UNIQUE INDEX IF NOT EXISTS idx_allocs_ip_range ON allocs(ctrhost, ip_range) WHERE ip_range IS NOT NULL;

-- Now handle the boxes table (renamed from machines) - remove docker_host column and ensure ssh_user exists
CREATE TABLE boxes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    alloc_id TEXT NOT NULL,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    image TEXT NOT NULL,
    container_id TEXT,
    created_by_user_id TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_started_at DATETIME,
    routes TEXT,
    ssh_server_identity_key BLOB,
    ssh_authorized_keys TEXT,
    ssh_ca_public_key TEXT,
    ssh_host_certificate TEXT,
    ssh_client_private_key BLOB,
    ssh_port INTEGER,
    ssh_user TEXT,
    UNIQUE(name), -- machine names are globally unique
    FOREIGN KEY (alloc_id) REFERENCES allocs(alloc_id) ON DELETE CASCADE,
    FOREIGN KEY (created_by_user_id) REFERENCES users(user_id)
);

-- Copy data from machines to boxes table (excluding docker_host, including ssh_user if it exists)
INSERT INTO boxes (id, alloc_id, name, status, image, container_id, created_by_user_id, 
    created_at, updated_at, last_started_at, routes,
    ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key, ssh_host_certificate, 
    ssh_client_private_key, ssh_port, ssh_user)
SELECT id, alloc_id, name, status, image, container_id, created_by_user_id,
    created_at, updated_at, last_started_at, routes,
    ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key, ssh_host_certificate,
    ssh_client_private_key, ssh_port, ssh_user
FROM machines;

-- Drop old machines table
DROP TABLE machines;

-- Recreate indexes for boxes
CREATE INDEX IF NOT EXISTS idx_boxes_alloc ON boxes(alloc_id);
CREATE INDEX IF NOT EXISTS idx_boxes_name ON boxes(name);
CREATE INDEX IF NOT EXISTS idx_boxes_status ON boxes(status);

-- Insert migration entry
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (014, '014_ctrhost_cleanup');