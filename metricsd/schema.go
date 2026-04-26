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
// vm_id is appended after the original columns because it was added via ALTER
// TABLE; the memory.stat breakdown columns were added later still.
const InsertSQL = `
INSERT INTO vm_metrics (
	timestamp, host, vm_name,
	disk_size_bytes, disk_used_bytes, disk_logical_used_bytes,
	memory_nominal_bytes, memory_rss_bytes, memory_swap_bytes,
	cpu_used_cumulative_seconds, cpu_nominal,
	network_tx_bytes, network_rx_bytes,
	resource_group,
	io_read_bytes, io_write_bytes,
	vm_id,
	memory_anon_bytes, memory_file_bytes, memory_kernel_bytes,
	memory_shmem_bytes, memory_slab_bytes, memory_inactive_file_bytes
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`

// SelectSQL is the query for retrieving metrics.
// Uses vm_metrics_all view which unions the duckdb table with archived
// parquet files. The memory.stat breakdown columns are wrapped in COALESCE
// because archived parquet files written before migration 010 don't have
// those columns and would surface as NULL via UNION ALL BY NAME.
const SelectSQL = `
SELECT
	timestamp, host, vm_name,
	disk_size_bytes, disk_used_bytes, disk_logical_used_bytes,
	memory_nominal_bytes, memory_rss_bytes, memory_swap_bytes,
	cpu_used_cumulative_seconds, cpu_nominal,
	network_tx_bytes, network_rx_bytes,
	resource_group,
	io_read_bytes, io_write_bytes,
	vm_id,
	COALESCE(memory_anon_bytes, 0) AS memory_anon_bytes,
	COALESCE(memory_file_bytes, 0) AS memory_file_bytes,
	COALESCE(memory_kernel_bytes, 0) AS memory_kernel_bytes,
	COALESCE(memory_shmem_bytes, 0) AS memory_shmem_bytes,
	COALESCE(memory_slab_bytes, 0) AS memory_slab_bytes,
	COALESCE(memory_inactive_file_bytes, 0) AS memory_inactive_file_bytes
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
	io_read_bytes, io_write_bytes,
	vm_id,
	COALESCE(memory_anon_bytes, 0) AS memory_anon_bytes,
	COALESCE(memory_file_bytes, 0) AS memory_file_bytes,
	COALESCE(memory_kernel_bytes, 0) AS memory_kernel_bytes,
	COALESCE(memory_shmem_bytes, 0) AS memory_shmem_bytes,
	COALESCE(memory_slab_bytes, 0) AS memory_slab_bytes,
	COALESCE(memory_inactive_file_bytes, 0) AS memory_inactive_file_bytes
FROM vm_metrics_all
WHERE timestamp > now() - INTERVAL '%d' HOUR
ORDER BY vm_name, timestamp ASC
`
