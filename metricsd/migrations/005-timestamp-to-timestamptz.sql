DROP INDEX IF EXISTS idx_vm_metrics_timestamp;
DROP INDEX IF EXISTS idx_vm_metrics_host;
DROP INDEX IF EXISTS idx_vm_metrics_vm_name;
ALTER TABLE vm_metrics ALTER COLUMN timestamp TYPE TIMESTAMPTZ;
CREATE INDEX idx_vm_metrics_timestamp ON vm_metrics(timestamp);
CREATE INDEX idx_vm_metrics_host ON vm_metrics(host);
CREATE INDEX idx_vm_metrics_vm_name ON vm_metrics(vm_name);
