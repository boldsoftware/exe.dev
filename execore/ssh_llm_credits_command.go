package execore

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"exe.dev/billing"
	"exe.dev/billing/entitlement"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/llmgateway"
)

func (ss *SSHServer) handleLLMCreditsCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		return cc.Errorf("usage: sudo-exe llm-credits <userid-or-email>")
	}
	query := cc.Args[0]

	// Resolve user: try user ID first, then email
	user, err := ss.resolveUserForCredits(ctx, query)
	if err != nil {
		return cc.Errorf("user not found: %s", query)
	}

	// Get LLM gateway credit
	var credit *exedb.UserLlmCredit
	c, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserLLMCredit, user.UserID)
	if err == nil {
		credit = &c
	} else if !errors.Is(err, sql.ErrNoRows) {
		cc.WriteInternalError(ctx, "sudo-exe llm-credits", err)
		return nil
	}

	// Get plan
	plan, err := llmgateway.PlanForUser(ctx, ss.server.db, user.UserID, credit)
	if err != nil {
		cc.WriteInternalError(ctx, "sudo-exe llm-credits", err)
		return nil
	}

	// Get billing account and credit state
	var billingAccountID string
	var hasBillingAccount bool
	var creditState *billing.CreditState
	var gifts []billing.GiftEntry
	account, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetAccountByUserID, user.UserID)
	if err == nil {
		hasBillingAccount = true
		billingAccountID = account.ID
		cs, err := ss.server.billing.GetCreditState(ctx, account.ID)
		if err != nil {
			cc.WriteInternalError(ctx, "sudo-exe llm-credits", err)
			return nil
		}
		creditState = cs
		gifts, err = ss.server.billing.ListGifts(ctx, account.ID)
		if err != nil {
			cc.WriteInternalError(ctx, "sudo-exe llm-credits", err)
			return nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		cc.WriteInternalError(ctx, "sudo-exe llm-credits", err)
		return nil
	}

	// Get billing status
	billingStatus, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserBillingStatus, user.UserID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		cc.WriteInternalError(ctx, "sudo-exe llm-credits", err)
		return nil
	}

	// Derive billing exemption for display from account_plans
	var billingExemption string
	if hasBillingAccount {
		if activePlan, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetActiveAccountPlan, billingAccountID); err == nil {
			billingExemption = entitlement.DeriveExemptionDisplay(&activePlan.PlanID)
		}
	}

	if cc.WantJSON() {
		result := map[string]any{
			"user_id": user.UserID,
			"email":   user.Email,
			"plan":    plan.Name,
		}
		gateway := map[string]any{
			"plan":             plan.Name,
			"max_credit_usd":   plan.MaxCredit,
			"refresh_per_hour": plan.RefreshPerHour,
		}
		if credit != nil {
			gateway["available_credit_usd"] = credit.AvailableCredit
			gateway["total_used_usd"] = credit.TotalUsed
			gateway["last_refresh_at"] = credit.LastRefreshAt.Format(time.RFC3339)
			gateway["billing_upgrade_bonus_granted"] = credit.BillingUpgradeBonusGranted == 1
			if credit.BillingUpgradeBonusGranted == 1 && hasSignupGift(gifts) {
				gateway["deprecated"] = "billing_upgrade_bonus_granted is superseded by signup gift in ledger"
			}
			if credit.MaxCredit != nil {
				gateway["max_credit_override"] = *credit.MaxCredit
			}
			if credit.RefreshPerHour != nil {
				gateway["refresh_per_hour_override"] = *credit.RefreshPerHour
			}
		} else {
			gateway["status"] = "no credit record (not yet initialized)"
		}
		result["gateway_credit"] = gateway

		billingJSON := map[string]any{
			"has_account":    hasBillingAccount,
			"billing_status": billingStatus.BillingStatus,
		}
		if billingExemption != "" {
			billingJSON["billing_exemption"] = billingExemption
		}
		if hasBillingAccount {
			billingJSON["account_id"] = billingAccountID
			billingJSON["credit_state"] = map[string]any{
				"paid_usd":  creditState.Paid.String(),
				"gift_usd":  creditState.Gift.String(),
				"used_usd":  creditState.Used.String(),
				"total_usd": creditState.Total.String(),
			}
		}
		result["billing_credit"] = billingJSON

		if hasBillingAccount {
			giftList := []map[string]any{}
			for _, g := range gifts {
				giftList = append(giftList, map[string]any{
					"amount_usd": g.Amount.String(),
					"note":       g.Note,
					"gift_id":    g.GiftID,
					"created_at": g.CreatedAt.Format(time.RFC3339),
				})
			}
			result["gifts"] = giftList
		}

		cc.WriteJSON(result)
		return nil
	}

	// Text output
	cc.Writeln("")
	cc.Writeln("\033[1mUser:\033[0m %s (%s)", user.UserID, user.Email)
	cc.Writeln("\033[1mPlan:\033[0m %s", plan.Name)
	cc.Writeln("")

	cc.Writeln("\033[1;33m── Gateway Credit (user_llm_credit) ──\033[0m")
	if credit != nil {
		cc.Writeln("  Available:       $%.2f", credit.AvailableCredit)
		cc.Writeln("  Max:             $%.2f", plan.MaxCredit)
		cc.Writeln("  Refresh/hr:      $%.2f", plan.RefreshPerHour)
		cc.Writeln("  Total used:      $%.2f", credit.TotalUsed)
		cc.Writeln("  Last refresh:    %s", credit.LastRefreshAt.Format(time.RFC3339))
		cc.Writeln("  Upgrade bonus:   %s", upgradeBonusText(credit.BillingUpgradeBonusGranted == 1, gifts))
		if credit.MaxCredit != nil {
			cc.Writeln("  Override max:    $%.2f", *credit.MaxCredit)
		}
		if credit.RefreshPerHour != nil {
			cc.Writeln("  Override rate:   $%.2f/hr", *credit.RefreshPerHour)
		}
	} else {
		cc.Writeln("  (no credit record — not yet initialized)")
	}

	cc.Writeln("")
	cc.Writeln("\033[1;33m── Gift Credits ──\033[0m")
	if hasBillingAccount && len(gifts) > 0 {
		var totalGiftMicrocents int64
		for _, g := range gifts {
			dollar, cents := g.Amount.Dollars()
			cc.Writeln("  $%d.%02d  %s  (id: %s, %s)", dollar, cents, g.Note, g.GiftID, g.CreatedAt.Format(time.RFC3339))
			totalGiftMicrocents += g.Amount.Microcents()
		}
		cc.Writeln("  ──")
		cc.Writeln("  Total gifts:     $%.2f", float64(totalGiftMicrocents)/1_000_000)
	} else {
		cc.Writeln("  No gifts")
	}

	cc.Writeln("")
	cc.Writeln("\033[1;33m── Billing Credit ──\033[0m")
	cc.Writeln("  Billing status:  %s", cmp.Or(billingStatus.BillingStatus, "(none)"))
	if billingExemption != "" {
		cc.Writeln("  Exemption:       %s", billingExemption)
	}
	if hasBillingAccount {
		cc.Writeln("  Account ID:      %s", billingAccountID)
		cc.Writeln("  Paid:            %s", creditState.Paid)
		cc.Writeln("  Gift:            %s", creditState.Gift)
		cc.Writeln("  Used:            %s", creditState.Used)
		cc.Writeln("  Total:           %s", creditState.Total)
	} else {
		cc.Writeln("  (no billing account)")
	}
	cc.Writeln("")

	return nil
}

// hasSignupGift reports whether any gift in the list has a GiftID
// starting with the signup prefix.
func hasSignupGift(gifts []billing.GiftEntry) bool {
	for _, g := range gifts {
		if strings.HasPrefix(g.GiftID, billing.GiftPrefixSignup+":") {
			return true
		}
	}
	return false
}

// upgradeBonusText returns the display string for the upgrade bonus flag.
// When the flag is set and a signup gift exists in the ledger, it appends
// a deprecation note directing the reader to the gift ledger.
func upgradeBonusText(flagSet bool, gifts []billing.GiftEntry) string {
	if !flagSet {
		return "false"
	}
	if hasSignupGift(gifts) {
		return "true (deprecated flag — see gift ledger)"
	}
	return "true"
}

func (ss *SSHServer) resolveUserForCredits(ctx context.Context, query string) (exedb.User, error) {
	// Try as user ID first
	user, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserWithDetails, query)
	if err == nil {
		return user, nil
	}

	// Try as email (canonicalized)
	email := strings.ToLower(strings.TrimSpace(query))
	user, err = withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserByEmail, &email)
	if err == nil {
		return user, nil
	}

	return exedb.User{}, fmt.Errorf("no user found for %q", query)
}
