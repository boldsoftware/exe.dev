package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
)

// billingUsageMonthlyMetric is one month's aggregated usage returned by
// GET /api/billing/usage?granularity=monthly.
type billingUsageMonthlyMetric struct {
	Date           string `json:"date"`
	VMID           string `json:"vm_id"`
	VMName         string `json:"vm_name"`
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

// billingUsageVMsResponse is the response body for GET /api/billing/usage/vms.
type billingUsageVMsResponse struct {
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`

	Metrics []billingUsageVMEntry `json:"metrics"`
}

// billingUsageVMEntry is a per-VM summary in the /api/billing/usage/vms response.
type billingUsageVMEntry struct {
	VMID                   string  `json:"vm_id"`
	VMName                 string  `json:"vm_name"`
	DiskProvisionedBytes   int64   `json:"disk_provisioned_bytes"`
	DiskAvgBytes           int64   `json:"disk_avg_bytes"`
	BandwidthBytes         int64   `json:"bandwidth_bytes"`
	CPUSeconds             float64 `json:"cpu_seconds"`
	IOReadBytes            int64   `json:"io_read_bytes"`
	IOWriteBytes           int64   `json:"io_write_bytes"`
	DaysWithData           int     `json:"days_with_data"`
	IncludedDiskBytes      uint64  `json:"included_disk_bytes"`
	IncludedBandwidthBytes uint64  `json:"included_bandwidth_bytes"`
	OverageDiskBytes       int64   `json:"overage_disk_bytes"`
	OverageBandwidthBytes  int64   `json:"overage_bandwidth_bytes"`
	// Display holds pre-formatted strings for the UI — use these instead of raw bytes.
	Display vmUsageDisplay `json:"display"`
}

// vmUsageDisplay holds human-readable strings for a VM's usage entry.
type vmUsageDisplay struct {
	DiskProvisioned   string `json:"disk_provisioned"`   // e.g. "25 GiB"
	Bandwidth         string `json:"bandwidth"`          // e.g. "45.2 GiB"
	IncludedDisk      string `json:"included_disk"`      // e.g. "25 GiB"; empty when unknown
	IncludedBandwidth string `json:"included_bandwidth"` // e.g. "100 GiB"; empty when unknown
	OverageDisk       string `json:"overage_disk"`       // e.g. "25 GiB"; empty when no overage
	OverageBandwidth  string `json:"overage_bandwidth"`  // e.g. "2 GiB"; empty when no overage
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
		monthly, err := client.queryMonthly(ctx, []string{userID}, start, end, true)
		if err != nil {
			slog.ErrorContext(ctx, "failed to query monthly metrics", "error", err, "user_id", userID)
			http.Error(w, "failed to query metrics", http.StatusBadGateway)
			return
		}

		metrics := make([]billingUsageMonthlyMetric, 0, len(monthly))
		for _, m := range monthly {
			metrics = append(metrics, billingUsageMonthlyMetric{
				Date:           m.MonthStart.UTC().Format("2006-01-02"),
				VMID:           m.VMID,
				VMName:         m.VMName,
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
// It returns per-VM usage metrics for the given billing period, enriched with
// plan limits and overage data. start and end must be RFC3339 timestamps.
func (s *Server) handleAPIBillingUsageVMs(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()

	if s.metricsdURL == "" {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return
	}

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
	periodStart, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		http.Error(w, "start must be RFC3339 (e.g. 2024-01-01T00:00:00Z)", http.StatusBadRequest)
		return
	}
	periodEnd, err := time.Parse(time.RFC3339, endStr)
	if err != nil {
		http.Error(w, "end must be RFC3339 (e.g. 2024-02-01T00:00:00Z)", http.StatusBadRequest)
		return
	}

	// Look up the user's plan to get quota and metered status.
	planRow, planErr := withRxRes1(s, ctx, (*exedb.Queries).GetActivePlanForUser, userID)
	var planID string
	var includedDisk uint64
	var includedBandwidth uint64
	if planErr == nil {
		planID = planRow.PlanID
		includedDisk = plan.IncludedDisk(planID, s.env.DefaultDisk)
		includedBandwidth = plan.IncludedBandwidth(planID)
	}

	metricsClient := newMetricsClient(s.metricsdURL)
	summaries, err := metricsClient.queryUsage(ctx, []string{userID}, periodStart, periodEnd)
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
			entry := billingUsageVMEntry{
				VMID:                   vm.VMID,
				VMName:                 vm.VMName,
				DiskProvisionedBytes:   vm.DiskProvisionedMaxBytes,
				DiskAvgBytes:           vm.DiskAvgBytes,
				BandwidthBytes:         vm.BandwidthBytes,
				CPUSeconds:             vm.CPUSeconds,
				IOReadBytes:            vm.IOReadBytes,
				IOWriteBytes:           vm.IOWriteBytes,
				DaysWithData:           vm.DaysWithData,
				IncludedDiskBytes:      includedDisk,
				IncludedBandwidthBytes: includedBandwidth,
			}
			// Disk overage is based on provisioned size beyond included.
			if includedDisk > 0 && vm.DiskProvisionedMaxBytes > int64(includedDisk) {
				entry.OverageDiskBytes = vm.DiskProvisionedMaxBytes - int64(includedDisk)
			}
			// Bandwidth overage is based on actual usage beyond included.
			if includedBandwidth > 0 && vm.BandwidthBytes > int64(includedBandwidth) {
				entry.OverageBandwidthBytes = vm.BandwidthBytes - int64(includedBandwidth)
			}
			entry.Display.DiskProvisioned = fmtBytes(uint64(vm.DiskProvisionedMaxBytes))
			entry.Display.Bandwidth = fmtBytes(uint64(vm.BandwidthBytes))
			if includedDisk > 0 {
				entry.Display.IncludedDisk = fmtBytes(includedDisk)
			}
			if includedBandwidth > 0 {
				entry.Display.IncludedBandwidth = fmtBytes(includedBandwidth)
			}
			if entry.OverageDiskBytes > 0 {
				entry.Display.OverageDisk = fmtBytes(uint64(entry.OverageDiskBytes))
			}
			if entry.OverageBandwidthBytes > 0 {
				entry.Display.OverageBandwidth = fmtBytes(uint64(entry.OverageBandwidthBytes))
			}
			vms = append(vms, entry)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(billingUsageVMsResponse{
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
		Metrics:     vms,
	})
}

// billingPeriodForUser computes the current billing period [start, end) for a user.
// It tries the Stripe subscription first (authoritative), then falls back to the
// plan anchor day, then to calendar month.
func billingPeriodForUser(ctx context.Context, s *Server, accountID string, planErr error) (time.Time, time.Time) {
	now := time.Now().UTC()

	// Try Stripe subscription period first (authoritative source).
	if accountID != "" {
		if period, err := s.billing.CurrentBillingPeriod(ctx, accountID); err == nil && period != nil {
			return period.Start, period.End
		}
	}

	if planErr != nil && !errors.Is(planErr, sql.ErrNoRows) {
		return calendarMonthPeriod(now)
	}

	// Fall back to anchor day from plan start date.
	if accountID != "" {
		accPlan, err := withRxRes1(s, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
		if err == nil && accPlan.ChangedBy != nil && *accPlan.ChangedBy == "stripe:event" {
			return anchoredMonthPeriod(now, accPlan.StartedAt.UTC().Day())
		}
	}

	return calendarMonthPeriod(now)
}

// calendarMonthPeriod returns the first and exclusive-last day of the current UTC calendar month.
func calendarMonthPeriod(now time.Time) (time.Time, time.Time) {
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	return start, end
}

// anchoredMonthPeriod returns the [start, end) billing period whose boundary falls on anchorDay
// each month. If anchorDay > the number of days in a given month it clamps to the last day.
func anchoredMonthPeriod(now time.Time, anchorDay int) (time.Time, time.Time) {
	if anchorDay < 1 {
		anchorDay = 1
	}
	// Find the most recent anchor that is <= now.
	start := clampDay(now.Year(), now.Month(), anchorDay)
	if start.After(now) {
		// This month's anchor is in the future; use last month's.
		prev := now.AddDate(0, -1, 0)
		start = clampDay(prev.Year(), prev.Month(), anchorDay)
	}
	// End is one month after start.
	nextY, nextM := start.Year(), start.Month()+1
	if nextM > 12 {
		nextM = 1
		nextY++
	}
	end := clampDay(nextY, nextM, anchorDay)
	return start, end
}

// clampDay returns the first moment of day d in the given year/month, clamped to the last
// day of the month if d exceeds the number of days.
func clampDay(year int, month time.Month, day int) time.Time {
	// time.Date normalises out-of-range values, so use the last day of month instead.
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
