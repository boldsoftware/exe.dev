package execore

import (
	"context"
	"flag"
	"fmt"
	"math"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

func statCommandFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("stat", flag.ContinueOnError)
	fs.String("range", "24h", "time range: 24h, 7d, or 30d")
	fs.Bool("json", false, "output in JSON format")
	return fs
}

// parseStatRange parses the range flag into hours. Accepts 24h, 7d, 30d.
func parseStatRange(s string) (int, error) {
	switch s {
	case "24h", "24H":
		return 24, nil
	case "7d", "7D":
		return 168, nil
	case "30d", "30D":
		return 720, nil
	default:
		return 0, fmt.Errorf("invalid range %q (use 24h, 7d, or 30d)", s)
	}
}

// handleStatCommand handles the "stat <vm-name>" command.
// Shows per-VM metrics (CPU, memory, disk, IO) matching the web Usage view.
func (ss *SSHServer) handleStatCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) == 0 {
		return cc.Errorf("usage: stat <vm-name> [--range=24h|7d|30d]")
	}
	vmName := cc.Args[0]
	userID := cc.User.ID

	rangeStr := cc.FlagSet.Lookup("range").Value.String()
	hours, err := parseStatRange(rangeStr)
	if err != nil {
		return cc.Errorf("%v", err)
	}

	// Verify the VM exists and belongs to this user.
	boxes, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxesForUser, userID)
	if err != nil {
		return cc.Errorf("listing VMs: %v", err)
	}
	var vmStatus string
	found := false
	for _, b := range boxes {
		if b.Name == vmName {
			vmStatus = b.Status
			found = true
			break
		}
	}
	if !found {
		return cc.Errorf("VM not found: %s", vmName)
	}

	// Fetch historical metrics from metricsd.
	if ss.server.metricsdURL == "" {
		return cc.Errorf("metrics not available")
	}

	client := newMetricsClient(ss.server.metricsdURL)
	metrics, err := client.queryVMs(ctx, []string{vmName}, hours)
	if err != nil {
		return cc.Errorf("querying metrics: %v", err)
	}

	points := computeUsageData(metrics[vmName])

	rangeLabel := map[int]string{24: "24h", 168: "7d", 720: "30d"}[hours]

	if cc.WantJSON() {
		cc.WriteJSON(statJSON{
			Name:   vmName,
			Status: vmStatus,
			Range:  rangeLabel,
			Points: points,
		})
		return nil
	}

	cc.Writeln("\033[1m%s\033[0m  %s  %s", vmName, statusColorStr(vmStatus), rangeLabel)
	cc.Writeln("\033[33m[Beta]\033[0m Metrics update periodically and may have discrepancies. For real-time data, use free, df -h, or top.")

	if len(points) == 0 {
		cc.Writeln("  No metrics data available.")
		cc.Writeln("")
		return nil
	}

	// Compute summary stats from the time series.
	last := points[len(points)-1]
	cpuNominal := last.CPUNominal

	var avgCPUPct, avgMemGB, avgDiskGB float64
	for _, p := range points {
		nom := p.CPUNominal
		if nom <= 0 {
			nom = 1
		}
		avgCPUPct += (p.CPUCores / nom) * 100
		avgMemGB += p.MemoryUsedGB
		avgDiskGB += p.DiskUsedGB
	}
	n := float64(len(points))
	avgCPUPct /= n
	avgMemGB /= n
	avgDiskGB /= n

	// Current values (last point).
	curCPUPct := 0.0
	if cpuNominal > 0 {
		curCPUPct = (last.CPUCores / cpuNominal) * 100
	}
	curIO := last.IOReadMBps + last.IOWriteMBps

	cc.Writeln("")
	cc.Writeln("  \033[2m%-10s %8s %8s\033[0m", "", "current", "avg")
	cc.Writeln("  %-10s %7s%% %7s%%", "CPU", fmtF1(curCPUPct), fmtF1(avgCPUPct))
	cc.Writeln("  %-10s %8s %8s", "RSS", fmtGBStat(last.MemoryUsedGB), fmtGBStat(avgMemGB))
	cc.Writeln("  %-10s %8s %8s", "Disk", fmtGBStat(last.DiskUsedGB), fmtGBStat(avgDiskGB))
	cc.Writeln("  %-10s %8s", "IO", fmtMBpsStat(curIO))
	cc.Writeln("")

	// Sparkline section.
	cc.Writeln("  \033[2mSPARKLINES\033[0m")
	cc.Writeln("  CPU:  %s", sparkline(extractField(points, func(p usageDataPoint) float64 {
		nom := p.CPUNominal
		if nom <= 0 {
			nom = 1
		}
		return (p.CPUCores / nom) * 100
	})))
	cc.Writeln("  RSS:  %s", sparkline(extractField(points, func(p usageDataPoint) float64 {
		return p.MemoryUsedGB
	})))
	cc.Writeln("  Disk: %s", sparkline(extractField(points, func(p usageDataPoint) float64 {
		return p.DiskUsedGB
	})))
	cc.Writeln("  IO:   %s", sparkline(extractField(points, func(p usageDataPoint) float64 {
		return p.IOReadMBps + p.IOWriteMBps
	})))
	cc.Writeln("")

	return nil
}

// statJSON is the JSON output for the stat command.
type statJSON struct {
	Name   string           `json:"name"`
	Status string           `json:"status"`
	Range  string           `json:"range"`
	Points []usageDataPoint `json:"points"`
}

// statusColorStr returns a colored status string.
func statusColorStr(status string) string {
	switch status {
	case "running":
		return "\033[32m" + status + "\033[0m"
	case "stopped":
		return "\033[2m" + status + "\033[0m"
	default:
		return status
	}
}

// fmtF1 formats a float with 1 decimal place.
func fmtF1(v float64) string {
	return fmt.Sprintf("%.1f", v)
}

// fmtGBStat formats a GB value for the stat table.
func fmtGBStat(gb float64) string {
	if gb >= 1 {
		return fmt.Sprintf("%.1f GB", gb)
	}
	mb := gb * 1024
	if mb < 1 {
		return "0 MB"
	}
	return fmt.Sprintf("%.0f MB", mb)
}

// fmtMBpsStat formats a MB/s value for the stat table.
func fmtMBpsStat(v float64) string {
	if v < 0.1 {
		return "0"
	}
	return fmt.Sprintf("%.1f MB/s", v)
}

// extractField extracts a float64 series from usage data points.
func extractField(points []usageDataPoint, fn func(usageDataPoint) float64) []float64 {
	vals := make([]float64, len(points))
	for i, p := range points {
		vals[i] = fn(p)
	}
	return vals
}

// sparkline renders a series of values as a unicode sparkline.
func sparkline(values []float64) string {
	if len(values) == 0 {
		return "\033[2m(no data)\033[0m"
	}

	// Downsample to at most 40 buckets for terminal width.
	const maxBuckets = 40
	buckets := downsample(values, maxBuckets)

	min, max := buckets[0], buckets[0]
	for _, v := range buckets {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}

	blocks := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	out := make([]rune, len(buckets))
	for i, v := range buckets {
		if max == min {
			out[i] = blocks[0]
		} else {
			idx := int(math.Round((v - min) / (max - min) * float64(len(blocks)-1)))
			if idx < 0 {
				idx = 0
			}
			if idx >= len(blocks) {
				idx = len(blocks) - 1
			}
			out[i] = blocks[idx]
		}
	}
	return string(out)
}

// downsample reduces a slice to at most n buckets by averaging.
func downsample(values []float64, n int) []float64 {
	if len(values) <= n {
		return values
	}
	buckets := make([]float64, n)
	bucketSize := float64(len(values)) / float64(n)
	for i := 0; i < n; i++ {
		start := int(float64(i) * bucketSize)
		end := int(float64(i+1) * bucketSize)
		if end > len(values) {
			end = len(values)
		}
		var sum float64
		for j := start; j < end; j++ {
			sum += values[j]
		}
		buckets[i] = sum / float64(end-start)
	}
	return buckets
}
