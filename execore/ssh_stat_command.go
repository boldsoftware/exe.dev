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
	var diskProvisionedBytes int64
	var bandwidthBytes int64
	if ss.server.metricsdURL != "" {
		client := newMetricsClient(ss.server.metricsdURL)
		summaries, err := client.queryUsage(ctx, []string{userID}, periodStart, periodEnd)
		if err == nil {
			for _, summary := range summaries {
				for _, vm := range summary.VMs {
					if vm.VMName == vmName {
						diskProvisionedBytes = vm.DiskProvisionedMaxBytes
						bandwidthBytes = vm.BandwidthBytes
					}
				}
			}
		}
	}

	// Fetch live metrics from exelet.
	var liveRow *vmUsageRow
	usageRows, usageErr := ss.fetchVMUsageForUser(ctx, userID)
	if usageErr == nil {
		for i := range usageRows {
			if usageRows[i].Name == vmName {
				liveRow = &usageRows[i]
				break
			}
		}
	}

	// Print the stat output.
	cc.Writeln("\033[1m%s\033[0m", vmName)

	// Live metrics section.
	if liveRow != nil && liveRow.Status == "running" {
		cc.Writeln("  Status:     \033[32mrunning\033[0m")
		cc.Writeln("  CPU:        %.1f%%", liveRow.CPUPercent)
		if liveRow.CPUs > 0 {
			cc.Writeln("  vCPUs:      %d", liveRow.CPUs)
		}
		if liveRow.MemCapacity > 0 {
			cc.Writeln("  Memory:     %s", fmtBytes(liveRow.MemCapacity))
		}
		diskUsed := liveRow.DiskLogicalBytes
		if diskUsed == 0 {
			diskUsed = liveRow.DiskBytes
		}
		if diskUsed > 0 {
			if liveRow.DiskCapacity > 0 {
				cc.Writeln("  Disk used:  %s / %s", fmtBytes(diskUsed), fmtBytes(liveRow.DiskCapacity))
			} else {
				cc.Writeln("  Disk used:  %s", fmtBytes(diskUsed))
			}
		}
	} else if liveRow != nil {
		cc.Writeln("  Status:     %s", liveRow.Status)
	}

	cc.Writeln("")
	cc.Writeln("  Period:     %s \u2013 %s", formatDate(periodStart), formatDate(periodEnd))

	// Disk line: show provisioned size, with extra if over included.
	cc.Writeln("  Disk:       %s", diskStatLine(diskProvisionedBytes, includedDisk))

	// Bandwidth line: show used / included.
	cc.Writeln("  Bandwidth:  %s", bandwidthStatLine(bandwidthBytes, includedBandwidth))

	cc.Writeln("")
	return nil
}

// diskStatLine formats the disk provisioned size with extra-disk indicator.
// Shows just the size when at or below included, adds extra amount when over.
func diskStatLine(provisionedBytes int64, includedBytes uint64) string {
	size := fmtBytes(uint64(provisionedBytes))
	if includedBytes == 0 {
		return size
	}
	if provisionedBytes <= int64(includedBytes) {
		return fmt.Sprintf("\033[32m%s\033[0m", size)
	}
	extraBytes := provisionedBytes - int64(includedBytes)
	return fmt.Sprintf("\033[33m%s (%s extra)\033[0m",
		size, fmtBytes(uint64(extraBytes)))
}

// bandwidthStatLine formats bandwidth usage as used / included.
func bandwidthStatLine(usedBytes int64, includedBytes uint64) string {
	used := fmtBytes(uint64(usedBytes))
	if includedBytes == 0 {
		return used
	}
	incl := fmtBytes(includedBytes)
	if usedBytes <= int64(includedBytes) {
		pct := float64(usedBytes) / float64(includedBytes) * 100
		color := "\033[32m" // green
		if pct >= 80 {
			color = "\033[33m" // yellow
		}
		return fmt.Sprintf("%s%s / %s\033[0m", color, used, incl)
	}
	extraBytes := usedBytes - int64(includedBytes)
	return fmt.Sprintf("\033[1;31m%s / %s (%s extra)\033[0m",
		used, incl, fmtBytes(uint64(extraBytes)))
}

// formatDate formats a time.Time as a short date string.
func formatDate(t time.Time) string {
	return t.UTC().Format("Jan 2, 2006")
}
