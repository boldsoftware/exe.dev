CREATE TABLE IF NOT EXISTS migrations (
	migration_number INTEGER PRIMARY KEY,
	migration_name   VARCHAR NOT NULL,
	applied_at       TIMESTAMP DEFAULT current_timestamp
);

CREATE TABLE IF NOT EXISTS vm_metrics (
	timestamp                   TIMESTAMP NOT NULL,
	host                        VARCHAR NOT NULL,
	vm_name                     VARCHAR NOT NULL,
	disk_size_bytes             BIGINT NOT NULL,
	disk_used_bytes             BIGINT NOT NULL,
	disk_logical_used_bytes     BIGINT NOT NULL,
	memory_nominal_bytes        BIGINT NOT NULL,
	memory_rss_bytes            BIGINT NOT NULL,
	memory_swap_bytes           BIGINT NOT NULL,
	cpu_used_cumulative_seconds DOUBLE NOT NULL,
	cpu_nominal                 DOUBLE NOT NULL,
	network_tx_bytes            BIGINT NOT NULL,
	network_rx_bytes            BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vm_metrics_timestamp ON vm_metrics(timestamp);
CREATE INDEX IF NOT EXISTS idx_vm_metrics_host ON vm_metrics(host);
CREATE INDEX IF NOT EXISTS idx_vm_metrics_vm_name ON vm_metrics(vm_name);
