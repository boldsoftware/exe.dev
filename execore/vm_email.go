package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"exe.dev/exedb"
	"exe.dev/exeweb"
)

// handleVMEmailSend handles POST /_/gateway/email/send from the metadata proxy
func (s *Server) handleVMEmailSend(w http.ResponseWriter, r *http.Request) {
	s.proxyServer().HandleVMEmailSend(w, r)
}

// checkAndDebitVMEmailCredit checks if the box has email credit available and debits 1 email.
// Uses token bucket algorithm: max 50 emails, refill 10/day.
func (s *Server) checkAndDebitVMEmailCredit(ctx context.Context, boxID int64) error {
	return s.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		now := time.Now()

		credit, err := q.GetBoxEmailCredit(ctx, boxID)
		if errors.Is(err, sql.ErrNoRows) {
			// First time: create record with max credit
			if err := q.CreateBoxEmailCredit(ctx, exedb.CreateBoxEmailCreditParams{
				BoxID:           boxID,
				AvailableCredit: exeweb.VMEmailMaxCredit,
				LastRefreshAt:   now,
			}); err != nil {
				return fmt.Errorf("failed to create email credit: %w", err)
			}
			// Re-fetch to get the new record
			credit, err = q.GetBoxEmailCredit(ctx, boxID)
			if err != nil {
				return fmt.Errorf("failed to get new email credit: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("failed to get email credit: %w", err)
		}

		// Calculate refreshed credit based on time elapsed
		newCredit := calculateRefreshedVMEmailCredit(
			credit.AvailableCredit,
			exeweb.VMEmailMaxCredit,
			exeweb.VMEmailRefreshPerDay,
			credit.LastRefreshAt,
			now,
		)

		// Check if we have credit available
		if newCredit < 1.0 {
			return exeweb.ErrVMEmailRateLimited
		}

		// Debit 1 email
		newCredit -= 1.0

		// Update the record
		if err := q.UpdateBoxEmailCredit(ctx, exedb.UpdateBoxEmailCreditParams{
			AvailableCredit: newCredit,
			LastRefreshAt:   now,
			BoxID:           boxID,
		}); err != nil {
			return fmt.Errorf("failed to update email credit: %w", err)
		}

		return nil
	})
}

// calculateRefreshedVMEmailCredit computes the current available credit after applying refresh.
func calculateRefreshedVMEmailCredit(available, max, refreshPerDay float64, lastRefresh, now time.Time) float64 {
	if available >= max {
		return max
	}

	elapsed := now.Sub(lastRefresh)
	if elapsed <= 0 {
		return available
	}

	// Calculate refresh amount based on elapsed time
	days := elapsed.Hours() / 24.0
	refreshAmount := days * refreshPerDay

	newAvailable := available + refreshAmount
	if newAvailable > max {
		newAvailable = max
	}

	return newAvailable
}
