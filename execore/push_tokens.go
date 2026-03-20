package execore

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"exe.dev/exedb"
)

type pushTokenRequest struct {
	Token       string `json:"token"`
	Platform    string `json:"platform"`
	Environment string `json:"environment"`
}

type pushTokenResponse struct {
	Success bool   `json:"success,omitempty"`
	Error   string `json:"error,omitempty"`
}

// handlePushTokens handles POST /api/push-tokens and DELETE /api/push-tokens.
// Only app token (iOS) authentication is accepted.
func (s *Server) handlePushTokens(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Require app token auth (iOS only).
	userID, ok := s.requireAppTokenAuth(w, r)
	if !ok {
		return
	}

	switch r.Method {
	case http.MethodPost:
		s.handleRegisterPushToken(w, r, userID)
	case http.MethodDelete:
		s.handleUnregisterPushToken(w, r, userID)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		writePushTokenError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRegisterPushToken(w http.ResponseWriter, r *http.Request, userID string) {
	var req pushTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writePushTokenError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Token == "" {
		writePushTokenError(w, "missing required field: token", http.StatusBadRequest)
		return
	}
	if req.Platform == "" {
		writePushTokenError(w, "missing required field: platform", http.StatusBadRequest)
		return
	}
	if req.Platform != "apns" {
		writePushTokenError(w, "unsupported platform", http.StatusBadRequest)
		return
	}

	switch req.Environment {
	case "production", "sandbox":
	case "":
		req.Environment = "production"
	default:
		writePushTokenError(w, "environment must be 'production' or 'sandbox'", http.StatusBadRequest)
		return
	}

	// Validate token is valid hex.
	if _, err := hex.DecodeString(req.Token); err != nil {
		writePushTokenError(w, "token must be hex-encoded", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	if err := withTx1(s, ctx, (*exedb.Queries).UpsertPushToken, exedb.UpsertPushTokenParams{
		UserID:      userID,
		Token:       req.Token,
		Platform:    req.Platform,
		Environment: req.Environment,
	}); err != nil {
		s.slog().ErrorContext(ctx, "failed to upsert push token", "error", err, "user_id", userID)
		writePushTokenError(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "push token registered", "user_id", userID, "platform", req.Platform, "environment", req.Environment)
	json.NewEncoder(w).Encode(pushTokenResponse{Success: true})
}

func (s *Server) handleUnregisterPushToken(w http.ResponseWriter, r *http.Request, userID string) {
	var req pushTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writePushTokenError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Token == "" {
		writePushTokenError(w, "missing required field: token", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	if err := withTx1(s, ctx, (*exedb.Queries).DeletePushToken, exedb.DeletePushTokenParams{
		Token:  req.Token,
		UserID: userID,
	}); err != nil {
		s.slog().ErrorContext(ctx, "failed to delete push token", "error", err, "user_id", userID)
		writePushTokenError(w, "internal error", http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(pushTokenResponse{Success: true})
}

// requireAppTokenAuth validates that the request uses an app token (exeapp_ prefix)
// and returns the authenticated user ID. Writes an error response and returns false
// if authentication fails.
func (s *Server) requireAppTokenAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	const bearerPrefix = "Bearer "
	if len(auth) < len(bearerPrefix) || !strings.EqualFold(auth[:len(bearerPrefix)], bearerPrefix) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writePushTokenError(w, "missing or invalid Authorization header", http.StatusUnauthorized)
		return "", false
	}
	token := strings.TrimSpace(auth[len(bearerPrefix):])

	// Only accept app tokens.
	if !strings.HasPrefix(token, AppTokenPrefix) {
		writePushTokenError(w, "unauthorized", http.StatusUnauthorized)
		return "", false
	}

	userID, err := s.validateAppToken(r.Context(), token)
	if err != nil {
		writePushTokenError(w, "unauthorized", http.StatusUnauthorized)
		return "", false
	}

	return userID, true
}

func writePushTokenError(w http.ResponseWriter, msg string, code int) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(pushTokenResponse{Error: msg})
}
