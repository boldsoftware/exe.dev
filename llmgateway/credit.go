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

// Default credit settings. Used when max_credit or refresh_per_hour is NULL in the database.
// These can be changed to adjust the default policy for all users without explicit overrides.
const (
	DefaultMaxCredit      = 100.0 // Maximum credit in USD
	DefaultRefreshPerHour = 10.0  // Credit refresh rate per hour in USD
)

// ErrInsufficientCredit indicates insufficient credit for an LLM request
var ErrInsufficientCredit = errors.New("insufficient LLM credit")

// EffectiveMaxCredit returns the effective max credit, using the default if nil.
func EffectiveMaxCredit(maxCredit *float64) float64 {
	if maxCredit == nil {
		return DefaultMaxCredit
	}
	return *maxCredit
}

// EffectiveRefreshPerHour returns the effective refresh rate, using the default if nil.
func EffectiveRefreshPerHour(refreshPerHour *float64) float64 {
	if refreshPerHour == nil {
		return DefaultRefreshPerHour
	}
	return *refreshPerHour
}

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
		// Ensure record exists
		if err := q.CreateUserLLMCreditIfNotExists(ctx, userID); err != nil {
			return err
		}

		credit, err := q.GetUserLLMCredit(ctx, userID)
		if err != nil {
			return err
		}

		maxCredit := EffectiveMaxCredit(credit.MaxCredit)
		refreshPerHour := EffectiveRefreshPerHour(credit.RefreshPerHour)

		now := m.now()
		newAvailable, newLastRefresh := CalculateRefreshedCredit(
			credit.AvailableCredit,
			maxCredit,
			refreshPerHour,
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
			Max:            maxCredit,
			RefreshPerHour: refreshPerHour,
			LastRefresh:    newLastRefresh,
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

		maxCredit := EffectiveMaxCredit(credit.MaxCredit)
		refreshPerHour := EffectiveRefreshPerHour(credit.RefreshPerHour)

		now := m.now()
		// First apply any refresh
		newAvailable, newLastRefresh := CalculateRefreshedCredit(
			credit.AvailableCredit,
			maxCredit,
			refreshPerHour,
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
			Max:            maxCredit,
			RefreshPerHour: refreshPerHour,
			LastRefresh:    newLastRefresh,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return info, nil
}
