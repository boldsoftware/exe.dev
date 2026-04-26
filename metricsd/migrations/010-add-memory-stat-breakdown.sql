-- Add detailed cgroup memory.stat breakdown columns to vm_metrics.
-- The existing memory_rss_bytes column is misnamed: it stores the cgroup's
-- total memory.current (anon + file cache + kernel + slab) rather than RSS.
-- These new columns let consumers distinguish the VM's anonymous working set
-- from reclaimable host page cache.
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS memory_anon_bytes BIGINT DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS memory_file_bytes BIGINT DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS memory_kernel_bytes BIGINT DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS memory_shmem_bytes BIGINT DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS memory_slab_bytes BIGINT DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS memory_inactive_file_bytes BIGINT DEFAULT 0;
