// Package types provides the data types for metrics collection.
// This package is separate from metricsd to avoid importing DuckDB dependencies.
package types

import "time"

// Metric represents a single VM metrics data point.
type Metric struct {
	Timestamp             time.Time `json:"timestamp"`
	Host                  string    `json:"host"` // Exelet host name (container host where VM runs)
	VMName                string    `json:"vm_name"`
	ResourceGroup         string    `json:"resource_group"`          // Resource group for per-account cgroup grouping
	DiskSizeBytes         int64     `json:"disk_size_bytes"`         // ZFS volsize (nominal/provisioned size)
	DiskUsedBytes         int64     `json:"disk_used_bytes"`         // ZFS used (actual compressed bytes on disk)
	DiskLogicalUsedBytes  int64     `json:"disk_logical_used_bytes"` // ZFS logicalused (uncompressed logical usage)
	MemoryNominalBytes    int64     `json:"memory_nominal_bytes"`
	MemoryRSSBytes        int64     `json:"memory_rss_bytes"`
	MemorySwapBytes       int64     `json:"memory_swap_bytes"`
	CPUUsedCumulativeSecs float64   `json:"cpu_used_cumulative_seconds"`
	CPUNominal            float64   `json:"cpu_nominal"`
	NetworkTXBytes        int64     `json:"network_tx_bytes"`
	NetworkRXBytes        int64     `json:"network_rx_bytes"`
	IOReadBytes           int64     `json:"io_read_bytes"`
	IOWriteBytes          int64     `json:"io_write_bytes"`
	VMID                  string    `json:"vm_id,omitempty"`
}

// MetricsBatch allows submitting multiple metrics at once.
type MetricsBatch struct {
	Metrics []Metric `json:"metrics"`
}

// QueryVMsRequest is the request body for POST /query/vms
type QueryVMsRequest struct {
	VMNames []string `json:"vm_names"`
	Hours   int      `json:"hours"`
}

// QueryVMsResponse is the response for POST /query/vms
type QueryVMsResponse struct {
	VMs map[string][]Metric `json:"vms"`
}

// QueryVMsPoolRequest is the request body for POST /query/vms/pool.
type QueryVMsPoolRequest struct {
	VMNames []string `json:"vm_names"`
	Hours   int      `json:"hours"`
}

// PoolMetric holds avg and sum for a single metric at a point in time.
type PoolMetric struct {
	Avg float64 `json:"avg"`
	Sum float64 `json:"sum"`
}

// PoolPoint is a single time-series data point for pool history.
type PoolPoint struct {
	Timestamp string     `json:"timestamp"`
	CPUCores  PoolMetric `json:"cpu_cores"`
	MemBytes  PoolMetric `json:"mem_bytes"`
}

// QueryVMsPoolResponse is the response for POST /query/vms/pool.
type QueryVMsPoolResponse struct {
	Points []PoolPoint `json:"points"`
}
