-- Add fs_used_bytes column: ext4 used bytes from the zvol superblock
-- (block_size * (blocks_count - free_blocks_count)). Mirrors the other
-- fs_*_bytes columns added in migration 011. Zero when not collected.
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS fs_used_bytes BIGINT DEFAULT 0;
