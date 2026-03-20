package exeweb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"

	"exe.dev/domz"

	"tailscale.com/net/tsaddr"
)

const (
	VMPushMaxTitleLen    = 200       // Maximum title length in characters
	VMPushMaxBodyLen     = 4 * 1024  // Maximum body length (4KB)
	VMPushMaxRequestSize = 64 * 1024 // Maximum request size (64KB)
)

// VMPushRequest is the JSON request body for sending push notifications from a VM.
type VMPushRequest struct {
	Title string            `json:"title"`
	Body  string            `json:"body"`
	Data  map[string]string `json:"data,omitempty"`
}

// VMPushResponse is the JSON response for the push endpoint.
type VMPushResponse struct {
	Success bool   `json:"success,omitempty"`
	Sent    int    `json:"sent,omitempty"`
	Error   string `json:"error,omitempty"`
}

// PushTokenData contains push token data needed by the handler.
type PushTokenData struct {
	Token       string
	Platform    string
	Environment string // "production" or "sandbox"
}

// PushSender is an interface for sending push notifications.
// This allows the APNs client to be injected/mocked.
type PushSender interface {
	Send(ctx context.Context, environment, deviceToken, title, body string, data map[string]string) error
}

// HandleVMPushSend handles POST /_/gateway/push/send from the metadata proxy.
func (ps *ProxyServer) HandleVMPushSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Content-Type", "application/json")

	// Security: only accept requests from Tailscale IPs (internal network).
	host := domz.StripPort(r.RemoteAddr)
	remoteIP, err := netip.ParseAddr(host)
	if !ps.Env.GatewayDev && (err != nil || !tsaddr.IsTailscaleIP(remoteIP)) {
		writeVMPushError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Get box name from header (set by metadata proxy).
	boxName := r.Header.Get("X-Exedev-Box")
	if boxName == "" {
		writeVMPushError(w, "missing X-Exedev-Box header", http.StatusUnauthorized)
		return
	}

	// Check if push sender is available.
	if ps.PushSender == nil {
		writeVMPushError(w, "push notifications not configured", http.StatusServiceUnavailable)
		return
	}

	// Limit request body size.
	r.Body = http.MaxBytesReader(w, r.Body, VMPushMaxRequestSize)

	// Parse request body.
	var req VMPushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeVMPushError(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		writeVMPushError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate required fields.
	if req.Title == "" {
		writeVMPushError(w, "missing required field: title", http.StatusBadRequest)
		return
	}

	// Validate size limits.
	if len(req.Title) > VMPushMaxTitleLen {
		writeVMPushError(w, "title exceeds maximum length", http.StatusBadRequest)
		return
	}
	if len(req.Body) > VMPushMaxBodyLen {
		writeVMPushError(w, "body exceeds maximum length", http.StatusBadRequest)
		return
	}

	// Look up box and owner.
	bd, exists, err := ps.Data.BoxInfo(ctx, boxName)
	if err != nil {
		ps.Lg.ErrorContext(ctx, "failed to look up box", "error", err, "box", boxName)
		writeVMPushError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !exists {
		writeVMPushError(w, "box not found", http.StatusNotFound)
		return
	}

	ud, exists, err := ps.Data.UserInfo(ctx, bd.CreatedByUserID)
	if err != nil {
		ps.Lg.ErrorContext(ctx, "failed to look up user", "error", err, "userID", bd.CreatedByUserID, "box", boxName)
		writeVMPushError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !exists {
		writeVMPushError(w, "box owner not found", http.StatusNotFound)
		return
	}

	// Get push tokens for the box owner.
	tokens, err := ps.Data.GetPushTokensByUserID(ctx, ud.UserID)
	if err != nil {
		ps.Lg.ErrorContext(ctx, "failed to get push tokens", "error", err, "userID", ud.UserID, "box", boxName)
		writeVMPushError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if len(tokens) == 0 {
		// No push tokens registered — that's fine, not an error.
		json.NewEncoder(w).Encode(VMPushResponse{Success: true, Sent: 0})
		return
	}

	// Send push notification to each token.
	sentCount := 0
	for _, tok := range tokens {
		if err := ps.PushSender.Send(ctx, tok.Environment, tok.Token, req.Title, req.Body, req.Data); err != nil {
			ps.Lg.ErrorContext(ctx, "failed to send push notification", "error", err, "userID", ud.UserID, "box", boxName)
			continue
		}
		sentCount++
	}

	ps.Lg.InfoContext(ctx, "VM push sent", "box", boxName, "userID", ud.UserID, "sent", sentCount, "total", len(tokens))

	json.NewEncoder(w).Encode(VMPushResponse{Success: true, Sent: sentCount})
}

// writeVMPushError writes a JSON error response.
func writeVMPushError(w http.ResponseWriter, msg string, code int) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(VMPushResponse{Error: msg})
}
