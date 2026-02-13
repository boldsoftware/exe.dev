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
}

// MetricsBatch allows submitting multiple metrics at once.
type MetricsBatch struct {
	Metrics []Metric `json:"metrics"`
}
