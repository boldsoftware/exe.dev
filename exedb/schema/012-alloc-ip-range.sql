-- Migration 012: Add IP range tracking to allocs
-- This migration adds an ip_range column to track the assigned IP range for each alloc

-- Add ip_range column to allocs table
ALTER TABLE allocs ADD COLUMN ip_range TEXT;

-- Create index for efficient IP range lookups and uniqueness per docker_host
CREATE UNIQUE INDEX IF NOT EXISTS idx_allocs_ip_range ON allocs(docker_host, ip_range) WHERE ip_range IS NOT NULL;

-- Insert migration entry
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (012, '012_alloc_ip_range');