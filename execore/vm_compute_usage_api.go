package execore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"exe.dev/exedb"
)

// computeUsagePoint is a single chart-ready data point for the UI.
// Rates are pre-computed from cumulative counters.
type computeUsagePoint struct {
	Timestamp         time.Time `json:"timestamp"`
	CPUPercent        float64   `json:"cpu_percent"`
	MemoryBytes       int64     `json:"memory_bytes"`
	DiskUsedBytes     int64     `json:"disk_used_bytes"`
	DiskCapacityBytes int64     `json:"disk_capacity_bytes"`
	NetRxBytesPerSec  float64   `json:"net_rx_bytes_per_sec"`
	NetTxBytesPerSec  float64   `json:"net_tx_bytes_per_sec"`
}

// handleAPIVMComputeUsage handles GET /api/vm/{name}/compute-usage?hours=24|168|720
// Returns chart-ready data points with pre-computed rates for a single VM.
func (s *Server) handleAPIVMComputeUsage(w http.ResponseWriter, r *http.Request, userID, vmName string) {
	ctx := r.Context()

	_, err := withRxRes1(s, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            vmName,
		CreatedByUserID: userID,
	})
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("VM %q not found", vmName),
		})
		return
	}

	hoursStr := r.URL.Query().Get("hours")
	hours := 24
	if hoursStr != "" {
		parsed, err := strconv.Atoi(hoursStr)
		if err != nil || parsed <= 0 || parsed > 720 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "invalid hours parameter (must be 1-720)",
			})
			return
		}
		hours = parsed
	}

	if s.metricsdURL == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "metrics service unavailable",
		})
		return
	}

	client := newMetricsClient(s.metricsdURL)
	result, err := client.queryVMs(ctx, []string{vmName}, hours)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("failed to query metrics: %v", err),
		})
		return
	}

	raw := result[vmName]
	points := make([]computeUsagePoint, 0, len(raw))
	for i, m := range raw {
		p := computeUsagePoint{
			Timestamp:         m.Timestamp,
			MemoryBytes:       m.MemoryRSSBytes,
			DiskUsedBytes:     m.DiskLogicalUsedBytes,
			DiskCapacityBytes: m.DiskSizeBytes,
		}
		if i > 0 {
			prev := raw[i-1]
			dt := m.Timestamp.Sub(prev.Timestamp).Seconds()
			if dt > 0 {
				p.CPUPercent = (m.CPUUsedCumulativeSecs - prev.CPUUsedCumulativeSecs) / dt * 100
				p.NetRxBytesPerSec = float64(m.NetworkRXBytes-prev.NetworkRXBytes) / dt
				p.NetTxBytesPerSec = float64(m.NetworkTXBytes-prev.NetworkTXBytes) / dt
			}
		}
		points = append(points, p)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(points)
}
