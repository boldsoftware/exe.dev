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
	"exe.dev/stage"

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

// HandleVMEmailSend handles POST /_/gateway/email/send from the metadata proxy.
//
// Note: if you make any changes here, look at
// execore.(*Server).handleVMEmailSend.
func (ps *ProxyServer) HandleVMEmailSend(w http.ResponseWriter, r *http.Request) {
	req, boxName, ok := PrepareVMEmailSend(ps.Env, w, r)
	if !ok {
		return
	}

	ctx := r.Context()

	// Look up box and owner email.
	bd, exists, err := ps.Data.BoxInfo(ctx, boxName)
	if err != nil {
		ps.Lg.ErrorContext(ctx, "failed to look up box", "error", err, "box", boxName)
		WriteVMEmailError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !exists {
		WriteVMEmailError(w, "box not found", http.StatusNotFound)
		return
	}

	ud, exists, err := ps.Data.UserInfo(ctx, bd.CreatedByUserID)
	if err != nil {
		ps.Lg.ErrorContext(ctx, "failed to look up user", "error", err, "userID", bd.CreatedByUserID, "box", boxName)
		WriteVMEmailError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !exists {
		WriteVMEmailError(w, "box owner not found", http.StatusNotFound)
		return
	}

	// Security: only allow sending to the VM owner
	if !strings.EqualFold(req.To, ud.Email) {
		WriteVMEmailError(w, "can only send email to VM owner", http.StatusForbidden)
		return
	}

	// Check and apply rate limiting.
	// We debit before sending intentionally: there's no way to be perfect,
	// and we'd rather fail closed (lose a credit on send failure) than allow
	// unlimited retries on a problematic email.
	err = ps.Data.CheckAndDebitVMEmailCredit(ctx, bd.ID)
	if errors.Is(err, ErrVMEmailRateLimited) {
		WriteVMEmailError(w, "rate limit exceeded; emails refill at 10/day", http.StatusTooManyRequests)
		return
	}
	if err != nil {
		ps.Lg.ErrorContext(ctx, "failed to check email credit", "error", err, "box", boxName)
		WriteVMEmailError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Send the email
	if err := ps.Data.SendEmail(ctx, email.SendRequest{
		Type:     email.TypeSendFromInsideVM,
		To:       ud.Email,
		Subject:  req.Subject,
		Body:     req.Body,
		UserID:   bd.CreatedByUserID,
		FromName: ps.Env.BoxSub(boxName),
		ReplyTo:  "",
	}); err != nil {
		ps.Lg.ErrorContext(ctx, "failed to send VM email", "error", err, "box", boxName, "to", ud.Email)
		WriteVMEmailError(w, "failed to send email", http.StatusInternalServerError)
		return
	}

	ps.Lg.InfoContext(ctx, "VM email sent", "box", boxName, "to", ud.Email, "subject", req.Subject)

	// Success
	json.NewEncoder(w).Encode(VMEmailResponse{Success: true})
}

// PrepareVMEmailSend prepares to send email to a VM.
// This handles POST /_/gateway/email/send from the metadata proxy.
// The bool result reports whether it is OK to continue with the send;
// if it is false an error has been written to w.
func PrepareVMEmailSend(env *stage.Env, w http.ResponseWriter, r *http.Request) (req VMEmailRequest, boxName string, ok bool) {
	w.Header().Set("Content-Type", "application/json")

	// Security: only accept requests from Tailscale IPs (internal network).
	// The X-Exedev-Box header is set by the metadata proxy on exelet hosts,
	// so we must verify the request comes from the internal network.
	host := domz.StripPort(r.RemoteAddr)
	remoteIP, err := netip.ParseAddr(host)
	if !env.GatewayDev && (err != nil || !tsaddr.IsTailscaleIP(remoteIP)) {
		WriteVMEmailError(w, "unauthorized", http.StatusUnauthorized)
		return VMEmailRequest{}, "", false
	}

	// Get box name from header (set by metadata proxy)
	boxName = r.Header.Get("X-Exedev-Box")
	if boxName == "" {
		WriteVMEmailError(w, "missing X-Exedev-Box header", http.StatusUnauthorized)
		return VMEmailRequest{}, "", false
	}

	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, VMEmailMaxRequestSize)

	// Parse request body with streaming decode
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			WriteVMEmailError(w, "request body too large", http.StatusRequestEntityTooLarge)
			return VMEmailRequest{}, "", false
		}
		WriteVMEmailError(w, "invalid JSON", http.StatusBadRequest)
		return VMEmailRequest{}, "", false
	}

	// Validate required fields
	if req.To == "" {
		WriteVMEmailError(w, "missing required field: to", http.StatusBadRequest)
		return VMEmailRequest{}, "", false
	}
	if req.Subject == "" {
		WriteVMEmailError(w, "missing required field: subject", http.StatusBadRequest)
		return VMEmailRequest{}, "", false
	}
	if req.Body == "" {
		WriteVMEmailError(w, "missing required field: body", http.StatusBadRequest)
		return VMEmailRequest{}, "", false
	}

	// Validate size limits
	if len(req.Subject) > VMEmailMaxSubjectLen {
		WriteVMEmailError(w, fmt.Sprintf("subject exceeds maximum length of %d characters", VMEmailMaxSubjectLen), http.StatusBadRequest)
		return VMEmailRequest{}, "", false
	}
	// Reject CRLF in subject to prevent email header injection
	if strings.ContainsAny(req.Subject, "\r\n") {
		WriteVMEmailError(w, "subject contains invalid characters", http.StatusBadRequest)
		return VMEmailRequest{}, "", false
	}
	if len(req.Body) > VMEmailMaxBodyLen {
		WriteVMEmailError(w, fmt.Sprintf("body exceeds maximum length of %d bytes", VMEmailMaxBodyLen), http.StatusBadRequest)
		return VMEmailRequest{}, "", false
	}

	return req, boxName, true
}

var ErrVMEmailRateLimited = errors.New("VM email rate limited")

// WriteVMEmailError writes a JSON error response
func WriteVMEmailError(w http.ResponseWriter, msg string, code int) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(VMEmailResponse{Error: msg})
}
