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
	Timestamp       string  `json:"timestamp"`
	DiskSizeGB      float64 `json:"disk_size_gb"`
	DiskUsedGB      float64 `json:"disk_used_gb"`
	CPUCores        float64 `json:"cpu_cores"`   // CPU seconds/second (cores used)
	CPUNominal      float64 `json:"cpu_nominal"` // total nominal cores
	NetworkTXMbps   float64 `json:"network_tx_mbps"`
	NetworkRXMbps   float64 `json:"network_rx_mbps"`
	MemoryRSSGB     float64 `json:"memory_rss_gb"`
	MemoryNominalGB float64 `json:"memory_nominal_gb"`
	VMName          string  `json:"vm_name"`
}

func computeUsageData(metrics []types.Metric) []usageDataPoint {
	if len(metrics) == 0 {
		return nil
	}

	points := make([]usageDataPoint, 0, len(metrics))
	for i, m := range metrics {
		p := usageDataPoint{
			Timestamp:       m.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
			DiskSizeGB:      float64(m.DiskSizeBytes) / 1e9,
			DiskUsedGB:      float64(m.DiskLogicalUsedBytes) / 1e9,
			CPUNominal:      m.CPUNominal,
			MemoryRSSGB:     float64(m.MemoryRSSBytes) / 1e9,
			MemoryNominalGB: float64(m.MemoryNominalBytes) / 1e9,
			VMName:          m.VMName,
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
		}

		points = append(points, p)
	}

	// Remove the first point since it has no derived values
	if len(points) > 1 {
		points = points[1:]
	}

	return points
}
