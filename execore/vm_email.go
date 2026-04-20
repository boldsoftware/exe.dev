package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"exe.dev/email"
	"exe.dev/exedb"
	"exe.dev/exeweb"
)

// handleVMEmailSend handles POST /_/gateway/email/send from the metadata proxy.
//
// Note: if you make any changes here, look at
// exeweb.(*ProxyServer).HandleVMEmailSend.
func (s *Server) handleVMEmailSend(w http.ResponseWriter, r *http.Request) {
	req, boxName, ok := exeweb.PrepareVMEmailSend(&s.env, w, r)
	if !ok {
		return
	}

	ctx := r.Context()

	// Look up box and owner email.
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			exeweb.WriteVMEmailError(w, "box not found", http.StatusNotFound)
			return
		}
		s.slog().ErrorContext(ctx, "failed to look up box", "error", err, "box", boxName)
		exeweb.WriteVMEmailError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	userEmail, err := withRxRes1(s, ctx, (*exedb.Queries).GetEmailByUserID, box.CreatedByUserID)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to look up user", "error", err, "userID", box.CreatedByUserID, "box", boxName)
		exeweb.WriteVMEmailError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Security: only allow sending to the VM owner
	if !strings.EqualFold(req.To, userEmail) {
		exeweb.WriteVMEmailError(w, "can only send email to VM owner", http.StatusForbidden)
		return
	}

	// Check and apply rate limiting.
	// We debit before sending intentionally: there's no way to be perfect,
	// and we'd rather fail closed (lose a credit on send failure) than allow
	// unlimited retries on a problematic email.
	err = s.checkAndDebitVMEmailCredit(ctx, int64(box.ID))
	if errors.Is(err, exeweb.ErrVMEmailRateLimited) {
		exeweb.WriteVMEmailError(w, "rate limit exceeded; emails refill at 10/day", http.StatusTooManyRequests)
		return
	}
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to check email credit", "error", err, "box", boxName)
		exeweb.WriteVMEmailError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Send the email
	if err := s.sendEmail(ctx, sendEmailParams{
		emailType: email.TypeSendFromInsideVM,
		to:        userEmail,
		subject:   req.Subject,
		body:      req.Body,
		fromName:  s.env.BoxSub(boxName),
		replyTo:   "",
		attrs:     []slog.Attr{slog.String("user_id", box.CreatedByUserID)},
	}); err != nil {
		s.slog().ErrorContext(ctx, "failed to send VM email", "error", err, "box", boxName, "to", userEmail)
		exeweb.WriteVMEmailError(w, "failed to send email", http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "VM email sent", "box", boxName, "to", userEmail, "subject", req.Subject)

	// Success
	json.NewEncoder(w).Encode(exeweb.VMEmailResponse{Success: true})
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
