package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"time"
)

type MigrationResult struct {
	OperationID string        `json:"operation_id"`
	InstanceID  string        `json:"instance_id"`
	Exelet      string        `json:"exelet"`
	SourcePool  string        `json:"source_pool"`
	TargetPool  string        `json:"target_pool"`
	State       string        `json:"state"`
	Error       string        `json:"error,omitempty"`
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at"`
	Duration    time.Duration `json:"duration_ns"`
	DurationStr string        `json:"duration"`
}

type Report struct {
	StartTime  time.Time         `json:"start_time"`
	EndTime    time.Time         `json:"end_time"`
	Elapsed    string            `json:"elapsed"`
	Total      int               `json:"total"`
	Successes  int               `json:"successes"`
	Failures   int               `json:"failures"`
	Stats      DurationStats     `json:"stats"`
	PerPool    map[string]int    `json:"per_pool"`
	PerExelet  map[string]int    `json:"per_exelet"`
	Migrations []MigrationResult `json:"migrations"`
}

type DurationStats struct {
	Min string `json:"min"`
	Max string `json:"max"`
	Avg string `json:"avg"`
	P50 string `json:"p50"`
	P95 string `json:"p95"`
}

type ReportCollector struct {
	mu        sync.Mutex
	startTime time.Time
	results   []MigrationResult
}

func NewReportCollector() *ReportCollector {
	return &ReportCollector{
		startTime: time.Now(),
	}
}

func (r *ReportCollector) Add(result MigrationResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, result)
}

func (r *ReportCollector) Results() []MigrationResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]MigrationResult, len(r.results))
	copy(out, r.results)
	return out
}

func (r *ReportCollector) Counts() (total, successes, failures int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, res := range r.results {
		total++
		if res.State == "completed" {
			successes++
		} else {
			failures++
		}
	}
	return total, successes, failures
}

func (r *ReportCollector) AvgDuration() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.results) == 0 {
		return 0
	}
	var total time.Duration
	var count int
	for _, res := range r.results {
		if res.State == "completed" {
			total += res.Duration
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return total / time.Duration(count)
}

func (r *ReportCollector) RecentCompleted(n int) []MigrationResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	var recent []MigrationResult
	for i := len(r.results) - 1; i >= 0 && len(recent) < n; i-- {
		recent = append(recent, r.results[i])
	}
	return recent
}

func (r *ReportCollector) Generate() Report {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	report := Report{
		StartTime:  r.startTime,
		EndTime:    now,
		Elapsed:    now.Sub(r.startTime).Truncate(time.Second).String(),
		Migrations: r.results,
		PerPool:    make(map[string]int),
		PerExelet:  make(map[string]int),
	}

	var successDurations []time.Duration
	for _, res := range r.results {
		report.Total++
		if res.State == "completed" {
			report.Successes++
			successDurations = append(successDurations, res.Duration)
		} else {
			report.Failures++
		}
		report.PerPool[res.TargetPool]++
		report.PerExelet[res.Exelet]++
	}

	if len(successDurations) > 0 {
		sort.Slice(successDurations, func(i, j int) bool {
			return successDurations[i] < successDurations[j]
		})
		report.Stats = computeDurationStats(successDurations)
	}

	return report
}

func computeDurationStats(durations []time.Duration) DurationStats {
	n := len(durations)
	if n == 0 {
		return DurationStats{}
	}

	var total time.Duration
	for _, d := range durations {
		total += d
	}

	return DurationStats{
		Min: durations[0].Truncate(time.Millisecond).String(),
		Max: durations[n-1].Truncate(time.Millisecond).String(),
		Avg: (total / time.Duration(n)).Truncate(time.Millisecond).String(),
		P50: durations[percentileIndex(n, 50)].Truncate(time.Millisecond).String(),
		P95: durations[percentileIndex(n, 95)].Truncate(time.Millisecond).String(),
	}
}

func percentileIndex(n, p int) int {
	idx := int(math.Ceil(float64(n)*float64(p)/100)) - 1
	if idx < 0 {
		return 0
	}
	if idx >= n {
		return n - 1
	}
	return idx
}

func (r *ReportCollector) WriteJSON(path string) error {
	report := r.Generate()
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

func (r *ReportCollector) PrintSummary() {
	report := r.Generate()
	fmt.Println()
	fmt.Println("=== Migration Report ===")
	fmt.Printf("Duration:   %s\n", report.Elapsed)
	fmt.Printf("Total:      %d\n", report.Total)
	fmt.Printf("Successes:  %d\n", report.Successes)
	fmt.Printf("Failures:   %d\n", report.Failures)
	if report.Total > 0 {
		fmt.Printf("Success %%:  %.1f%%\n", float64(report.Successes)/float64(report.Total)*100)
	}
	if report.Successes > 0 {
		fmt.Printf("Min:        %s\n", report.Stats.Min)
		fmt.Printf("Max:        %s\n", report.Stats.Max)
		fmt.Printf("Avg:        %s\n", report.Stats.Avg)
		fmt.Printf("P50:        %s\n", report.Stats.P50)
		fmt.Printf("P95:        %s\n", report.Stats.P95)
	}
	if len(report.PerPool) > 0 {
		fmt.Println("Per pool:")
		for pool, count := range report.PerPool {
			fmt.Printf("  %-12s %d\n", pool, count)
		}
	}
	if len(report.PerExelet) > 1 {
		fmt.Println("Per exelet:")
		for addr, count := range report.PerExelet {
			fmt.Printf("  %-30s %d\n", addr, count)
		}
	}
}
