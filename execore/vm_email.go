package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"exe.dev/domz"
	"exe.dev/email"
	"exe.dev/exedb"
	"tailscale.com/net/tsaddr"
)

// VM email rate limiting constants
const (
	VMEmailMaxCredit      = 50.0       // Maximum emails in bucket (burst)
	VMEmailRefreshPerDay  = 10.0       // Emails refilled per day
	VMEmailMaxSubjectLen  = 200        // Maximum subject length in characters
	VMEmailMaxBodyLen     = 64 * 1024  // Maximum body length (64KB)
	VMEmailMaxRequestSize = 128 * 1024 // Maximum request size (128KB)
)

// vmEmailRequest is the JSON request body for sending email from a VM
type vmEmailRequest struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// vmEmailResponse is the JSON response for the email endpoint
type vmEmailResponse struct {
	Success bool   `json:"success,omitempty"`
	Error   string `json:"error,omitempty"`
}

// handleVMEmailSend handles POST /_/vm/email/send from the metadata proxy
func (s *Server) handleVMEmailSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Content-Type", "application/json")

	// Security: only accept requests from Tailscale IPs (internal network).
	// The X-Exedev-Box header is set by the metadata proxy on exelet hosts,
	// so we must verify the request comes from the internal network.
	host := domz.StripPort(r.RemoteAddr)
	remoteIP, err := netip.ParseAddr(host)
	if !s.env.GatewayDev && (err != nil || !tsaddr.IsTailscaleIP(remoteIP)) {
		writeVMEmailError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Get box name from header (set by metadata proxy)
	boxName := r.Header.Get("X-Exedev-Box")
	if boxName == "" {
		writeVMEmailError(w, "missing X-Exedev-Box header", http.StatusUnauthorized)
		return
	}

	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, VMEmailMaxRequestSize)

	// Parse request body with streaming decode
	var req vmEmailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeVMEmailError(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		writeVMEmailError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if req.To == "" {
		writeVMEmailError(w, "missing required field: to", http.StatusBadRequest)
		return
	}
	if req.Subject == "" {
		writeVMEmailError(w, "missing required field: subject", http.StatusBadRequest)
		return
	}
	if req.Body == "" {
		writeVMEmailError(w, "missing required field: body", http.StatusBadRequest)
		return
	}

	// Validate size limits
	if len(req.Subject) > VMEmailMaxSubjectLen {
		writeVMEmailError(w, fmt.Sprintf("subject exceeds maximum length of %d characters", VMEmailMaxSubjectLen), http.StatusBadRequest)
		return
	}
	// Reject CRLF in subject to prevent email header injection
	if strings.ContainsAny(req.Subject, "\r\n") {
		writeVMEmailError(w, "subject contains invalid characters", http.StatusBadRequest)
		return
	}
	if len(req.Body) > VMEmailMaxBodyLen {
		writeVMEmailError(w, fmt.Sprintf("body exceeds maximum length of %d bytes", VMEmailMaxBodyLen), http.StatusBadRequest)
		return
	}

	// Look up box and owner email
	boxInfo, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxWithOwnerEmail, boxName)
	if errors.Is(err, sql.ErrNoRows) {
		writeVMEmailError(w, "box not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to look up box", "error", err, "box", boxName)
		writeVMEmailError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Security: only allow sending to the VM owner
	if !strings.EqualFold(req.To, boxInfo.OwnerEmail) {
		writeVMEmailError(w, "can only send email to VM owner", http.StatusForbidden)
		return
	}

	// Check and apply rate limiting.
	// We debit before sending intentionally: there's no way to be perfect,
	// and we'd rather fail closed (lose a credit on send failure) than allow
	// unlimited retries on a problematic email.
	err = s.checkAndDebitVMEmailCredit(ctx, int64(boxInfo.ID))
	if errors.Is(err, errVMEmailRateLimited) {
		writeVMEmailError(w, "rate limit exceeded; emails refill at 10/day", http.StatusTooManyRequests)
		return
	}
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to check email credit", "error", err, "box", boxName)
		writeVMEmailError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Send the email
	if err := s.sendEmail(ctx, email.TypeSendFromInsideVM, boxInfo.OwnerEmail, req.Subject, req.Body); err != nil {
		s.slog().ErrorContext(ctx, "failed to send VM email", "error", err, "box", boxName, "to", boxInfo.OwnerEmail)
		writeVMEmailError(w, "failed to send email", http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "VM email sent", "box", boxName, "to", boxInfo.OwnerEmail, "subject", req.Subject)

	// Success
	json.NewEncoder(w).Encode(vmEmailResponse{Success: true})
}

var errVMEmailRateLimited = errors.New("VM email rate limited")

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
				AvailableCredit: VMEmailMaxCredit,
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
			VMEmailMaxCredit,
			VMEmailRefreshPerDay,
			credit.LastRefreshAt,
			now,
		)

		// Check if we have credit available
		if newCredit < 1.0 {
			return errVMEmailRateLimited
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

// writeVMEmailError writes a JSON error response
func writeVMEmailError(w http.ResponseWriter, msg string, code int) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(vmEmailResponse{Error: msg})
}
