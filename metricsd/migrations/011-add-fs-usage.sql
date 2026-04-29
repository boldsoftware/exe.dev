-- Add ext4 superblock-derived filesystem usage columns to vm_metrics.
-- These reflect the guest's view of disk usage (used / free / available
-- bytes from the ext4 primary superblock, read directly from the zvol
-- by the exelet) when the exelet is configured to collect it. Zero
-- when not collected (the default off-prod gate).
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS fs_total_bytes BIGINT DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS fs_free_bytes BIGINT DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS fs_available_bytes BIGINT DEFAULT 0;
