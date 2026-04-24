package execore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"exe.dev/exedb"
)

// billingPeriodForUser computes the current billing period [start, end) for a user.
// It tries the Stripe subscription first (authoritative), then falls back to the
// plan anchor day, then to calendar month.
func billingPeriodForUser(ctx context.Context, s *Server, accountID string, planErr error) (time.Time, time.Time) {
	now := time.Now().UTC()

	// Try Stripe subscription period first (authoritative source).
	if accountID != "" {
		if period, err := s.billing.CurrentBillingPeriod(ctx, accountID); err == nil && period != nil {
			return period.Start, period.End
		}
	}

	if planErr != nil && !errors.Is(planErr, sql.ErrNoRows) {
		return calendarMonthPeriod(now)
	}

	// Fall back to anchor day from plan start date.
	if accountID != "" {
		accPlan, err := withRxRes1(s, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
		if err == nil && accPlan.ChangedBy != nil && *accPlan.ChangedBy == "stripe:event" {
			return anchoredMonthPeriod(now, accPlan.StartedAt.UTC().Day())
		}
	}

	return calendarMonthPeriod(now)
}

// calendarMonthPeriod returns the first and exclusive-last day of the current UTC calendar month.
func calendarMonthPeriod(now time.Time) (time.Time, time.Time) {
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	return start, end
}

// anchoredMonthPeriod returns the [start, end) billing period whose boundary falls on anchorDay
// each month. If anchorDay > the number of days in a given month it clamps to the last day.
func anchoredMonthPeriod(now time.Time, anchorDay int) (time.Time, time.Time) {
	if anchorDay < 1 {
		anchorDay = 1
	}
	// Find the most recent anchor that is <= now.
	start := clampDay(now.Year(), now.Month(), anchorDay)
	if start.After(now) {
		// This month's anchor is in the future; use last month's.
		prev := now.AddDate(0, -1, 0)
		start = clampDay(prev.Year(), prev.Month(), anchorDay)
	}
	// End is one month after start.
	nextY, nextM := start.Year(), start.Month()+1
	if nextM > 12 {
		nextM = 1
		nextY++
	}
	end := clampDay(nextY, nextM, anchorDay)
	return start, end
}

// clampDay returns the first moment of day d in the given year/month, clamped to the last
// day of the month if d exceeds the number of days.
func clampDay(year int, month time.Month, day int) time.Time {
	// time.Date normalises out-of-range values, so use the last day of month instead.
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
