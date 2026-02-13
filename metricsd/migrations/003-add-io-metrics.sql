ALTER TABLE vm_metrics ADD COLUMN io_read_bytes BIGINT DEFAULT 0;
ALTER TABLE vm_metrics ADD COLUMN io_write_bytes BIGINT DEFAULT 0;
