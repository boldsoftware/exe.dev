package collector

import "context"

// Collector gathers a specific type of system metric.
type Collector interface {
	Name() string
	Collect(ctx context.Context) error
}

// Metrics holds all collected system metrics.
type Metrics struct {
	CPU        float64
	MemTotal   int64
	MemUsed    int64
	MemFree    int64
	MemSwap    int64
	DiskTotal  int64
	DiskUsed   int64
	DiskFree   int64
	NetSend    int64
	NetRecv    int64
	ZFSUsed    *int64
	ZFSFree    *int64
	UptimeSecs int64
	Components []ComponentInfo
	Updates    []string
}

// ComponentInfo holds exe component version and status info.
type ComponentInfo struct {
	Name    string
	Version string
	Status  string
}
