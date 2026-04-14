package execore

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// billingUsageMonthlyMetric is one month's aggregated usage returned by
// GET /api/billing/usage?granularity=monthly.
type billingUsageMonthlyMetric struct {
	Date           string `json:"date"`
	DiskAvgBytes   int64  `json:"disk_avg_bytes"`
	BandwidthBytes int64  `json:"bandwidth_bytes"`
}

// billingUsageDailyMetric is one day's aggregated usage returned by
// GET /api/billing/usage?granularity=daily.
type billingUsageDailyMetric struct {
	Date           string `json:"date"`
	DiskAvgBytes   int64  `json:"disk_avg_bytes"`
	BandwidthBytes int64  `json:"bandwidth_bytes"`
}

// billingUsageResponse is the response body for GET /api/billing/usage.
type billingUsageResponse struct {
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	Metrics     any       `json:"metrics"`
}

// billingUsageVMEntry is a per-VM summary in the /api/billing/usage/vms response.
type billingUsageVMEntry struct {
	VMID           string  `json:"vm_id"`
	VMName         string  `json:"vm_name"`
	DiskAvgBytes   int64   `json:"disk_avg_bytes"`
	BandwidthBytes int64   `json:"bandwidth_bytes"`
	CPUSeconds     float64 `json:"cpu_seconds"`
	IOReadBytes    int64   `json:"io_read_bytes"`
	IOWriteBytes   int64   `json:"io_write_bytes"`
	DaysWithData   int     `json:"days_with_data"`
}

// handleAPIBillingUsage handles GET /api/billing/usage?granularity=monthly|daily&start=...&end=...
// It returns usage metrics for the authenticated user aggregated by month or day.
// resource_group is the userID (see debugsrv.go billing section for the pattern).
func (s *Server) handleAPIBillingUsage(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()

	q := r.URL.Query()
	granularity := q.Get("granularity")
	startStr := q.Get("start")
	endStr := q.Get("end")

	if granularity == "" {
		http.Error(w, "granularity is required", http.StatusBadRequest)
		return
	}
	if startStr == "" {
		http.Error(w, "start is required", http.StatusBadRequest)
		return
	}
	if endStr == "" {
		http.Error(w, "end is required", http.StatusBadRequest)
		return
	}

	switch granularity {
	case "monthly", "daily":
	default:
		http.Error(w, "granularity must be 'monthly' or 'daily'", http.StatusBadRequest)
		return
	}

	start, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		http.Error(w, "start must be RFC3339 (e.g. 2024-01-01T00:00:00Z)", http.StatusBadRequest)
		return
	}
	end, err := time.Parse(time.RFC3339, endStr)
	if err != nil {
		http.Error(w, "end must be RFC3339 (e.g. 2024-02-01T00:00:00Z)", http.StatusBadRequest)
		return
	}

	if s.metricsdURL == "" {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return
	}

	client := newMetricsClient(s.metricsdURL)
	w.Header().Set("Content-Type", "application/json")

	switch granularity {
	case "monthly":
		monthly, err := client.queryMonthly(ctx, []string{userID}, start, end)
		if err != nil {
			slog.ErrorContext(ctx, "failed to query monthly metrics", "error", err, "user_id", userID)
			http.Error(w, "failed to query metrics", http.StatusBadGateway)
			return
		}

		metrics := make([]billingUsageMonthlyMetric, 0, len(monthly))
		for _, m := range monthly {
			metrics = append(metrics, billingUsageMonthlyMetric{
				Date:           m.MonthStart.UTC().Format("2006-01-02"),
				DiskAvgBytes:   m.DiskLogicalAvgBytes,
				BandwidthBytes: m.NetworkTXBytes + m.NetworkRXBytes,
			})
		}

		json.NewEncoder(w).Encode(billingUsageResponse{
			PeriodStart: start,
			PeriodEnd:   end,
			Metrics:     metrics,
		})

	case "daily":
		daily, err := client.queryDaily(ctx, []string{userID}, start, end)
		if err != nil {
			slog.ErrorContext(ctx, "failed to query daily metrics", "error", err, "user_id", userID)
			http.Error(w, "failed to query metrics", http.StatusBadGateway)
			return
		}

		// metricsd already aggregates across VMs per day (GROUP BY day_start)
		// when GroupByVM is false, so just map the rows directly.
		metrics := make([]billingUsageDailyMetric, 0, len(daily))
		for _, m := range daily {
			metrics = append(metrics, billingUsageDailyMetric{
				Date:           m.DayStart.UTC().Format("2006-01-02"),
				DiskAvgBytes:   m.DiskLogicalAvgBytes,
				BandwidthBytes: m.NetworkTXBytes + m.NetworkRXBytes,
			})
		}

		json.NewEncoder(w).Encode(billingUsageResponse{
			PeriodStart: start,
			PeriodEnd:   end,
			Metrics:     metrics,
		})
	}
}

// handleAPIBillingUsageVMs handles GET /api/billing/usage/vms?start=...&end=...
// It returns per-VM usage metrics for the authenticated user over the specified period.
// resource_group is the userID (see debugsrv.go billing section for the pattern).
func (s *Server) handleAPIBillingUsageVMs(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()

	q := r.URL.Query()
	startStr := q.Get("start")
	endStr := q.Get("end")

	if startStr == "" {
		http.Error(w, "start is required", http.StatusBadRequest)
		return
	}
	if endStr == "" {
		http.Error(w, "end is required", http.StatusBadRequest)
		return
	}

	start, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		http.Error(w, "start must be RFC3339 (e.g. 2024-01-01T00:00:00Z)", http.StatusBadRequest)
		return
	}
	end, err := time.Parse(time.RFC3339, endStr)
	if err != nil {
		http.Error(w, "end must be RFC3339 (e.g. 2024-02-01T00:00:00Z)", http.StatusBadRequest)
		return
	}

	if s.metricsdURL == "" {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return
	}

	client := newMetricsClient(s.metricsdURL)
	summaries, err := client.queryUsage(ctx, []string{userID}, start, end)
	if err != nil {
		slog.ErrorContext(ctx, "failed to query vm usage metrics", "error", err, "user_id", userID)
		http.Error(w, "failed to query metrics", http.StatusBadGateway)
		return
	}

	// Extract per-VM rows from the resource-group summary.
	// queryUsage returns one UsageSummary per resource group; each has a VMs slice.
	vms := make([]billingUsageVMEntry, 0)
	for _, summary := range summaries {
		for _, vm := range summary.VMs {
			vms = append(vms, billingUsageVMEntry{
				VMID:           vm.VMID,
				VMName:         vm.VMName,
				DiskAvgBytes:   vm.DiskAvgBytes,
				BandwidthBytes: vm.BandwidthBytes,
				CPUSeconds:     vm.CPUSeconds,
				IOReadBytes:    vm.IOReadBytes,
				IOWriteBytes:   vm.IOWriteBytes,
				DaysWithData:   vm.DaysWithData,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(billingUsageResponse{
		PeriodStart: start,
		PeriodEnd:   end,
		Metrics:     vms,
	})
}


