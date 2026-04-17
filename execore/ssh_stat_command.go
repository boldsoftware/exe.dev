package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
	"exe.dev/exemenu"

	"github.com/dustin/go-humanize"
)

// handleStatCommand handles the "stat <vm-name>" command.
// It prints a one-shot summary of disk and bandwidth usage for a named VM,
// including plan limits and overage state.
func (ss *SSHServer) handleStatCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) == 0 {
		return cc.Errorf("usage: stat <vm-name>")
	}
	vmName := cc.Args[0]
	userID := cc.User.ID

	// Verify the VM exists and belongs to this user.
	_, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            vmName,
		CreatedByUserID: userID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cc.Errorf("VM not found: %s", vmName)
		}
		return cc.Errorf("looking up VM: %v", err)
	}

	// Look up the user's plan.
	planRow, planErr := withRxRes1(ss.server, ctx, (*exedb.Queries).GetActivePlanForUser, userID)
	var planID string
	var includedDisk uint64
	var includedBandwidth uint64
	if planErr == nil {
		planID = planRow.PlanID
		includedDisk = plan.IncludedDisk(planID, ss.server.env.DefaultDisk)
		includedBandwidth = plan.IncludedBandwidth(planID)
	}

	// Compute billing period.
	var accountID string
	if planErr == nil {
		accountID = planRow.AccountID
	}
	periodStart, periodEnd := billingPeriodForUser(ctx, ss.server, accountID, planErr)

	// Fetch usage metrics for this period.
	var diskAvgBytes int64
	var bandwidthBytes int64
	if ss.server.metricsdURL != "" {
		client := newMetricsClient(ss.server.metricsdURL)
		summaries, err := client.queryUsage(ctx, []string{userID}, periodStart, periodEnd)
		if err == nil {
			for _, summary := range summaries {
				for _, vm := range summary.VMs {
					if vm.VMName == vmName {
						diskAvgBytes = vm.DiskAvgBytes
						bandwidthBytes = vm.BandwidthBytes
					}
				}
			}
		}
	}

	// Print the stat output.
	cc.Writeln("\033[1m%s\033[0m", vmName)
	cc.Writeln("  Period:    %s \u2013 %s", formatDate(periodStart), formatDate(periodEnd))
	cc.Writeln("")

	// Disk lines.
	cc.Writeln("  Included disk:      %s", diskStatLine(diskAvgBytes, includedDisk))
	if includedDisk > 0 && diskAvgBytes > int64(includedDisk) {
		overBytes := diskAvgBytes - int64(includedDisk)
		cc.Writeln("  Extra disk:         %s", humanize.IBytes(uint64(overBytes)))
	}

	// Bandwidth lines.
	cc.Writeln("  Included bandwidth: %s", bandwidthStatLine(bandwidthBytes, includedBandwidth))
	if includedBandwidth > 0 && bandwidthBytes > int64(includedBandwidth) {
		overBytes := bandwidthBytes - int64(includedBandwidth)
		cc.Writeln("  Extra bandwidth:    %s", humanize.IBytes(uint64(overBytes)))
	}

	// Overage cost.
	const (
		diskCentsPerGB      = 8
		bandwidthCentsPerGB = 7
	)
	gbBytes := int64(1024 * 1024 * 1024)
	var overageDisk, overageBW int64
	if includedDisk > 0 && diskAvgBytes > int64(includedDisk) {
		overageDisk = diskAvgBytes - int64(includedDisk)
	}
	if includedBandwidth > 0 && bandwidthBytes > int64(includedBandwidth) {
		overageBW = bandwidthBytes - int64(includedBandwidth)
	}
	totalCents := (overageDisk/gbBytes)*diskCentsPerGB + (overageBW/gbBytes)*bandwidthCentsPerGB
	if totalCents > 0 {
		cc.Writeln("")
		cc.Writeln("  \033[1;31mEstimated overage: ~$%s\033[0m",
			formatCents(totalCents))
		if overageDisk > 0 {
			cc.Writeln("    Disk:      %s over \u00b7 ~$%s",
				humanize.IBytes(uint64(overageDisk)),
				formatCents((overageDisk/gbBytes)*diskCentsPerGB))
		}
		if overageBW > 0 {
			cc.Writeln("    Bandwidth: %s over \u00b7 ~$%s",
				humanize.IBytes(uint64(overageBW)),
				formatCents((overageBW/gbBytes)*bandwidthCentsPerGB))
		}
	}

	cc.Writeln("")
	return nil
}

// diskStatLine formats the disk usage line with plan limit and overage indicator.
func diskStatLine(usedBytes int64, includedBytes uint64) string {
	used := humanize.IBytes(uint64(usedBytes))
	if includedBytes == 0 {
		return used
	}
	incl := humanize.IBytes(includedBytes)
	if usedBytes <= int64(includedBytes) {
		pct := 0.0
		if includedBytes > 0 {
			pct = float64(usedBytes) / float64(includedBytes) * 100
		}
		color := "\033[32m" // green
		if pct >= 80 {
			color = "\033[33m" // yellow
		}
		return fmt.Sprintf("%s%s / %s (%.0f%%)\033[0m", color, used, incl, pct)
	}
	overBytes := usedBytes - int64(includedBytes)
	return fmt.Sprintf("\033[1;31m%s / %s (%s over)\033[0m",
		used, incl, humanize.IBytes(uint64(overBytes)))
}

// bandwidthStatLine formats the bandwidth usage line with plan limit and overage indicator.
func bandwidthStatLine(usedBytes int64, includedBytes uint64) string {
	used := humanize.IBytes(uint64(usedBytes))
	if includedBytes == 0 {
		return used
	}
	incl := humanize.IBytes(includedBytes)
	if usedBytes <= int64(includedBytes) {
		pct := 0.0
		if includedBytes > 0 {
			pct = float64(usedBytes) / float64(includedBytes) * 100
		}
		color := "\033[32m"
		if pct >= 80 {
			color = "\033[33m"
		}
		return fmt.Sprintf("%s%s / %s (%.0f%%)\033[0m", color, used, incl, pct)
	}
	overBytes := usedBytes - int64(includedBytes)
	return fmt.Sprintf("\033[1;31m%s / %s (%s over)\033[0m",
		used, incl, humanize.IBytes(uint64(overBytes)))
}

// formatDate formats a time.Time as a short date string.
func formatDate(t time.Time) string {
	return t.UTC().Format("Jan 2, 2006")
}

// formatCents formats a cent value as a dollars-and-cents string.
func formatCents(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}
