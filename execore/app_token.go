package execore

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"exe.dev/exedb"
	"exe.dev/exeweb"
	"exe.dev/stage"
)

const (
	// AppTokenPrefix distinguishes app tokens from cookies and SSH-signed tokens.
	AppTokenPrefix = exeweb.AppTokenPrefix

	// appTokenExpiry is the lifetime of an app token.
	appTokenExpiry = 180 * 24 * time.Hour

	// maxAppTokensPerUser is the maximum number of app tokens a user can have.
	// When exceeded, the oldest tokens are auto-revoked.
	maxAppTokensPerUser = 7

	// responseModeAppToken is the response_mode value that triggers the app token flow.
	responseModeAppToken = "app_token"
)

// appTokenFlowParams are the parameters carried through the auth flow
// for the app token response mode.
type appTokenFlowParams struct {
	ResponseMode string
	CallbackURI  string
}

// isAppTokenFlow reports whether the given params indicate an app token flow.
func (p appTokenFlowParams) isAppTokenFlow() bool {
	return p.ResponseMode == responseModeAppToken
}

// parseAppTokenFlowParams extracts app token flow params from a request.
func parseAppTokenFlowParams(r *http.Request) appTokenFlowParams {
	responseMode := r.URL.Query().Get("response_mode")
	if responseMode == "" {
		responseMode = r.FormValue("response_mode")
	}
	callbackURI := r.URL.Query().Get("callback_uri")
	if callbackURI == "" {
		callbackURI = r.FormValue("callback_uri")
	}
	return appTokenFlowParams{
		ResponseMode: responseMode,
		CallbackURI:  callbackURI,
	}
}

// appTokenFlowFromEmailVerification extracts app token flow params from an email verification record.
func appTokenFlowFromEmailVerification(ev exedb.EmailVerification) appTokenFlowParams {
	var rm, cb string
	if ev.ResponseMode != nil {
		rm = *ev.ResponseMode
	}
	if ev.CallbackUri != nil {
		cb = *ev.CallbackUri
	}
	return appTokenFlowParams{ResponseMode: rm, CallbackURI: cb}
}

// appTokenFlowFromOAuthState extracts app token flow params from an OAuth state record.
func appTokenFlowFromOAuthState(os exedb.OauthState) appTokenFlowParams {
	var rm, cb string
	if os.ResponseMode != nil {
		rm = *os.ResponseMode
	}
	if os.CallbackUri != nil {
		cb = *os.CallbackUri
	}
	return appTokenFlowParams{ResponseMode: rm, CallbackURI: cb}
}

// validateCallbackURI checks that the callback URI uses a custom app scheme.
// Only schemes that look like app identifiers are allowed (e.g. exedev-app://, com.example.app://).
// This blocks http, https, javascript, data, blob, and other browser-interpreted schemes.
func validateCallbackURI(uri string) error {
	if uri == "" {
		return errors.New("callback_uri is required when response_mode=app_token")
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		return fmt.Errorf("invalid callback_uri: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "" {
		return errors.New("callback_uri must include a scheme")
	}
	// Block all well-known browser schemes. Only custom app schemes are allowed.
	switch scheme {
	case "http", "https", "javascript", "data", "blob", "vbscript", "file", "ftp", "about", "ws", "wss":
		return errors.New("callback_uri must use a custom app scheme")
	}
	// Require the scheme to contain a letter (rejects e.g. "://noscheme").
	hasLetter := false
	for _, c := range scheme {
		if c >= 'a' && c <= 'z' {
			hasLetter = true
			break
		}
	}
	if !hasLetter {
		return errors.New("callback_uri must include a scheme")
	}
	return nil
}

// createAppToken generates a new app token and stores it in the database.
// If the user already has maxAppTokensPerUser tokens, the oldest are revoked.
func (s *Server) createAppToken(ctx context.Context, userID string) (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := crand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed to generate app token: %w", err)
	}
	tokenValue := AppTokenPrefix + base64.RawURLEncoding.EncodeToString(tokenBytes)

	expiresAt := time.Now().Add(appTokenExpiry)

	err := s.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		// Revoke oldest tokens if at the cap.
		existing, err := q.GetAppTokensByUserID(ctx, userID)
		if err != nil {
			return fmt.Errorf("listing app tokens: %w", err)
		}
		// existing is ordered newest-first; delete from the tail to make room.
		if len(existing) >= maxAppTokensPerUser {
			for _, tok := range existing[maxAppTokensPerUser-1:] {
				if err := q.DeleteAppToken(ctx, exedb.DeleteAppTokenParams{
					Token:  tok.Token,
					UserID: userID,
				}); err != nil {
					return fmt.Errorf("revoking old app token: %w", err)
				}
			}
		}
		return q.InsertAppToken(ctx, exedb.InsertAppTokenParams{
			Token:     tokenValue,
			UserID:    userID,
			Name:      "iOS",
			ExpiresAt: expiresAt,
		})
	})
	if err != nil {
		return "", fmt.Errorf("failed to store app token: %w", err)
	}

	return tokenValue, nil
}

// completeAuthWithAppToken creates an app token and shows the passkey prompt page.
// After passkey registration (or cancellation), the page redirects to the callback URI.
// Returns true if it handled the response (app token flow), false if the caller
// should proceed with normal cookie-based auth.
func (s *Server) completeAuthWithAppToken(w http.ResponseWriter, r *http.Request, userID string, flow appTokenFlowParams, isNewUser bool) bool {
	if !flow.isAppTokenFlow() {
		return false
	}

	ctx := r.Context()

	if err := validateCallbackURI(flow.CallbackURI); err != nil {
		s.slog().ErrorContext(ctx, "invalid callback_uri in app token flow", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	}

	tokenValue, err := s.createAppToken(context.WithoutCancel(ctx), userID)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to create app token", "error", err)
		http.Error(w, "Failed to create app token", http.StatusInternalServerError)
		return true
	}

	// Build the final callback URL with the token.
	callbackURL, err := url.Parse(flow.CallbackURI)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to parse callback_uri", "error", err)
		http.Error(w, "Invalid callback_uri", http.StatusBadRequest)
		return true
	}
	q := callbackURL.Query()
	q.Set("token", tokenValue)
	callbackURL.RawQuery = q.Encode()

	// Set an auth cookie so passkey registration works from this page.
	cookieValue, err := s.createAuthCookie(context.WithoutCancel(ctx), userID, r.Host)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to create auth cookie for app token passkey flow", "error", err)
		// Non-fatal: the page will just skip passkeys and redirect.
	}
	if cookieValue != "" {
		setExeAuthCookie(w, r, cookieValue)
	}

	// Look up user email for the page.
	var email string
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err == nil {
		email = user.Email
	}

	data := struct {
		stage.Env
		Email       string
		CallbackURL string
		IsWelcome   bool
	}{
		Env:         s.env,
		Email:       email,
		CallbackURL: callbackURL.String(),
		IsWelcome:   isNewUser,
	}
	s.renderTemplate(ctx, w, "app-token-success.html", data)
	return true
}

// validateAppToken validates an app token from a Bearer header and returns the user ID.
func (s *Server) validateAppToken(ctx context.Context, token string) (string, error) {
	if !strings.HasPrefix(token, AppTokenPrefix) {
		return "", errors.New("not an app token")
	}

	appToken, err := withRxRes1(s, ctx, (*exedb.Queries).GetAppTokenInfo, token)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("invalid app token")
	}
	if err != nil {
		return "", fmt.Errorf("database error: %w", err)
	}

	if time.Now().After(appToken.ExpiresAt) {
		return "", errors.New("app token expired")
	}

	// Check lockout
	isLockedOut, err := s.isUserLockedOut(ctx, appToken.UserID)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to check lockout status for app token", "error", err, "user_id", appToken.UserID)
		return "", errors.New("invalid app token")
	}
	if isLockedOut {
		s.slog().WarnContext(ctx, "locked out user attempted app token auth", "user_id", appToken.UserID)
		return "", errors.New("invalid app token")
	}

	// Update last_used_at asynchronously
	go func() {
		_ = withTx1(s, context.WithoutCancel(ctx), (*exedb.Queries).UpdateAppTokenLastUsed, token)
	}()

	return appToken.UserID, nil
}
