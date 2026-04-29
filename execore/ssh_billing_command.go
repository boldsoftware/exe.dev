package execore

import (
	"context"
	"fmt"
	"time"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// billingCommand returns the command definition for the billing command.
func (ss *SSHServer) billingCommand() *exemenu.Command {
	cmd := &exemenu.Command{
		Name:        "billing",
		Description: "View and manage your billing",
		FlagSetFunc: jsonOnlyFlags("billing"),
		Available:   ss.canSeeBilling,
		Subcommands: []*exemenu.Command{
			{
				Name:        "plan",
				Description: "Show your current plan and resource limits",
				Usage:       "billing plan",
				Handler:     ss.handleBillingPlanCommand,
				FlagSetFunc: jsonOnlyFlags("billing-plan"),
				Available:   ss.canSeeBilling,
			},
			{
				Name:        "update",
				Description: "Open Stripe billing portal to manage your subscription",
				Usage:       "billing update",
				Handler:     ss.handleBillingUpdateCommand,
				FlagSetFunc: jsonOnlyFlags("billing-update"),
				Available:   ss.canSeeBilling,
			},
			{
				Name:        "invoices",
				Description: "Show recent invoices",
				Usage:       "billing invoices",
				Handler:     ss.handleBillingInvoicesCommand,
				FlagSetFunc: jsonOnlyFlags("billing-invoices"),
				Available:   ss.canSeeBilling,
			},
			{
				Name:        "receipts",
				Description: "Show receipts for credit purchases",
				Usage:       "billing receipts",
				Handler:     ss.handleBillingReceiptsCommand,
				FlagSetFunc: jsonOnlyFlags("billing-receipts"),
				Available:   ss.canSeeBilling,
			},
		},
	}
	cmd.Handler = func(ctx context.Context, cc *exemenu.CommandContext) error {
		return cmd.Help(cc)
	}
	return cmd
}

// canSeeBilling returns true for non-team users and team billing owners.
// Regular team members should not see the billing command — billing is
// managed by the billing owner.
func (ss *SSHServer) canSeeBilling(cc *exemenu.CommandContext) bool {
	if ss.server == nil || ss.server.db == nil {
		return false
	}
	team, _ := ss.server.GetTeamForUser(context.Background(), cc.User.ID)
	if team == nil {
		return true // not on a team — always visible
	}
	return team.Role == "billing_owner"
}

// handleBillingPlanCommand shows the user's current plan, tier, and resource limits.
func (ss *SSHServer) handleBillingPlanCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	planRow, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetActivePlanForUser, cc.User.ID)
	if err != nil {
		// No active plan — show a basic message
		if cc.WantJSON() {
			cc.WriteJSON(map[string]any{"plan": nil})
			return nil
		}
		cc.Writeln("No active plan.")
		cc.Writeln("Sign up at \033[1m%s/user\033[0m", ss.server.webBaseURLNoRequest())
		return nil
	}

	base := plan.Base(planRow.PlanID)
	planName := plan.Name(base)
	paid := plan.IsPaid(base)

	tier, tierErr := plan.GetTierByID(planRow.PlanID)

	// JSON output
	if cc.WantJSON() {
		result := map[string]any{
			"plan":    planName,
			"plan_id": planRow.PlanID,
			"paid":    paid,
		}
		if tierErr == nil {
			result["tier"] = tier.Name
			result["monthly_price_cents"] = tier.MonthlyPriceCents
			result["max_cpus"] = tier.Quotas.MaxCPUs
			result["max_memory_gb"] = tier.Quotas.MaxMemory / (1024 * 1024 * 1024)
			result["max_vms"] = tier.Quotas.MaxUserVMs
			result["default_disk_gb"] = tier.Quotas.DefaultDisk / (1024 * 1024 * 1024)
			result["max_disk_gb"] = tier.Quotas.MaxDisk / (1024 * 1024 * 1024)
			result["bandwidth_gb"] = tier.Quotas.DefaultBandwidth / (1024 * 1024 * 1024)

			// Include live pool usage in JSON.
			usageRows, usageErr := ss.fetchVMUsageForUser(ctx, cc.User.ID)
			if usageErr == nil && len(usageRows) > 0 {
				var cpuUsed float64
				var memUsed uint64
				var diskProvisioned uint64
				for _, row := range usageRows {
					if row.Status == "running" {
						cpuUsed += row.CPUPercent / 100.0
						memUsed += row.DisplayMemBytes()
					}
					diskProvisioned += row.DiskCapacity
				}
				result["usage"] = map[string]any{
					"cpu_used":               cpuUsed,
					"mem_used_bytes":         memUsed,
					"disk_provisioned_bytes": diskProvisioned,
					"vm_count":               len(usageRows),
				}
			}
		}
		cc.WriteJSON(result)
		return nil
	}

	// Human-readable output
	cc.Writeln("")
	if tierErr == nil && tier.Name != "Standard" {
		cc.Writeln("  \033[1m%s Plan (%s)\033[0m", planName, tier.Name)
	} else {
		cc.Writeln("  \033[1m%s Plan\033[0m", planName)
	}

	if tierErr == nil {
		// Resource limits line
		if tier.Quotas.MaxCPUs > 0 || tier.Quotas.MaxMemory > 0 {
			cc.Writeln("  %d vCPUs · %d GB memory", tier.Quotas.MaxCPUs, tier.Quotas.MaxMemory/(1024*1024*1024))
		}

		// Secondary limits
		var parts []string
		if tier.Quotas.MaxUserVMs > 0 {
			parts = append(parts, fmt.Sprintf("%d VMs", tier.Quotas.MaxUserVMs))
		}
		if tier.Quotas.DefaultDisk > 0 {
			parts = append(parts, fmt.Sprintf("%d GB disk", tier.Quotas.DefaultDisk/(1024*1024*1024)))
		}
		if tier.Quotas.DefaultBandwidth > 0 {
			parts = append(parts, fmt.Sprintf("%d GB transfer", tier.Quotas.DefaultBandwidth/(1024*1024*1024)))
		}
		if len(parts) > 0 {
			line := "  "
			for i, p := range parts {
				if i > 0 {
					line += " · "
				}
				line += p
			}
			cc.Writeln("%s", line)
		}

		// Price
		if tier.MonthlyPriceCents > 0 {
			cc.Writeln("")
			if plan.Grants(planRow.PlanID, plan.BillingSeats) {
				cc.Writeln("  $%d/user/month", tier.MonthlyPriceCents/100)
			} else {
				cc.Writeln("  $%d/month", tier.MonthlyPriceCents/100)
			}
		}

		// Live pool utilization bars (only for plans with pool limits).
		if tier.Quotas.MaxCPUs > 0 || tier.Quotas.MaxMemory > 0 {
			ss.writeBillingPlanPoolBars(ctx, cc, tier, planRow)
		}
	}

	cc.Writeln("")
	cc.Writeln("  Manage your plan at \033[1m%s/user\033[0m", ss.server.webBaseURLNoRequest())
	cc.Writeln("")
	return nil
}

// writeBillingPlanPoolBars fetches live metrics and writes pool utilization bars.
func (ss *SSHServer) writeBillingPlanPoolBars(ctx context.Context, cc *exemenu.CommandContext, tier plan.Tier, planRow exedb.GetActivePlanForUserRow) {
	usageRows, err := ss.fetchVMUsageForUser(ctx, cc.User.ID)
	if err != nil || len(usageRows) == 0 {
		return
	}

	var cpuUsed float64
	var memUsed uint64
	var diskProvisioned uint64
	for _, row := range usageRows {
		if row.Status == "running" {
			cpuUsed += row.CPUPercent / 100.0
			memUsed += row.DisplayMemBytes()
		}
		diskProvisioned += row.DiskCapacity
	}

	cc.Writeln("")
	cc.Writeln("  \033[2mRESOURCE USAGE\033[0m")

	if tier.Quotas.MaxCPUs > 0 {
		cpuMax := float64(tier.Quotas.MaxCPUs)
		suffix := fmt.Sprintf("%.1f / %d cores", clampF64(cpuUsed, cpuMax), tier.Quotas.MaxCPUs)
		cc.Writeln("  vCPU:      %s", poolBar(cpuUsed, cpuMax, suffix))
	}
	if tier.Quotas.MaxMemory > 0 {
		cc.Writeln("  Memory:    %s", poolBarBytes(memUsed, tier.Quotas.MaxMemory, ""))
	}

	// Disk: per-VM model, sum provisioned vs sum included.
	if tier.Quotas.DefaultDisk > 0 {
		vmCount := uint64(len(usageRows))
		if vmCount == 0 {
			vmCount = 1
		}
		diskIncluded := tier.Quotas.DefaultDisk * vmCount
		var diskColor string
		if diskProvisioned > diskIncluded {
			diskColor = "\033[33m" // yellow
		} else {
			diskColor = "\033[32m" // green
		}
		cc.Writeln("  Disk:      %s%s / %s provisioned\033[0m", diskColor, fmtBytes(diskProvisioned), fmtBytes(diskIncluded))
	}

	// Bandwidth from billing period.
	if tier.Quotas.DefaultBandwidth > 0 {
		var totalBandwidth int64
		if ss.server.metricsdURL != "" {
			accountID := planRow.AccountID
			periodStart, periodEnd := billingPeriodForUser(ctx, ss.server, accountID, nil)
			client := newMetricsClient(ss.server.metricsdURL)
			summaries, mErr := client.queryUsage(ctx, []string{cc.User.ID}, periodStart, periodEnd)
			if mErr == nil {
				for _, summary := range summaries {
					for _, vm := range summary.VMs {
						totalBandwidth += vm.BandwidthBytes
					}
				}
			}
		}
		bwMax := tier.Quotas.DefaultBandwidth * uint64(len(usageRows))
		if bwMax == 0 {
			bwMax = tier.Quotas.DefaultBandwidth
		}
		cc.Writeln("  Bandwidth: %s", poolBarBytes(uint64(totalBandwidth), bwMax, "used"))
	}
}

// handleBillingInvoicesCommand shows the user's recent invoices.
func (ss *SSHServer) handleBillingInvoicesCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	account, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetAccountByUserID, cc.User.ID)
	if err != nil {
		if cc.WantJSON() {
			cc.WriteJSON(map[string]any{"invoices": []any{}})
			return nil
		}
		cc.Writeln("No billing account found.")
		return nil
	}

	upcoming, err := ss.server.billing.UpcomingInvoice(ctx, account.ID)
	if err != nil {
		ss.server.slog().WarnContext(ctx, "failed to fetch upcoming invoice", "error", err, "user_id", cc.User.ID)
	}
	invoices, err := ss.server.billing.ListInvoices(ctx, account.ID)
	if err != nil {
		ss.server.slog().WarnContext(ctx, "failed to list invoices", "error", err, "user_id", cc.User.ID)
	}

	if cc.WantJSON() {
		type jsonInvoice struct {
			Description string `json:"description"`
			Plan        string `json:"plan,omitempty"`
			Date        string `json:"date"`
			Amount      int64  `json:"amount_cents"`
			Status      string `json:"status"`
			URL         string `json:"url,omitempty"`
			PDF         string `json:"pdf,omitempty"`
		}
		var rows []jsonInvoice
		if upcoming != nil {
			rows = append(rows, jsonInvoice{
				Description: upcoming.Description,
				Plan:        upcoming.PlanName,
				Date:        upcoming.PeriodStart.Format("2006-01-02"),
				Amount:      upcoming.AmountPaid,
				Status:      "upcoming",
			})
		}
		for _, inv := range invoices {
			rows = append(rows, jsonInvoice{
				Description: inv.Description,
				Plan:        inv.PlanName,
				Date:        inv.Date.Format("2006-01-02"),
				Amount:      inv.AmountPaid,
				Status:      inv.Status,
				URL:         inv.HostedInvoiceURL,
				PDF:         inv.InvoicePDF,
			})
		}
		if rows == nil {
			rows = []jsonInvoice{}
		}
		cc.WriteJSON(map[string]any{"invoices": rows})
		return nil
	}

	if upcoming == nil && len(invoices) == 0 {
		cc.Writeln("No invoices found.")
		return nil
	}

	cc.Writeln("")
	if upcoming != nil {
		label := "Upcoming"
		if upcoming.PlanName != "" {
			label += " — " + upcoming.PlanName
		}
		cc.Writeln("  \033[1m%s\033[0m", label)
		cc.Writeln("  %s – %s · $%d.%02d",
			upcoming.PeriodStart.Format("Jan 2"),
			upcoming.PeriodEnd.Format("Jan 2, 2006"),
			upcoming.AmountPaid/100, upcoming.AmountPaid%100)
		cc.Writeln("")
	}

	for _, inv := range invoices {
		status := ""
		if inv.Status == "open" {
			status = " \033[33m(open)\033[0m"
		}
		label := inv.Date.Format("Jan 2, 2006")
		if inv.PlanName != "" {
			label += " — " + inv.PlanName
		}
		cc.Writeln("  \033[1m%s\033[0m%s", label, status)
		cc.Writeln("  $%d.%02d", inv.AmountPaid/100, inv.AmountPaid%100)
		if inv.HostedInvoiceURL != "" {
			cc.Writeln("  \033[2m%s\033[0m", inv.HostedInvoiceURL)
		}
		cc.Writeln("")
	}
	return nil
}

// handleBillingReceiptsCommand shows receipts for credit purchases.
func (ss *SSHServer) handleBillingReceiptsCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	account, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetAccountByUserID, cc.User.ID)
	if err != nil {
		if cc.WantJSON() {
			cc.WriteJSON(map[string]any{"receipts": []any{}})
			return nil
		}
		cc.Writeln("No billing account found.")
		return nil
	}

	since := time.Now().AddDate(0, -6, 0)
	receipts, err := ss.server.billing.ReceiptURLsAfter(ctx, account.ID, since)
	if err != nil {
		ss.server.slog().WarnContext(ctx, "failed to list receipts", "error", err, "user_id", cc.User.ID)
	}

	if cc.WantJSON() {
		type jsonReceipt struct {
			Date string `json:"date"`
			URL  string `json:"url"`
		}
		rows := make([]jsonReceipt, len(receipts))
		for i, r := range receipts {
			rows[i] = jsonReceipt{
				Date: r.Created.Format("2006-01-02"),
				URL:  r.URL,
			}
		}
		cc.WriteJSON(map[string]any{"receipts": rows})
		return nil
	}

	if len(receipts) == 0 {
		cc.Writeln("No credit purchase receipts found.")
		return nil
	}

	cc.Writeln("")
	for _, r := range receipts {
		cc.Writeln("  \033[1m%s\033[0m", r.Created.Format("Jan 2, 2006"))
		cc.Writeln("  \033[2m%s\033[0m", r.URL)
		cc.Writeln("")
	}
	return nil
}

// handleBillingUpdateCommand generates a magic link that authenticates the user
// and redirects to the Stripe billing portal.
func (ss *SSHServer) handleBillingUpdateCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	token := generateRegistrationToken()
	redirectURL := "/billing/update?source=exemenu"

	err := withTx1(ss.server, ctx, (*exedb.Queries).InsertEmailVerification, exedb.InsertEmailVerificationParams{
		Token:        token,
		Email:        cc.User.Email,
		UserID:       cc.User.ID,
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		InviteCodeID: nil,
		IsNewUser:    false,
		RedirectUrl:  &redirectURL,
	})
	if err != nil {
		return err
	}

	baseURL := ss.server.webBaseURLNoRequest()
	magicURL := fmt.Sprintf("%s/auth/verify?token=%s", baseURL, token)

	if cc.WantJSON() {
		cc.WriteJSON(map[string]string{"url": magicURL})
		return nil
	}

	cc.Writeln("")
	cc.Writeln("  Open this link to manage your subscription:")
	cc.Writeln("")
	cc.Writeln("  \033[1;36m%s\033[0m", magicURL)
	cc.Writeln("")
	cc.Writeln("  \033[2mExpires in 15 minutes.\033[0m")
	cc.Writeln("")
	return nil
}
