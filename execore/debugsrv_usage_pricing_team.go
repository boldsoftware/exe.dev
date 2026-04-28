package execore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"exe.dev/exedb"
)

func (s *Server) handleDebugUsagePricingTeam(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	teams, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllTeams)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list teams: %v", err), http.StatusInternalServerError)
		return
	}

	type teamOption struct {
		TeamID      string
		DisplayName string
		MemberCount int64
	}
	var teamOptions []teamOption
	for _, t := range teams {
		teamOptions = append(teamOptions, teamOption{
			TeamID:      t.TeamID,
			DisplayName: t.DisplayName,
			MemberCount: t.MemberCount,
		})
	}

	data := struct {
		Teams       []teamOption
		HasMetricsd bool
	}{
		Teams:       teamOptions,
		HasMetricsd: s.metricsdURL != "",
	}

	s.renderDebugTemplate(ctx, w, "usage-pricing-team.html", data)
}

type usagePricingHourBucket struct {
	Hour          string  `json:"hour"`
	CPUCores      float64 `json:"cpu_cores"`
	MemoryRSSGiB  float64 `json:"memory_rss_gib"`
	MemorySwapGiB float64 `json:"memory_swap_gib"`
	DiskGiB       float64 `json:"disk_gib"`
	CPUCostUSD    float64 `json:"cpu_cost_usd"`
	MemoryCostUSD float64 `json:"memory_cost_usd"`
	DiskCostUSD   float64 `json:"disk_cost_usd"`
	SwapCostUSD   float64 `json:"swap_cost_usd"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	VMCount       int     `json:"vm_count"`
}

// handleDebugUsagePricingTeamAPI returns JSON data for the usage pricing team page.
// GET /debug/usage-pricing-team-api?team_id=xxx&hours=24
func (s *Server) handleDebugUsagePricingTeamAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if s.metricsdURL == "" {
		http.Error(w, "metricsd not configured", http.StatusServiceUnavailable)
		return
	}

	teamID := r.URL.Query().Get("team_id")
	if teamID == "" {
		http.Error(w, "team_id is required", http.StatusBadRequest)
		return
	}

	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		var err error
		hours, err = strconv.Atoi(h)
		if err != nil || hours < 1 || hours > 744 {
			http.Error(w, "hours must be between 1 and 744", http.StatusBadRequest)
			return
		}
	}

	// Get team members to find their resource_groups (user IDs).
	members, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamMembers, teamID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get team members: %v", err), http.StatusInternalServerError)
		return
	}
	if len(members) == 0 {
		http.Error(w, "team has no members", http.StatusNotFound)
		return
	}

	resourceGroups := make([]string, len(members))
	for i, m := range members {
		resourceGroups[i] = m.UserID
	}

	end := time.Now().UTC().Truncate(time.Hour).Add(time.Hour)
	start := end.Add(-time.Duration(hours) * time.Hour)

	client := newMetricsClient(s.metricsdURL)

	queryCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	allMetrics, err := client.queryHourly(queryCtx, resourceGroups, start, end)
	if err != nil {
		slog.ErrorContext(ctx, "failed to query hourly metrics", "error", err)
		http.Error(w, fmt.Sprintf("failed to query metrics: %v", err), http.StatusBadGateway)
		return
	}

	// Pricing from /usage-pricing:
	const (
		cpuPerCorePerHour      = 0.05
		activeMemPerGiBPerHour = 0.016
		diskPerGiBPerMonth     = 0.08
		swapPerGiBPerMonth     = 0.08 // inactive memory rate
		hoursPerMonth          = 720.0
	)

	bucketMap := make(map[string]*usagePricingHourBucket)
	for _, m := range allMetrics {
		hour := m.HourStart.UTC().Format("2006-01-02T15:00:00Z")
		b, ok := bucketMap[hour]
		if !ok {
			b = &usagePricingHourBucket{Hour: hour}
			bucketMap[hour] = b
		}
		// CPUDeltaSeconds is the total CPU seconds used in this hour for this VM.
		// Convert to average cores: cpu_seconds / 3600.
		cpuCores := m.CPUDeltaSeconds / 3600.0
		b.CPUCores += cpuCores
		b.MemoryRSSGiB += float64(m.MemoryRSSMaxBytes) / (1 << 30)
		b.MemorySwapGiB += float64(m.MemorySwapMaxBytes) / (1 << 30)
		b.DiskGiB += float64(m.DiskProvisionedBytes) / (1 << 30)
		b.VMCount++
	}

	// Compute costs per hour.
	for _, b := range bucketMap {
		b.CPUCostUSD = b.CPUCores * cpuPerCorePerHour
		b.MemoryCostUSD = b.MemoryRSSGiB * activeMemPerGiBPerHour
		b.DiskCostUSD = b.DiskGiB * diskPerGiBPerMonth / hoursPerMonth
		b.SwapCostUSD = b.MemorySwapGiB * swapPerGiBPerMonth / hoursPerMonth
		b.TotalCostUSD = b.CPUCostUSD + b.MemoryCostUSD + b.DiskCostUSD + b.SwapCostUSD
	}

	// Sort by hour.
	buckets := make([]*usagePricingHourBucket, 0, len(bucketMap))
	for _, b := range bucketMap {
		buckets = append(buckets, b)
	}
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i].Hour < buckets[j].Hour
	})

	result := struct {
		Buckets []*usagePricingHourBucket `json:"buckets"`
		TeamID  string                    `json:"team_id"`
		Hours   int                       `json:"hours"`
	}{
		Buckets: buckets,
		TeamID:  teamID,
		Hours:   hours,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
