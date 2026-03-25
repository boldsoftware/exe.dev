package metricsd

import "exe.dev/metricsd/types"

// Re-export types for backward compatibility
type (
	Metric           = types.Metric
	MetricsBatch     = types.MetricsBatch
	QueryVMsRequest  = types.QueryVMsRequest
	QueryVMsResponse = types.QueryVMsResponse
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
// Uses vm_metrics_all view which unions the duckdb table with archived parquet files.
const SelectSQL = `
SELECT
	timestamp, host, vm_name,
	disk_size_bytes, disk_used_bytes, disk_logical_used_bytes,
	memory_nominal_bytes, memory_rss_bytes, memory_swap_bytes,
	cpu_used_cumulative_seconds, cpu_nominal,
	network_tx_bytes, network_rx_bytes,
	resource_group,
	io_read_bytes, io_write_bytes
FROM vm_metrics_all
`

// SparklineSQL is the query template for the sparkline dashboard.
// Use fmt.Sprintf to fill in the hours value (validated as integer).
// Uses vm_metrics_all view which unions the duckdb table with archived parquet files.
const SparklineSQL = `
SELECT
	timestamp, host, vm_name,
	disk_size_bytes, disk_used_bytes, disk_logical_used_bytes,
	memory_nominal_bytes, memory_rss_bytes, memory_swap_bytes,
	cpu_used_cumulative_seconds, cpu_nominal,
	network_tx_bytes, network_rx_bytes,
	resource_group,
	io_read_bytes, io_write_bytes
FROM vm_metrics_all
WHERE timestamp > now() - INTERVAL '%d' HOUR
ORDER BY vm_name, timestamp ASC
`
