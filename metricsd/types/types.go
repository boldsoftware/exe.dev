// Package types provides the data types for metrics collection.
// This package is separate from metricsd to avoid importing DuckDB dependencies.
package types

import "time"

// Metric represents a single VM metrics data point.
type Metric struct {
	Timestamp            time.Time `json:"timestamp"`
	Host                 string    `json:"host"` // Exelet host name (container host where VM runs)
	VMName               string    `json:"vm_name"`
	ResourceGroup        string    `json:"resource_group"`          // Resource group for per-account cgroup grouping
	DiskSizeBytes        int64     `json:"disk_size_bytes"`         // ZFS volsize (nominal/provisioned size)
	DiskUsedBytes        int64     `json:"disk_used_bytes"`         // ZFS used (actual compressed bytes on disk)
	DiskLogicalUsedBytes int64     `json:"disk_logical_used_bytes"` // ZFS logicalused (uncompressed logical usage)
	MemoryNominalBytes   int64     `json:"memory_nominal_bytes"`
	// MemoryRSSBytes is a misnomer kept for backwards compatibility: it stores
	// the cgroup's total memory.current, not RSS. memory.current covers the
	// sum of charges to the cgroup (anon + file + kernel — where kernel
	// already includes slab — plus any other categories), but for a
	// hugepage-backed VM the guest RAM is *not* counted here, while host page
	// cache from the VM's disk I/O is. Prefer MemoryAnonBytes for the VM's
	// non-reclaimable footprint.
	MemoryRSSBytes  int64 `json:"memory_rss_bytes"`
	MemorySwapBytes int64 `json:"memory_swap_bytes"`

	// Detailed cgroup memory.stat breakdown (added later).
	// Older exelets that pre-date this addition will leave these at zero.
	MemoryAnonBytes         int64   `json:"memory_anon_bytes"`
	MemoryFileBytes         int64   `json:"memory_file_bytes"`
	MemoryKernelBytes       int64   `json:"memory_kernel_bytes"`
	MemoryShmemBytes        int64   `json:"memory_shmem_bytes"`
	MemorySlabBytes         int64   `json:"memory_slab_bytes"`
	MemoryInactiveFileBytes int64   `json:"memory_inactive_file_bytes"`
	CPUUsedCumulativeSecs   float64 `json:"cpu_used_cumulative_seconds"`
	CPUNominal              float64 `json:"cpu_nominal"`
	NetworkTXBytes          int64   `json:"network_tx_bytes"`
	NetworkRXBytes          int64   `json:"network_rx_bytes"`
	IOReadBytes             int64   `json:"io_read_bytes"`
	IOWriteBytes            int64   `json:"io_write_bytes"`
	VMID                    string  `json:"vm_id,omitempty"`

	// Filesystem-level (ext4) view of the zvol, when the exelet was
	// configured to collect it for this VM. Zero when not collected.
	// FsTotalBytes is raw block_count*block_size; FsFreeBytes/
	// FsAvailableBytes match statvfs f_bfree/f_bavail. See
	// exelet/storage/ext4 for the read-only superblock probe.
	FsTotalBytes     int64 `json:"fs_total_bytes,omitempty"`
	FsFreeBytes      int64 `json:"fs_free_bytes,omitempty"`
	FsAvailableBytes int64 `json:"fs_available_bytes,omitempty"`
	FsUsedBytes      int64 `json:"fs_used_bytes,omitempty"`
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

// VMPoolPoint is a per-VM data point within pool history.
type VMPoolPoint struct {
	CPUCores float64 `json:"cpu_cores"`
	MemBytes float64 `json:"mem_bytes"`
}

// QueryVMsPoolResponse is the response for POST /query/vms/pool.
type QueryVMsPoolResponse struct {
	Points []PoolPoint              `json:"points"`
	VMs    map[string][]VMPoolPoint `json:"vms,omitempty"`
}
