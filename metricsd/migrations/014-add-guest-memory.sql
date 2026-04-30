-- Guest memory observability (memwatch v0). Populated from in-guest memd
-- scrapes and forwarded by exelet. Zero when no fresh sample is available.
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS guest_mem_total_bytes     BIGINT DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS guest_mem_available_bytes BIGINT DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS guest_cached_bytes        BIGINT DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS guest_reclaimable_bytes   BIGINT DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS guest_dirty_bytes         BIGINT DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS guest_psi_some_avg60      DOUBLE DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS guest_psi_full_avg60      DOUBLE DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS guest_refault_rate        DOUBLE DEFAULT 0;
