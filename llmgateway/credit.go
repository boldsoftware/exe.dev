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

// A Plan defines the base credit limits and error messages for a group of users.
type Plan struct {
	// Mnemonic name of the plan
	Name string
	// Maximum LLM credit that can be refreshed up to, in USD
	MaxCredit float64
	// Rate at which credit refreshes, in USD per hour
	RefreshPerHour float64
	// User-facing error message when credit is exhausted.
	CreditExhaustedError string
}

// Base plans for each group
var (
	planHasBilling = Plan{
		Name:                 "has_billing",
		MaxCredit:            100.0,
		RefreshPerHour:       5.0,
		CreditExhaustedError: "LLM credits exhausted; credits refresh over time",
	}
	planNoBilling = Plan{
		Name:                 "no_billing",
		MaxCredit:            50.0,
		RefreshPerHour:       1.0,
		CreditExhaustedError: "LLM credits exhausted; credits refresh over time; for faster refresh, set up a subscription at https://exe.dev/take-my-money",
	}
	planFriend = Plan{
		Name:                 "friend",
		MaxCredit:            100.0,
		RefreshPerHour:       5.0,
		CreditExhaustedError: "LLM credits exhausted; credits refresh over time",
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

	// Apply any explicit overrides
	if credit != nil {
		if credit.MaxCredit != nil {
			plan.MaxCredit = *credit.MaxCredit
		}
		if credit.RefreshPerHour != nil {
			plan.RefreshPerHour = *credit.RefreshPerHour
		}
	}

	return plan, nil
}

// PlanForUser looks up and returns the plan for a user, including any overrides.
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

// CreditManager handles token bucket credit for LLM gateway access
type CreditManager struct {
	db  *sqlite.DB
	now func() time.Time
}

// NewCreditManager creates a new CreditManager
func NewCreditManager(db *sqlite.DB) *CreditManager {
	return &CreditManager{
		db:  db,
		now: time.Now,
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

// CheckAndRefreshCredit checks if the user has any credit available (after refresh)
// Returns the refreshed credit info if available, or ErrInsufficientCredit if not.
// This also updates the database with the refreshed credit amount.
func (m *CreditManager) CheckAndRefreshCredit(ctx context.Context, userID string) (*CreditInfo, error) {
	if m == nil || m.db == nil {
		return nil, nil // No credit management configured
	}
	var info *CreditInfo
	err := exedb.WithTx(m.db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		credit, err := q.GetUserLLMCredit(ctx, userID)
		if errors.Is(err, sql.ErrNoRows) {
			// New user: get their plan and initialize with plan.MaxCredit
			plan, err := planForUser(ctx, q, userID, nil)
			if err != nil {
				return err
			}
			now := m.now()
			if err := q.CreateUserLLMCreditWithInitial(ctx, exedb.CreateUserLLMCreditWithInitialParams{
				UserID:          userID,
				AvailableCredit: plan.MaxCredit,
				LastRefreshAt:   now,
			}); err != nil {
				return err
			}
			info = &CreditInfo{
				Available:      plan.MaxCredit,
				Max:            plan.MaxCredit,
				RefreshPerHour: plan.RefreshPerHour,
				LastRefresh:    now,
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

		now := m.now()
		newAvailable, newLastRefresh := CalculateRefreshedCredit(
			credit.AvailableCredit,
			plan.MaxCredit,
			plan.RefreshPerHour,
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
	if err != nil {
		return nil, err
	}

	if info.Available <= 0 {
		return info, ErrInsufficientCredit
	}

	return info, nil
}

// TopUpOnBillingUpgrade tops up a user's credit to their new plan maximum
// when they transition from no_billing to has_billing.
// If the user has no existing credit record, this is a no-op
// (their credit will be initialized at max when they first use the gateway).
func (m *CreditManager) TopUpOnBillingUpgrade(ctx context.Context, userID string) error {
	if m == nil || m.db == nil {
		return nil
	}
	return exedb.WithTx(m.db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		credit, err := q.GetUserLLMCredit(ctx, userID)
		if errors.Is(err, sql.ErrNoRows) {
			// No credit record exists; nothing to top up
			return nil
		}
		if err != nil {
			return err
		}

		plan, err := planForUser(ctx, q, userID, &credit)
		if err != nil {
			return err
		}

		// Top up to the new plan's max
		return q.UpdateUserLLMAvailableCredit(ctx, exedb.UpdateUserLLMAvailableCreditParams{
			AvailableCredit: plan.MaxCredit,
			LastRefreshAt:   m.now(),
			UserID:          userID,
		})
	})
}

// DebitCredit subtracts the given cost (in USD) from the user's credit.
// Returns the new credit info after the debit.
func (m *CreditManager) DebitCredit(ctx context.Context, userID string, costUSD float64) (*CreditInfo, error) {
	if m == nil || m.db == nil {
		return nil, nil // No credit management configured
	}
	var info *CreditInfo
	err := exedb.WithTx(m.db, ctx, func(ctx context.Context, q *exedb.Queries) error {
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

		now := m.now()
		// First apply any refresh
		newAvailable, newLastRefresh := CalculateRefreshedCredit(
			credit.AvailableCredit,
			plan.MaxCredit,
			plan.RefreshPerHour,
			credit.LastRefreshAt,
			now,
		)

		// Then subtract the cost (allow going negative)
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
