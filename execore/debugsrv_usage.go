package execore

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"exe.dev/metricsd/types"
)

// handleDebugUsageAPI is the JSON API endpoint for fetching VM metrics.
// It is used by the debug UI pages to load Vega-Lite chart data.
// Query params: vm_names (comma-separated), hours (int).
func (s *Server) handleDebugUsageAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if s.metricsdURL == "" {
		http.Error(w, "metricsd not configured", http.StatusServiceUnavailable)
		return
	}

	vmNamesParam := r.URL.Query().Get("vm_names")
	if vmNamesParam == "" {
		http.Error(w, "vm_names parameter is required", http.StatusBadRequest)
		return
	}
	vmNames := strings.Split(vmNamesParam, ",")

	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		var err error
		hours, err = strconv.Atoi(h)
		if err != nil || hours < 1 || hours > 744 {
			http.Error(w, "hours must be between 1 and 744", http.StatusBadRequest)
			return
		}
	}

	client := newMetricsClient(s.metricsdURL)
	metrics, err := client.queryVMs(ctx, vmNames, hours)
	if err != nil {
		slog.ErrorContext(ctx, "failed to query metricsd", "error", err)
		http.Error(w, "failed to query metrics", http.StatusBadGateway)
		return
	}

	// Compute derived metrics (CPU cores used, network rate) for the frontend
	result := make(map[string][]usageDataPoint)
	for vmName, vmMetrics := range metrics {
		result[vmName] = computeUsageData(vmMetrics)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// usageDataPoint is the derived data point sent to the frontend for charting.
type usageDataPoint struct {
	Timestamp         string  `json:"timestamp"`
	DiskSizeGB        float64 `json:"disk_size_gb"`
	DiskUsedGB        float64 `json:"disk_used_gb"`
	DiskLogicalUsedGB float64 `json:"disk_logical_used_gb"`
	CPUCores          float64 `json:"cpu_cores"`   // CPU seconds/second (cores used)
	CPUNominal        float64 `json:"cpu_nominal"` // total nominal cores
	NetworkTXMbps     float64 `json:"network_tx_mbps"`
	NetworkRXMbps     float64 `json:"network_rx_mbps"`
	// MemoryUsedGB is the user-facing "VM memory used" figure: cgroup
	// memory.current minus host page cache attributed to the VM
	// (memory.stat "file"). The page cache from VM disk I/O is reclaimable
	// and not part of the guest working set, so charging it to the VM
	// dramatically overstates real usage. Old metrics without the
	// memory.stat breakdown (MemoryFileBytes == 0) fall back to the raw
	// memory.current. The raw breakdown is available alongside as
	// memory_anon_gb / memory_file_gb / etc.
	MemoryUsedGB         float64 `json:"memory_used_gb"`
	MemoryAnonGB         float64 `json:"memory_anon_gb"`
	MemoryFileGB         float64 `json:"memory_file_gb"`
	MemoryKernelGB       float64 `json:"memory_kernel_gb"`
	MemoryShmemGB        float64 `json:"memory_shmem_gb"`
	MemorySlabGB         float64 `json:"memory_slab_gb"`
	MemoryInactiveFileGB float64 `json:"memory_inactive_file_gb"`
	MemorySwapGB         float64 `json:"memory_swap_gb"`
	MemoryNominalGB      float64 `json:"memory_nominal_gb"`
	IOReadMBps           float64 `json:"io_read_mbps"`
	IOWriteMBps          float64 `json:"io_write_mbps"`
	VMName               string  `json:"vm_name"`
}

func computeUsageData(metrics []types.Metric) []usageDataPoint {
	if len(metrics) == 0 {
		return nil
	}

	points := make([]usageDataPoint, 0, len(metrics))
	for i, m := range metrics {
		p := usageDataPoint{
			Timestamp:            m.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
			DiskSizeGB:           float64(m.DiskSizeBytes) / 1e9,
			DiskUsedGB:           float64(m.DiskUsedBytes) / 1e9,
			DiskLogicalUsedGB:    float64(m.DiskLogicalUsedBytes) / 1e9,
			CPUNominal:           m.CPUNominal,
			MemoryUsedGB:         float64(saturatingSub(m.MemoryRSSBytes, m.MemoryFileBytes)) / 1e9,
			MemoryAnonGB:         float64(m.MemoryAnonBytes) / 1e9,
			MemoryFileGB:         float64(m.MemoryFileBytes) / 1e9,
			MemoryKernelGB:       float64(m.MemoryKernelBytes) / 1e9,
			MemoryShmemGB:        float64(m.MemoryShmemBytes) / 1e9,
			MemorySlabGB:         float64(m.MemorySlabBytes) / 1e9,
			MemoryInactiveFileGB: float64(m.MemoryInactiveFileBytes) / 1e9,
			MemorySwapGB:         float64(m.MemorySwapBytes) / 1e9,
			MemoryNominalGB:      float64(m.MemoryNominalBytes) / 1e9,
			VMName:               m.VMName,
		}

		// Compute CPU cores used (seconds/second) from cumulative seconds
		if i > 0 {
			prev := metrics[i-1]
			dt := m.Timestamp.Sub(prev.Timestamp).Seconds()
			if dt > 0 {
				cpuDelta := m.CPUUsedCumulativeSecs - prev.CPUUsedCumulativeSecs
				if cpuDelta >= 0 {
					p.CPUCores = cpuDelta / dt
					if p.CPUCores > m.CPUNominal {
						p.CPUCores = m.CPUNominal
					}
				}
			}

			// Compute network rates
			txDelta := m.NetworkTXBytes - prev.NetworkTXBytes
			rxDelta := m.NetworkRXBytes - prev.NetworkRXBytes
			if txDelta >= 0 {
				p.NetworkTXMbps = float64(txDelta) * 8 / 1e6 / dt
			}
			if rxDelta >= 0 {
				p.NetworkRXMbps = float64(rxDelta) * 8 / 1e6 / dt
			}

			// Compute IO rates
			ioReadDelta := m.IOReadBytes - prev.IOReadBytes
			ioWriteDelta := m.IOWriteBytes - prev.IOWriteBytes
			if ioReadDelta >= 0 {
				p.IOReadMBps = float64(ioReadDelta) / dt / 1e6
			}
			if ioWriteDelta >= 0 {
				p.IOWriteMBps = float64(ioWriteDelta) / dt / 1e6
			}
		}

		points = append(points, p)
	}

	// Remove the first point since it has no derived values
	if len(points) > 1 {
		points = points[1:]
	}

	return points
}

// saturatingSub returns a-b, or 0 if b > a.
func saturatingSub(a, b int64) int64 {
	if b >= a {
		return 0
	}
	return a - b
}
