package metricsd

import "exe.dev/metricsd/types"

// Re-export types for backward compatibility
type (
	Metric       = types.Metric
	MetricsBatch = types.MetricsBatch
)

// InsertSQL is the prepared statement for inserting metrics.
const InsertSQL = `
INSERT INTO vm_metrics (
	timestamp, host, vm_name,
	disk_size_bytes, disk_used_bytes, disk_logical_used_bytes,
	memory_nominal_bytes, memory_rss_bytes, memory_swap_bytes,
	cpu_used_cumulative_seconds, cpu_nominal,
	network_tx_bytes, network_rx_bytes,
	resource_group,
	io_read_bytes, io_write_bytes
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`

// SelectSQL is the query for retrieving metrics.
const SelectSQL = `
SELECT
	timestamp, host, vm_name,
	disk_size_bytes, disk_used_bytes, disk_logical_used_bytes,
	memory_nominal_bytes, memory_rss_bytes, memory_swap_bytes,
	cpu_used_cumulative_seconds, cpu_nominal,
	network_tx_bytes, network_rx_bytes,
	resource_group,
	io_read_bytes, io_write_bytes
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
	network_tx_bytes, network_rx_bytes,
	resource_group,
	io_read_bytes, io_write_bytes
FROM vm_metrics
WHERE timestamp > now() - INTERVAL '%d' HOUR
ORDER BY vm_name, timestamp ASC
`
