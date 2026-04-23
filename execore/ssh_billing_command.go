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
		}
		cc.WriteJSON(result)
		return nil
	}

	// Human-readable output
	cc.Writeln("")
	if tierErr == nil && tier.Name != "Default" {
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
			cc.Writeln("  $%d/month", tier.MonthlyPriceCents/100)
		}
	}

	cc.Writeln("")
	cc.Writeln("  Manage your plan at \033[1m%s/user\033[0m", ss.server.webBaseURLNoRequest())
	cc.Writeln("")
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
