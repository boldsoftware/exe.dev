package exeweb

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"

	"exe.dev/domz"
	"exe.dev/email"

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

// VMEmailRequest is the JSON request body for sending email from a VM
type VMEmailRequest struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// VMEmailResponse is the JSON response for the email endpoint
type VMEmailResponse struct {
	Success bool   `json:"success,omitempty"`
	Error   string `json:"error,omitempty"`
}

// HandleVMEmailSend handles POST /_/gateway/email/send from the metadata proxy
func (ps *ProxyServer) HandleVMEmailSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Content-Type", "application/json")

	// Security: only accept requests from Tailscale IPs (internal network).
	// The X-Exedev-Box header is set by the metadata proxy on exelet hosts,
	// so we must verify the request comes from the internal network.
	host := domz.StripPort(r.RemoteAddr)
	remoteIP, err := netip.ParseAddr(host)
	if !ps.Env.GatewayDev && (err != nil || !tsaddr.IsTailscaleIP(remoteIP)) {
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
	var req VMEmailRequest
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

	// Look up box and owner email.
	bd, exists, err := ps.Data.BoxInfo(ctx, boxName)
	if err != nil {
		ps.Lg.ErrorContext(ctx, "failed to look up box", "error", err, "box", boxName)
		writeVMEmailError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !exists {
		writeVMEmailError(w, "box not found", http.StatusNotFound)
		return
	}

	ud, exists, err := ps.Data.UserInfo(ctx, bd.CreatedByUserID)
	if err != nil {
		ps.Lg.ErrorContext(ctx, "failed to look up user", "error", err, "userID", bd.CreatedByUserID, "box", boxName)
		writeVMEmailError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !exists {
		writeVMEmailError(w, "box owner not found", http.StatusNotFound)
		return
	}

	// Security: only allow sending to the VM owner
	if !strings.EqualFold(req.To, ud.Email) {
		writeVMEmailError(w, "can only send email to VM owner", http.StatusForbidden)
		return
	}

	// Check and apply rate limiting.
	// We debit before sending intentionally: there's no way to be perfect,
	// and we'd rather fail closed (lose a credit on send failure) than allow
	// unlimited retries on a problematic email.
	err = ps.Data.CheckAndDebitVMEmailCredit(ctx, bd.ID)
	if errors.Is(err, ErrVMEmailRateLimited) {
		writeVMEmailError(w, "rate limit exceeded; emails refill at 10/day", http.StatusTooManyRequests)
		return
	}
	if err != nil {
		ps.Lg.ErrorContext(ctx, "failed to check email credit", "error", err, "box", boxName)
		writeVMEmailError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Send the email
	if err := ps.Data.SendEmail(ctx, email.TypeSendFromInsideVM, ud.Email, req.Subject, req.Body, bd.CreatedByUserID); err != nil {
		ps.Lg.ErrorContext(ctx, "failed to send VM email", "error", err, "box", boxName, "to", ud.Email)
		writeVMEmailError(w, "failed to send email", http.StatusInternalServerError)
		return
	}

	ps.Lg.InfoContext(ctx, "VM email sent", "box", boxName, "to", ud.Email, "subject", req.Subject)

	// Success
	json.NewEncoder(w).Encode(VMEmailResponse{Success: true})
}

var ErrVMEmailRateLimited = errors.New("VM email rate limited")

// writeVMEmailError writes a JSON error response
func writeVMEmailError(w http.ResponseWriter, msg string, code int) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(VMEmailResponse{Error: msg})
}
