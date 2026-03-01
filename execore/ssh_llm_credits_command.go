package execore

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

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

	// Get billing account and balance
	var billingAccountID string
	var billingBalance int64
	var hasBillingAccount bool
	account, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetAccountByUserID, user.UserID)
	if err == nil {
		hasBillingAccount = true
		billingAccountID = account.ID
		balance, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetCreditBalance, account.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			cc.WriteInternalError(ctx, "sudo-exe llm-credits", err)
			return nil
		}
		billingBalance = balance
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

		billing := map[string]any{
			"has_account":    hasBillingAccount,
			"billing_status": billingStatus.BillingStatus,
		}
		if billingStatus.BillingExemption != nil {
			billing["billing_exemption"] = *billingStatus.BillingExemption
		}
		if hasBillingAccount {
			billing["account_id"] = billingAccountID
			billing["balance_microcents"] = billingBalance
			billing["balance_usd"] = fmt.Sprintf("$%.2f", float64(billingBalance)/1_000_000)
		}
		result["billing_credit"] = billing
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
		cc.Writeln("  Upgrade bonus:   %v", credit.BillingUpgradeBonusGranted == 1)
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
	cc.Writeln("\033[1;33m── Billing Credit (Stripe) ──\033[0m")
	cc.Writeln("  Billing status:  %s", cmp.Or(billingStatus.BillingStatus, "(none)"))
	if billingStatus.BillingExemption != nil {
		cc.Writeln("  Exemption:       %s", *billingStatus.BillingExemption)
	}
	if hasBillingAccount {
		cc.Writeln("  Account ID:      %s", billingAccountID)
		cc.Writeln("  Balance:         $%.2f (%d microcents)", float64(billingBalance)/1_000_000, billingBalance)
	} else {
		cc.Writeln("  (no billing account)")
	}
	cc.Writeln("")

	return nil
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
