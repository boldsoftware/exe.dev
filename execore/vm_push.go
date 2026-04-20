package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"

	"exe.dev/apns"
	"exe.dev/domz"
	"exe.dev/exedb"
	"exe.dev/exeweb"

	"tailscale.com/net/tsaddr"
)

// vmPushSender is an interface for sending push notifications.
// This allows the APNs client to be injected/mocked.
type vmPushSender interface {
	Send(ctx context.Context, environment, deviceToken, title, body string, data map[string]string) error
}

// handleVMPushSend handles POST /_/gateway/push/send from the metadata proxy.
func (s *Server) handleVMPushSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Content-Type", "application/json")

	// Security: only accept requests from Tailscale IPs (internal network).
	host := domz.StripPort(r.RemoteAddr)
	remoteIP, err := netip.ParseAddr(host)
	if !s.env.GatewayDev && (err != nil || !tsaddr.IsTailscaleIP(remoteIP)) {
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
	if s.vmPushSender == nil {
		writeVMPushError(w, "push notifications not configured", http.StatusServiceUnavailable)
		return
	}

	// Limit request body size.
	r.Body = http.MaxBytesReader(w, r.Body, exeweb.VMPushMaxRequestSize)

	// Parse request body.
	var req exeweb.VMPushRequest
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
	if len(req.Title) > exeweb.VMPushMaxTitleLen {
		writeVMPushError(w, "title exceeds maximum length", http.StatusBadRequest)
		return
	}
	if len(req.Body) > exeweb.VMPushMaxBodyLen {
		writeVMPushError(w, "body exceeds maximum length", http.StatusBadRequest)
		return
	}

	// Look up box and owner.
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeVMPushError(w, "box not found", http.StatusNotFound)
			return
		}
		s.slog().ErrorContext(ctx, "failed to look up box", "error", err, "box", boxName)
		writeVMPushError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	userID := box.CreatedByUserID

	// Get push tokens for the box owner.
	tokens, err := withRxRes1(s, ctx, (*exedb.Queries).GetPushTokensByUserID, userID)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to get push tokens", "error", err, "userID", userID, "box", boxName)
		writeVMPushError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if len(tokens) == 0 {
		// No push tokens registered — that's fine, not an error.
		json.NewEncoder(w).Encode(exeweb.VMPushResponse{Success: true, Sent: 0})
		return
	}

	// Send push notification to each token.
	sentCount := 0
	for _, tok := range tokens {
		if err := s.vmPushSender.Send(ctx, tok.Environment, tok.Token, req.Title, req.Body, req.Data); err != nil {
			s.slog().ErrorContext(ctx, "failed to send push notification", "error", err, "userID", userID, "box", boxName)
			continue
		}
		sentCount++
	}

	s.slog().InfoContext(ctx, "VM push sent", "box", boxName, "userID", userID, "sent", sentCount, "total", len(tokens))

	json.NewEncoder(w).Encode(exeweb.VMPushResponse{Success: true, Sent: sentCount})
}

// writeVMPushError writes a JSON error response.
func writeVMPushError(w http.ResponseWriter, msg string, code int) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(exeweb.VMPushResponse{Error: msg})
}

// apnsPushSender wraps APNs clients for production and sandbox environments
// to implement [exeweb.PushSender].
type apnsPushSender struct {
	production *apns.Client
	sandbox    *apns.Client
}

func (s *apnsPushSender) Send(ctx context.Context, environment, deviceToken, title, body string, data map[string]string) error {
	var client *apns.Client
	switch environment {
	case "sandbox":
		client = s.sandbox
	default:
		client = s.production
	}
	if client == nil {
		return fmt.Errorf("APNs %s client not configured", environment)
	}
	return client.Send(ctx, deviceToken, title, body, data)
}
