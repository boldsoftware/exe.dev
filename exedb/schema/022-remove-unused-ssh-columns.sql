-- Migration 022: Remove unused SSH columns
-- These columns are no longer used after simplifying SSH key management

-- SQLite doesn't support DROP COLUMN, so we need to recreate the table
CREATE TABLE boxes_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    alloc_id TEXT NOT NULL,
    name TEXT NOT NULL,
    status TEXT NOT NULL,
    image TEXT NOT NULL,
    container_id TEXT,
    created_by_user_id TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_started_at DATETIME,
    routes TEXT,
    ssh_server_identity_key BLOB,
    ssh_authorized_keys TEXT,
    -- ssh_ca_public_key TEXT removed
    -- ssh_host_certificate TEXT removed  
    ssh_client_private_key BLOB,
    ssh_port INTEGER,
    ssh_user TEXT,
    UNIQUE(name),
    FOREIGN KEY (alloc_id) REFERENCES allocs(alloc_id) ON DELETE CASCADE,
    FOREIGN KEY (created_by_user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

-- Copy data to new table (excluding ssh_ca_public_key and ssh_host_certificate)
INSERT INTO boxes_new (
    id, alloc_id, name, status, image, container_id, created_by_user_id,
    created_at, updated_at, last_started_at, routes,
    ssh_server_identity_key, ssh_authorized_keys, ssh_client_private_key, 
    ssh_port, ssh_user
)
SELECT 
    id, alloc_id, name, status, image, container_id, created_by_user_id,
    created_at, updated_at, last_started_at, routes,
    ssh_server_identity_key, ssh_authorized_keys, ssh_client_private_key, 
    ssh_port, ssh_user
FROM boxes;

-- Drop old table and rename new one
DROP TABLE boxes;
ALTER TABLE boxes_new RENAME TO boxes;

-- Recreate indexes
CREATE INDEX IF NOT EXISTS idx_boxes_alloc_id ON boxes(alloc_id);
CREATE INDEX IF NOT EXISTS idx_boxes_status ON boxes(status);

-- Insert migration entry
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (022, '022-remove-unused-ssh-columns');
