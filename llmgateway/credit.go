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

const freeCreditPerUTCMonthUSD = 20.0

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
func planForUser(ctx context.Context, q *exedb.Queries, userID string, credit *exedb.UserLlmCredit, now time.Time) (Plan, error) {
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
		return plan, nil
	}

	// Default free credit is fixed to $20/month and allocated as a strict hourly bucket.
	hourlyFreeCredit := freeCreditPerUTCHour(now)
	plan.MaxCredit = hourlyFreeCredit
	plan.RefreshPerHour = hourlyFreeCredit

	return plan, nil
}

// PlanForUser looks up and returns the plan for a user.
// This is useful for debug pages that need to display plan details.
func PlanForUser(ctx context.Context, db *sqlite.DB, userID string, credit *exedb.UserLlmCredit) (Plan, error) {
	var plan Plan
	err := exedb.WithRx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		var err error
		plan, err = planForUser(ctx, q, userID, credit, time.Now())
		return err
	})
	return plan, err
}

// ErrInsufficientCredit indicates insufficient credit for an LLM request
var ErrInsufficientCredit = errors.New("insufficient LLM credit")

// CreditManager handles token bucket credit for LLM gateway access
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

func freeCreditPerUTCHour(_ time.Time) float64 {
	return freeCreditPerUTCMonthUSD / (30.0 * 24.0)
}

func hasCreditOverride(credit *exedb.UserLlmCredit) bool {
	if credit == nil {
		return false
	}
	return credit.MaxCredit != nil || credit.RefreshPerHour != nil
}

func sameUTCHour(a, b time.Time) bool {
	return a.UTC().Truncate(time.Hour).Equal(b.UTC().Truncate(time.Hour))
}

func calculateHourlyFreeCredit(available float64, lastRefresh, now time.Time) (newAvailable float64, newLastRefresh time.Time, hourlyBucket float64) {
	now = now.UTC()
	hourlyBucket = freeCreditPerUTCHour(now)

	// Strict hourly bucket: free credit does not carry between hours.
	if !sameUTCHour(lastRefresh, now) {
		available = hourlyBucket
	}
	if available < 0 {
		available = 0
	}
	if available > hourlyBucket {
		available = hourlyBucket
	}

	return available, now, hourlyBucket
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
			plan, err := planForUser(ctx, q, userID, nil, now)
			if err != nil {
				return err
			}
			initialAvailable := plan.MaxCredit
			initialLastRefresh := now.UTC()

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
		plan, err := planForUser(ctx, q, userID, &credit, now)
		if err != nil {
			return err
		}

		newAvailable := credit.AvailableCredit
		newLastRefresh := credit.LastRefreshAt
		maxCredit := plan.MaxCredit
		refreshPerHour := plan.RefreshPerHour
		if hasCreditOverride(&credit) {
			newAvailable, newLastRefresh = CalculateRefreshedCredit(
				credit.AvailableCredit,
				plan.MaxCredit,
				plan.RefreshPerHour,
				credit.LastRefreshAt,
				now,
			)
		} else {
			newAvailable, newLastRefresh, maxCredit = calculateHourlyFreeCredit(
				credit.AvailableCredit,
				credit.LastRefreshAt,
				now,
			)
			refreshPerHour = maxCredit
		}

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
			Max:            maxCredit,
			RefreshPerHour: refreshPerHour,
			LastRefresh:    newLastRefresh,
			Plan:           plan,
		}
		return nil
	})
	return info, err
}

// TopUpOnBillingUpgrade is a no-op for the hourly free-credit model.
// Free credit now comes from the current UTC hour bucket, independent of billing plan.
func (m *CreditManager) TopUpOnBillingUpgrade(ctx context.Context, userID string) error {
	if m == nil || m.data == nil {
		return nil
	}
	return m.data.TopUpOnBillingUpgrade(ctx, userID, m.now())
}

// TopUpOnBillingUpgradeDB is the implementation of
// [CreditManager.TopUpOnBillingUpgrade] when using a database.
func TopUpOnBillingUpgradeDB(ctx context.Context, db *sqlite.DB, userID string, now time.Time) error {
	return nil
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
		plan, err := planForUser(ctx, q, userID, &credit, now)
		if err != nil {
			return err
		}

		newAvailable := credit.AvailableCredit
		newLastRefresh := credit.LastRefreshAt
		maxCredit := plan.MaxCredit
		refreshPerHour := plan.RefreshPerHour
		if hasCreditOverride(&credit) {
			// Override mode uses continuous token-bucket refill semantics.
			newAvailable, newLastRefresh = CalculateRefreshedCredit(
				credit.AvailableCredit,
				plan.MaxCredit,
				plan.RefreshPerHour,
				credit.LastRefreshAt,
				now,
			)
			newAvailable -= costUSD // allow negative, matching legacy behavior
		} else {
			// Default mode uses strict hourly free allocation.
			newAvailable, newLastRefresh, maxCredit = calculateHourlyFreeCredit(
				credit.AvailableCredit,
				credit.LastRefreshAt,
				now,
			)
			refreshPerHour = maxCredit
			// Free bucket is depleted first and cannot go below zero.
			if costUSD >= newAvailable {
				newAvailable = 0
			} else {
				newAvailable -= costUSD
			}
		}

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
			Max:            maxCredit,
			RefreshPerHour: refreshPerHour,
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
