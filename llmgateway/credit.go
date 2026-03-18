package llmgateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"exe.dev/exedb"
	"exe.dev/sqlite"
)

const (
	initialFreeCreditNoSubscriptionUSD = 20.0
	UpgradeBonusCreditUSD              = 100.0
	monthlyTopUpSubscribedUSD          = 20.0
)

// A Plan defines credit behavior and error messages for a group of users.
type Plan struct {
	// Mnemonic name of the plan
	Name string
	// Default ceiling or target used by policy logic, in USD.
	MaxCredit float64
	// Configured refresh rate for explicit overrides, in USD per hour.
	// Default monthly free credit does not refill during the month and sets this to 0.
	RefreshPerHour float64
	// Refresh computes the updated available credit and refresh timestamp.
	Refresh func(available float64, lastRefresh, now time.Time) (float64, time.Time)
	// User-facing error message when credit is exhausted.
	CreditExhaustedError string
}

// Base plans for each group.
// These values are used for explicit override behavior and then overridden by
// default policy constants when no explicit overrides are present.
var (
	planHasBilling = Plan{
		Name:                 "has_billing",
		MaxCredit:            100.0,
		RefreshPerHour:       5.0,
		CreditExhaustedError: "LLM credits exhausted; credits refresh over time; purchase more at https://exe.dev/user",
	}
	planNoBilling = Plan{
		Name:                 "no_billing",
		MaxCredit:            50.0,
		RefreshPerHour:       1.0,
		CreditExhaustedError: "LLM credits exhausted; credits refresh over time; purchase more at https://exe.dev/user",
	}
	planFriend = Plan{
		Name:                 "friend",
		MaxCredit:            100.0,
		RefreshPerHour:       5.0,
		CreditExhaustedError: "LLM credits exhausted; credits refresh over time; purchase more at https://exe.dev/user",
	}
)

// planForUser determines the appropriate Plan for a user based on their billing status.
// If the user has explicit overrides for max_credit or refresh_per_hour, those are applied
// on top of the base plan.
// This version takes a *exedb.Queries to be used within an existing transaction.
func planForUser(ctx context.Context, q *exedb.Queries, userID string, credit *exedb.UserLlmCredit) (Plan, error) {
	// Determine base plan from billing status
	catResult, err := q.GetUserPlanCategory(ctx, userID)
	if err != nil {
		return Plan{}, fmt.Errorf("failed to get user plan category: %w", err)
	}

	var plan Plan
	switch catResult {
	case "has_billing":
		plan = planHasBilling
	case "friend":
		plan = planFriend
	case "no_billing":
		plan = planNoBilling
	default:
		return Plan{}, fmt.Errorf("unknown plan category %q for user %s", catResult, userID)
	}

	// Apply explicit per-user overrides when configured.
	if credit != nil && (credit.MaxCredit != nil || credit.RefreshPerHour != nil) {
		if credit.MaxCredit != nil {
			plan.MaxCredit = *credit.MaxCredit
		}
		if credit.RefreshPerHour != nil {
			plan.RefreshPerHour = *credit.RefreshPerHour
		}
		plan.Refresh = func(available float64, lastRefresh, now time.Time) (float64, time.Time) {
			return calculateMonthlyCredit(available, lastRefresh, now, plan.MaxCredit)
		}
		return plan, nil
	}

	switch catResult {
	case "has_billing":
		plan.MaxCredit = monthlyTopUpSubscribedUSD
		plan.Refresh = func(available float64, lastRefresh, now time.Time) (float64, time.Time) {
			now = now.UTC()
			if !sameUTCMonth(lastRefresh, now) {
				if available < monthlyTopUpSubscribedUSD {
					return monthlyTopUpSubscribedUSD, now
				}
				return available, now
			}
			return available, lastRefresh
		}
	case "friend", "no_billing":
		plan.MaxCredit = initialFreeCreditNoSubscriptionUSD
		plan.Refresh = func(available float64, lastRefresh, now time.Time) (float64, time.Time) {
			return available, lastRefresh
		}
	default:
		return Plan{}, fmt.Errorf("unknown plan category %q for user %s", catResult, userID)
	}
	plan.RefreshPerHour = 0

	return plan, nil
}

// PlanForUser looks up and returns the plan for a user.
// This is useful for debug pages that need to display plan details.
func PlanForUser(ctx context.Context, db *sqlite.DB, userID string, credit *exedb.UserLlmCredit) (Plan, error) {
	var plan Plan
	err := exedb.WithRx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		var err error
		plan, err = planForUser(ctx, q, userID, credit)
		return err
	})
	return plan, err
}

// ErrInsufficientCredit indicates insufficient credit for an LLM request
var ErrInsufficientCredit = errors.New("insufficient LLM credit")

// CreditManager handles credit for LLM gateway access.
type CreditManager struct {
	data GatewayData
	now  func() time.Time
}

// NewCreditManager creates a new CreditManager.
// To create one using the sqlite database,
// use NewCreditManager(&DBGatewayData{db}).
func NewCreditManager(data GatewayData) *CreditManager {
	return &CreditManager{
		data: data,
		now:  time.Now,
	}
}

// CreditInfo contains the current credit status for a user
type CreditInfo struct {
	Available      float64
	Max            float64
	RefreshPerHour float64
	LastRefresh    time.Time
	Plan           Plan
}

// CalculateRefreshedCredit computes the current available credit after applying refresh
func CalculateRefreshedCredit(available, max, refreshPerHour float64, lastRefresh, now time.Time) (newAvailable float64, newLastRefresh time.Time) {
	if available >= max {
		return max, now
	}

	elapsed := now.Sub(lastRefresh)
	if elapsed <= 0 {
		return available, lastRefresh
	}

	// Calculate refresh amount based on elapsed time
	hours := elapsed.Hours()
	refreshAmount := hours * refreshPerHour

	newAvailable = available + refreshAmount
	if newAvailable > max {
		newAvailable = max
	}

	return newAvailable, now
}

func sameUTCMonth(a, b time.Time) bool {
	a = a.UTC()
	b = b.UTC()
	ay, am, _ := a.Date()
	by, bm, _ := b.Date()
	return ay == by && am == bm
}

func calculateMonthlyCredit(available float64, lastRefresh, now time.Time, maxCredit float64) (newAvailable float64, newLastRefresh time.Time) {
	now = now.UTC()
	if !sameUTCMonth(lastRefresh, now) {
		return maxCredit, now
	}
	if available > maxCredit {
		available = maxCredit
	}
	return available, lastRefresh
}

func userHasBillingCategory(ctx context.Context, q *exedb.Queries, userID string) (bool, error) {
	catResult, err := q.GetUserPlanCategory(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("failed to get user plan category: %w", err)
	}
	return catResult == "has_billing", nil
}

// CheckAndRefreshCredit checks if the user has any credit available (after refresh)
// Returns the refreshed credit info if available, or ErrInsufficientCredit if not.
// This also updates the database with the refreshed credit amount.
func (m *CreditManager) CheckAndRefreshCredit(ctx context.Context, userID string) (*CreditInfo, error) {
	if m == nil || m.data == nil {
		return nil, nil // No credit management configured
	}
	info, err := m.data.CheckAndRefreshCredit(ctx, userID, m.now())
	if err != nil {
		return nil, err
	}

	if info.Available <= 0 {
		return info, ErrInsufficientCredit
	}

	return info, nil
}

// CheckAndRefreshCreditDB is the implementation of
// [CreditManager.CheckAndRefreshCredit] when using a database.
func CheckAndRefreshCreditDB(ctx context.Context, db *sqlite.DB, userID string, now time.Time) (*CreditInfo, error) {
	var info *CreditInfo
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		credit, err := q.GetUserLLMCredit(ctx, userID)
		if errors.Is(err, sql.ErrNoRows) {
			plan, err := planForUser(ctx, q, userID, nil)
			if err != nil {
				return err
			}
			hasBilling, err := userHasBillingCategory(ctx, q, userID)
			if err != nil {
				return err
			}
			initialLastRefresh := now.UTC()
			// Deprecated: this billing_upgrade_bonus_granted path is superseded by
			// billing.GiftCredits with billing.GiftPrefixSignup. Remove once the
			// old credit path is fully migrated.
			if hasBilling {
				if err := q.GrantBillingUpgradeBonusOnce(ctx, exedb.GrantBillingUpgradeBonusOnceParams{
					UserID:          userID,
					AvailableCredit: initialFreeCreditNoSubscriptionUSD + UpgradeBonusCreditUSD,
					LastRefreshAt:   initialLastRefresh,
				}); err != nil {
					return err
				}
				credit, err = q.GetUserLLMCredit(ctx, userID)
				if err != nil {
					return err
				}
				info = &CreditInfo{
					Available:      credit.AvailableCredit,
					Max:            plan.MaxCredit,
					RefreshPerHour: plan.RefreshPerHour,
					LastRefresh:    credit.LastRefreshAt,
					Plan:           plan,
				}
				return nil
			}
			initialAvailable := plan.MaxCredit

			if err := q.CreateUserLLMCreditWithInitial(ctx, exedb.CreateUserLLMCreditWithInitialParams{
				UserID:          userID,
				AvailableCredit: initialAvailable,
				LastRefreshAt:   initialLastRefresh,
			}); err != nil {
				return err
			}
			info = &CreditInfo{
				Available:      initialAvailable,
				Max:            plan.MaxCredit,
				RefreshPerHour: plan.RefreshPerHour,
				LastRefresh:    initialLastRefresh,
				Plan:           plan,
			}
			return nil
		}
		if err != nil {
			return err
		}
		plan, err := planForUser(ctx, q, userID, &credit)
		if err != nil {
			return err
		}
		hasBilling, err := userHasBillingCategory(ctx, q, userID)
		if err != nil {
			return err
		}
		// Deprecated: this billing_upgrade_bonus_granted check is superseded by
		// billing.GiftCredits with billing.GiftPrefixSignup. Remove once the
		// old credit path is fully migrated.
		if hasBilling && credit.BillingUpgradeBonusGranted == 0 {
			if err := q.GrantBillingUpgradeBonusOnce(ctx, exedb.GrantBillingUpgradeBonusOnceParams{
				UserID:          userID,
				AvailableCredit: initialFreeCreditNoSubscriptionUSD + UpgradeBonusCreditUSD,
				LastRefreshAt:   now.UTC(),
			}); err != nil {
				return err
			}
			credit, err = q.GetUserLLMCredit(ctx, userID)
			if err != nil {
				return err
			}
		}

		newAvailable, newLastRefresh := plan.Refresh(
			credit.AvailableCredit,
			credit.LastRefreshAt,
			now,
		)

		// Update the credit if it changed
		if newAvailable != credit.AvailableCredit || !newLastRefresh.Equal(credit.LastRefreshAt) {
			if err := q.UpdateUserLLMAvailableCredit(ctx, exedb.UpdateUserLLMAvailableCreditParams{
				AvailableCredit: newAvailable,
				LastRefreshAt:   newLastRefresh,
				UserID:          userID,
			}); err != nil {
				return err
			}
		}

		info = &CreditInfo{
			Available:      newAvailable,
			Max:            plan.MaxCredit,
			RefreshPerHour: plan.RefreshPerHour,
			LastRefresh:    newLastRefresh,
			Plan:           plan,
		}
		return nil
	})
	return info, err
}

// Deprecated: TopUpOnBillingUpgrade is superseded by billing.GiftCredits with
// billing.GiftPrefixSignup. Remove once the old credit path is fully migrated.
func (m *CreditManager) TopUpOnBillingUpgrade(ctx context.Context, userID string) error {
	if m == nil || m.data == nil {
		return nil
	}
	return m.data.TopUpOnBillingUpgrade(ctx, userID, m.now())
}

// Deprecated: TopUpOnBillingUpgradeDB is superseded by billing.GiftCredits with
// billing.GiftPrefixSignup. Remove once the old credit path is fully migrated.
func TopUpOnBillingUpgradeDB(ctx context.Context, db *sqlite.DB, userID string, now time.Time) error {
	return exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.GrantBillingUpgradeBonusOnce(ctx, exedb.GrantBillingUpgradeBonusOnceParams{
			UserID:          userID,
			AvailableCredit: initialFreeCreditNoSubscriptionUSD + UpgradeBonusCreditUSD,
			LastRefreshAt:   now.UTC(),
		})
	})
}

// DebitCredit subtracts the given cost (in USD) from the user's credit.
// Returns the new credit info after the debit.
func (m *CreditManager) DebitCredit(ctx context.Context, userID string, costUSD float64) (*CreditInfo, error) {
	if m == nil || m.data == nil {
		return nil, nil // No credit management configured
	}
	return m.data.DebitCredit(ctx, userID, costUSD, m.now())
}

// DebitCreditDB is the implementation of [CreditManager.DebitCredit]
// when using a database.
func DebitCreditDB(ctx context.Context, db *sqlite.DB, userID string, costUSD float64, now time.Time) (*CreditInfo, error) {
	var info *CreditInfo
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		credit, err := q.GetUserLLMCredit(ctx, userID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no credit record for user %s", userID)
		}
		if err != nil {
			return err
		}
		plan, err := planForUser(ctx, q, userID, &credit)
		if err != nil {
			return err
		}

		newAvailable, newLastRefresh := plan.Refresh(
			credit.AvailableCredit,
			credit.LastRefreshAt,
			now,
		)
		newAvailable -= costUSD

		if err := q.DebitUserLLMCredit(ctx, exedb.DebitUserLLMCreditParams{
			AvailableCredit: newAvailable,
			TotalUsed:       costUSD,
			LastRefreshAt:   newLastRefresh,
			UserID:          userID,
		}); err != nil {
			return err
		}

		info = &CreditInfo{
			Available:      newAvailable,
			Max:            plan.MaxCredit,
			RefreshPerHour: plan.RefreshPerHour,
			LastRefresh:    newLastRefresh,
			Plan:           plan,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return info, nil
}
