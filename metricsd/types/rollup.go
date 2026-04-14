package types

import "time"

// HourlyMetric is one row from vm_metrics_hourly.
type HourlyMetric struct {
	HourStart              time.Time `json:"hour_start"`
	DayStart               time.Time `json:"day_start"`
	Host                   string    `json:"host"`
	VMID                   string    `json:"vm_id"`
	VMName                 string    `json:"vm_name"`
	ResourceGroup          string    `json:"resource_group"`
	DiskLogicalMaxBytes    int64     `json:"disk_logical_max_bytes"`
	DiskCompressedMaxBytes int64     `json:"disk_compressed_max_bytes"`
	DiskProvisionedBytes   int64     `json:"disk_provisioned_bytes"`
	NetworkTXDeltaBytes    int64     `json:"network_tx_delta_bytes"`
	NetworkRXDeltaBytes    int64     `json:"network_rx_delta_bytes"`
	CPUDeltaSeconds        float64   `json:"cpu_delta_seconds"`
	IOReadDeltaBytes       int64     `json:"io_read_delta_bytes"`
	IOWriteDeltaBytes      int64     `json:"io_write_delta_bytes"`
	MemoryRSSMaxBytes      int64     `json:"memory_rss_max_bytes"`
	MemorySwapMaxBytes     int64     `json:"memory_swap_max_bytes"`
	SampleCount            int       `json:"sample_count"`
}

// DailyMetric is one row from vm_metrics_daily.
type DailyMetric struct {
	DayStart                time.Time `json:"day_start"`
	Host                    string    `json:"host"`
	VMID                    string    `json:"vm_id"`
	VMName                  string    `json:"vm_name"`
	ResourceGroup           string    `json:"resource_group"`
	DiskLogicalAvgBytes     int64     `json:"disk_logical_avg_bytes"`
	DiskLogicalMaxBytes     int64     `json:"disk_logical_max_bytes"`
	DiskCompressedAvgBytes  int64     `json:"disk_compressed_avg_bytes"`
	DiskProvisionedMaxBytes int64     `json:"disk_provisioned_max_bytes"`
	NetworkTXBytes          int64     `json:"network_tx_bytes"`
	NetworkRXBytes          int64     `json:"network_rx_bytes"`
	CPUSeconds              float64   `json:"cpu_seconds"`
	IOReadBytes             int64     `json:"io_read_bytes"`
	IOWriteBytes            int64     `json:"io_write_bytes"`
	MemoryRSSMaxBytes       int64     `json:"memory_rss_max_bytes"`
	MemorySwapMaxBytes      int64     `json:"memory_swap_max_bytes"`
	HoursWithData           int       `json:"hours_with_data"`
}

// VMUsageSummary is the per-VM usage for a time period (from daily rollups).
type VMUsageSummary struct {
	VMID           string  `json:"vm_id"`
	VMName         string  `json:"vm_name"`
	ResourceGroup  string  `json:"resource_group"`
	DiskAvgBytes   int64   `json:"disk_avg_bytes"`  // average daily logical disk usage
	DiskMaxBytes   int64   `json:"disk_max_bytes"`  // peak daily logical disk usage
	BandwidthBytes int64   `json:"bandwidth_bytes"` // total network tx+rx
	CPUSeconds     float64 `json:"cpu_seconds"`
	IOReadBytes    int64   `json:"io_read_bytes"`
	IOWriteBytes   int64   `json:"io_write_bytes"`
	DaysWithData   int     `json:"days_with_data"`
}

// UsageSummary is the per-resource-group usage summary for a time period.
type UsageSummary struct {
	ResourceGroup  string           `json:"resource_group"`
	PeriodStart    time.Time        `json:"period_start"`
	PeriodEnd      time.Time        `json:"period_end"`
	DiskAvgBytes   int64            `json:"disk_avg_bytes"`  // SUM of per-VM avg daily disk (for billing)
	DiskPeakBytes  int64            `json:"disk_peak_bytes"` // MAX observed disk across all VMs and days
	BandwidthBytes int64            `json:"bandwidth_bytes"` // total network tx+rx
	CPUSeconds     float64          `json:"cpu_seconds"`
	VMs            []VMUsageSummary `json:"vms"`
}

// QueryUsageRequest is the request for POST /query/usage.
type QueryUsageRequest struct {
	ResourceGroups []string  `json:"resource_groups"`
	Start          time.Time `json:"start"`
	End            time.Time `json:"end"`
}

// QueryUsageResponse is the response for POST /query/usage.
type QueryUsageResponse struct {
	Metrics []UsageSummary `json:"metrics"`
}

// MonthlyMetric is one row from vm_metrics_monthly.
type MonthlyMetric struct {
	MonthStart              time.Time `json:"month_start"`
	Host                    string    `json:"host"`
	VMID                    string    `json:"vm_id"`
	VMName                  string    `json:"vm_name"`
	ResourceGroup           string    `json:"resource_group"`
	DiskLogicalAvgBytes     int64     `json:"disk_logical_avg_bytes"`
	DiskLogicalMaxBytes     int64     `json:"disk_logical_max_bytes"`
	DiskCompressedAvgBytes  int64     `json:"disk_compressed_avg_bytes"`
	DiskProvisionedMaxBytes int64     `json:"disk_provisioned_max_bytes"`
	NetworkTXBytes          int64     `json:"network_tx_bytes"`
	NetworkRXBytes          int64     `json:"network_rx_bytes"`
	CPUSeconds              float64   `json:"cpu_seconds"`
	IOReadBytes             int64     `json:"io_read_bytes"`
	IOWriteBytes            int64     `json:"io_write_bytes"`
	MemoryRSSMaxBytes       int64     `json:"memory_rss_max_bytes"`
	MemorySwapMaxBytes      int64     `json:"memory_swap_max_bytes"`
	DaysWithData            int       `json:"days_with_data"`
}

// QueryHourlyRequest is the request for POST /query/hourly.
type QueryHourlyRequest struct {
	ResourceGroups []string  `json:"resource_groups"`
	Start          time.Time `json:"start"`
	End            time.Time `json:"end"`
}

// QueryHourlyResponse is the response for POST /query/hourly.
type QueryHourlyResponse struct {
	Metrics []HourlyMetric `json:"metrics"`
}

// QueryDailyRequest is the request for POST /query/daily.
type QueryDailyRequest struct {
	ResourceGroups []string  `json:"resource_groups"`
	Start          time.Time `json:"start"`
	End            time.Time `json:"end"`
	GroupByVM      bool      `json:"group_by_vm,omitempty"`
}

// QueryDailyResponse is the response for POST /query/daily.
type QueryDailyResponse struct {
	Metrics []DailyMetric `json:"metrics"`
}

// QueryVMsOverLimitRequest is the request for POST /query/vms-over-limit.
type QueryVMsOverLimitRequest struct {
	VMIDs                  []string `json:"vm_ids"`
	DiskIncludedBytes      int64    `json:"disk_included_bytes"`
	BandwidthIncludedBytes int64    `json:"bandwidth_included_bytes"`
}

// VMOverLimit is a VM that exceeds at least one threshold.
type VMOverLimit struct {
	VMID           string `json:"vm_id"`
	VMName         string `json:"vm_name"`
	DiskAvgBytes   int64  `json:"disk_avg_bytes"`
	BandwidthBytes int64  `json:"bandwidth_bytes"`
	DiskOver       bool   `json:"disk_over"`
	BandwidthOver  bool   `json:"bandwidth_over"`
}

// QueryVMsOverLimitResponse is the response for POST /query/vms-over-limit.
type QueryVMsOverLimitResponse struct {
	VMs []VMOverLimit `json:"vms"`
}

// QueryMonthlyRequest is the request for POST /query/monthly.
type QueryMonthlyRequest struct {
	ResourceGroups []string  `json:"resource_groups"`
	Start          time.Time `json:"start"`
	End            time.Time `json:"end"`
	GroupByVM      bool      `json:"group_by_vm,omitempty"`
}

// QueryMonthlyResponse is the response for POST /query/monthly.
type QueryMonthlyResponse struct {
	Metrics []MonthlyMetric `json:"metrics"`
}
