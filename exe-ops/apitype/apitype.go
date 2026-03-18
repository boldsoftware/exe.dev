package apitype

import (
	"fmt"
	"strings"
	"time"
)

// Report is the payload an agent sends to the server.
type Report struct {
	Name           string      `json:"name"`
	Tags           []string    `json:"tags,omitempty"`
	Timestamp      time.Time   `json:"timestamp"`
	AgentVersion   string      `json:"agent_version,omitempty"`
	Arch           string      `json:"arch,omitempty"`
	CPU            float64     `json:"cpu_percent"`
	MemTotal       int64       `json:"mem_total"`
	MemUsed        int64       `json:"mem_used"`
	MemFree        int64       `json:"mem_free"`
	MemSwap        int64       `json:"mem_swap"`
	MemSwapTotal   int64       `json:"mem_swap_total"`
	DiskTotal      int64       `json:"disk_total"`
	DiskUsed       int64       `json:"disk_used"`
	DiskFree       int64       `json:"disk_free"`
	NetSend        int64       `json:"net_send"`
	NetRecv        int64       `json:"net_recv"`
	ZFSUsed        *int64      `json:"zfs_used,omitempty"`
	ZFSFree        *int64      `json:"zfs_free,omitempty"`
	BackupZFSUsed  *int64      `json:"backup_zfs_used,omitempty"`
	BackupZFSFree  *int64      `json:"backup_zfs_free,omitempty"`
	UptimeSecs     int64       `json:"uptime_secs"`
	LoadAvg1       float64     `json:"load_avg_1"`
	LoadAvg5       float64     `json:"load_avg_5"`
	LoadAvg15      float64     `json:"load_avg_15"`
	ZFSPoolHealth  *string     `json:"zfs_pool_health,omitempty"`
	ZFSArcSize     *int64      `json:"zfs_arc_size,omitempty"`
	ZFSArcHitRate  *float64    `json:"zfs_arc_hit_rate,omitempty"`
	NetRxErrors    int64       `json:"net_rx_errors"`
	NetRxDropped   int64       `json:"net_rx_dropped"`
	NetTxErrors    int64       `json:"net_tx_errors"`
	NetTxDropped   int64       `json:"net_tx_dropped"`
	ConntrackCount *int64      `json:"conntrack_count,omitempty"`
	ConntrackMax   *int64      `json:"conntrack_max,omitempty"`
	FDAllocated    int64       `json:"fd_allocated"`
	FDMax          int64       `json:"fd_max"`
	Components     []Component `json:"components,omitempty"`
	Updates        []string    `json:"updates,omitempty"`
	FailedUnits    []string    `json:"failed_units,omitempty"`
	ZFSPools       []ZFSPool   `json:"zfs_pools,omitempty"`
}

// ZFSPool represents per-pool ZFS metrics.
type ZFSPool struct {
	Name        string `json:"name"`
	Health      string `json:"health"`
	Used        int64  `json:"used"`
	Free        int64  `json:"free"`
	FragPct     int    `json:"frag_pct"` // -1 if not applicable
	CapPct      int    `json:"cap_pct"`
	ReadErrors  int64  `json:"read_errors"`
	WriteErrors int64  `json:"write_errors"`
	CksumErrors int64  `json:"cksum_errors"`
}

// Component represents an exe component (exelet, exeprox).
type Component struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"` // "active", "inactive", "not-found"
}

// FleetServer is returned by GET /api/v1/fleet — extended server data for fleet-wide views.
type FleetServer struct {
	Name           string      `json:"name"`
	Hostname       string      `json:"hostname"`
	Role           string      `json:"role"`
	Region         string      `json:"region"`
	Env            string      `json:"env"`
	LastSeen       string      `json:"last_seen"`
	AgentVersion   string      `json:"agent_version,omitempty"`
	CPU            float64     `json:"cpu_percent"`
	MemTotal       int64       `json:"mem_total"`
	MemUsed        int64       `json:"mem_used"`
	DiskTotal      int64       `json:"disk_total"`
	DiskUsed       int64       `json:"disk_used"`
	ConntrackCount *int64      `json:"conntrack_count,omitempty"`
	ConntrackMax   *int64      `json:"conntrack_max,omitempty"`
	FDAllocated    int64       `json:"fd_allocated"`
	FDMax          int64       `json:"fd_max"`
	Components     []Component `json:"components,omitempty"`
	Updates        []string    `json:"updates,omitempty"`
	FailedUnits    []string    `json:"failed_units,omitempty"`
	ZFSPools       []ZFSPool   `json:"zfs_pools,omitempty"`
	ZFSPoolHealth  *string     `json:"zfs_pool_health,omitempty"`
	ZFSArcSize     *int64      `json:"zfs_arc_size,omitempty"`
	ZFSArcHitRate  *float64    `json:"zfs_arc_hit_rate,omitempty"`
	NetRxErrors    int64       `json:"net_rx_errors"`
	NetTxErrors    int64       `json:"net_tx_errors"`
	NetRxDropped   int64       `json:"net_rx_dropped"`
	NetTxDropped   int64       `json:"net_tx_dropped"`
}

// ServerSummary is returned by GET /api/v1/servers.
type ServerSummary struct {
	Name     string   `json:"name"`
	Hostname string   `json:"hostname"`
	Role     string   `json:"role"`
	Region   string   `json:"region"`
	Env      string   `json:"env"`
	Instance string   `json:"instance"`
	Tags     []string `json:"tags"`
	LastSeen string   `json:"last_seen"`

	AgentVersion     string `json:"agent_version,omitempty"`
	Arch             string `json:"arch,omitempty"`
	UpgradeAvailable bool   `json:"upgrade_available,omitempty"`

	// Exelet capacity (from exelet_capacity table, zero if not an exelet)
	Instances int `json:"instances,omitempty"`
	Capacity  int `json:"capacity,omitempty"`

	// Latest snapshot
	CPU          float64     `json:"cpu_percent"`
	MemTotal     int64       `json:"mem_total"`
	MemUsed      int64       `json:"mem_used"`
	MemSwap      int64       `json:"mem_swap"`
	MemSwapTotal int64       `json:"mem_swap_total"`
	DiskTotal    int64       `json:"disk_total"`
	DiskUsed     int64       `json:"disk_used"`
	NetSend      int64       `json:"net_send"`
	NetRecv      int64       `json:"net_recv"`
	Components   []Component `json:"components,omitempty"`
}

// ServerDetail is returned by GET /api/v1/servers/{name}.
type ServerDetail struct {
	ServerSummary
	MemFree        int64               `json:"mem_free"`
	MemSwap        int64               `json:"mem_swap"`
	MemSwapTotal   int64               `json:"mem_swap_total"`
	DiskFree       int64               `json:"disk_free"`
	ZFSUsed        *int64              `json:"zfs_used,omitempty"`
	ZFSFree        *int64              `json:"zfs_free,omitempty"`
	BackupZFSUsed  *int64              `json:"backup_zfs_used,omitempty"`
	BackupZFSFree  *int64              `json:"backup_zfs_free,omitempty"`
	UptimeSecs     int64               `json:"uptime_secs"`
	LoadAvg1       float64             `json:"load_avg_1"`
	LoadAvg5       float64             `json:"load_avg_5"`
	LoadAvg15      float64             `json:"load_avg_15"`
	ZFSPoolHealth  *string             `json:"zfs_pool_health,omitempty"`
	ZFSArcSize     *int64              `json:"zfs_arc_size,omitempty"`
	ZFSArcHitRate  *float64            `json:"zfs_arc_hit_rate,omitempty"`
	NetRxErrors    int64               `json:"net_rx_errors"`
	NetRxDropped   int64               `json:"net_rx_dropped"`
	NetTxErrors    int64               `json:"net_tx_errors"`
	NetTxDropped   int64               `json:"net_tx_dropped"`
	ConntrackCount *int64              `json:"conntrack_count,omitempty"`
	ConntrackMax   *int64              `json:"conntrack_max,omitempty"`
	FDAllocated    int64               `json:"fd_allocated"`
	FDMax          int64               `json:"fd_max"`
	Components     []Component         `json:"components,omitempty"`
	Updates        []string            `json:"updates,omitempty"`
	FailedUnits    []string            `json:"failed_units,omitempty"`
	ZFSPools       []ZFSPool           `json:"zfs_pools,omitempty"`
	FirstSeen      string              `json:"first_seen"`
	History        []ReportRow         `json:"history,omitempty"`
	ExeletCapacity []ExeletCapacityRow `json:"exelet_capacity,omitempty"`
}

// ExeletCapacityRow is a historical exelet capacity data point.
type ExeletCapacityRow struct {
	Timestamp string `json:"timestamp"`
	Instances int    `json:"instances"`
	Capacity  int    `json:"capacity"`
}

// ExeletCapacitySummary is the aggregated exelet capacity across servers.
type ExeletCapacitySummary struct {
	TotalInstances int `json:"total_instances"`
	TotalCapacity  int `json:"total_capacity"`
}

// ReportRow is a historical data point.
type ReportRow struct {
	Timestamp  string  `json:"timestamp"`
	CPU        float64 `json:"cpu_percent"`
	MemUsed    int64   `json:"mem_used"`
	DiskUsed   int64   `json:"disk_used"`
	NetSend    int64   `json:"net_send"`
	NetRecv    int64   `json:"net_recv"`
	UptimeSecs int64   `json:"uptime_secs"`
}

// CustomAlertRule is a user-defined alert rule.
type CustomAlertRule struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Metric    string  `json:"metric"`
	Operator  string  `json:"operator"`
	Threshold float64 `json:"threshold"`
	Severity  string  `json:"severity"`
	Enabled   bool    `json:"enabled"`
	CreatedAt string  `json:"created_at,omitempty"`
}

// HostnameParts contains parsed hostname components.
type HostnameParts struct {
	Role     string
	Region   string
	Env      string
	Instance string
}

// ParseHostname parses hostnames in the form role-region-env-instance.
// Example: exelet-nyc-prod-01 → {Role: "exelet", Region: "nyc", Env: "prod", Instance: "01"}
//
// Legacy exe-ctr-* hosts are mapped to Role "exelet", Region "pdx", Env "prod".
// Example: exe-ctr-04 → {Role: "exelet", Region: "pdx", Env: "prod", Instance: "04"}
//
// Returns an error if the hostname doesn't match any expected pattern.
func ParseHostname(hostname string) (HostnameParts, error) {
	// Legacy exe-ctr-<instance> hosts are exelets in pdx/prod.
	if rest, ok := strings.CutPrefix(hostname, "exe-ctr-"); ok && rest != "" {
		return HostnameParts{
			Role:     "exelet",
			Region:   "pdx",
			Env:      "prod",
			Instance: rest,
		}, nil
	}

	parts := strings.SplitN(hostname, "-", 4)
	if len(parts) != 4 {
		return HostnameParts{}, fmt.Errorf("hostname %q does not match pattern role-region-env-instance", hostname)
	}
	for i, p := range parts {
		if p == "" {
			return HostnameParts{}, fmt.Errorf("hostname %q has empty component at position %d", hostname, i)
		}
	}

	role := parts[0]
	instance := parts[3]

	// Replica servers: exelet-<region>-<env>-<ID>-replica → role "replica".
	if inst, ok := strings.CutSuffix(instance, "-replica"); ok {
		return HostnameParts{
			Role:     "replica",
			Region:   parts[1],
			Env:      parts[2],
			Instance: inst,
		}, nil
	}

	return HostnameParts{
		Role:     role,
		Region:   parts[1],
		Env:      parts[2],
		Instance: instance,
	}, nil
}
