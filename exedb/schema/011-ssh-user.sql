-- Migration 011: Add ssh_user field to machines table
-- This field stores the USER from the Docker image to properly configure SSH access

ALTER TABLE machines ADD COLUMN ssh_user TEXT DEFAULT 'root';

-- Update index for new column if needed (not required for this field)

-- Insert migration entry
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (011, '011_ssh_user');