package metricsd

import "exe.dev/metricsd/types"

// Re-export types for backward compatibility
type (
	Metric       = types.Metric
	MetricsBatch = types.MetricsBatch
)

// Schema is the DuckDB table creation SQL.
// Update this when modifying the Metric struct.
// Note: host column uses VARCHAR but is a good candidate for dictionary compression
// which DuckDB applies automatically for low-cardinality string columns.
const Schema = `
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
`

// InsertSQL is the prepared statement for inserting metrics.
const InsertSQL = `
INSERT INTO vm_metrics (
	timestamp, host, vm_name,
	disk_size_bytes, disk_used_bytes, disk_logical_used_bytes,
	memory_nominal_bytes, memory_rss_bytes, memory_swap_bytes,
	cpu_used_cumulative_seconds, cpu_nominal,
	network_tx_bytes, network_rx_bytes
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`

// SelectSQL is the query for retrieving metrics.
const SelectSQL = `
SELECT
	timestamp, host, vm_name,
	disk_size_bytes, disk_used_bytes, disk_logical_used_bytes,
	memory_nominal_bytes, memory_rss_bytes, memory_swap_bytes,
	cpu_used_cumulative_seconds, cpu_nominal,
	network_tx_bytes, network_rx_bytes
FROM vm_metrics
`

// SparklineSQL is the query template for the sparkline dashboard.
// Use fmt.Sprintf to fill in the hours value (validated as integer).
const SparklineSQL = `
SELECT
	timestamp, host, vm_name,
	disk_size_bytes, disk_used_bytes, disk_logical_used_bytes,
	memory_nominal_bytes, memory_rss_bytes, memory_swap_bytes,
	cpu_used_cumulative_seconds, cpu_nominal,
	network_tx_bytes, network_rx_bytes
FROM vm_metrics
WHERE timestamp > now() - INTERVAL '%d' HOUR
ORDER BY vm_name, timestamp ASC
`
