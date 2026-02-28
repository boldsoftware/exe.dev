package execore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"exe.dev/exedb"
	"exe.dev/exeweb"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	passkeyChallengeTimeout = 2 * time.Minute
	maxPasskeysPerUser      = 20
)

// webAuthnUser implements webauthn.User interface for registration
type webAuthnUser struct {
	id          string
	email       string
	credentials []webauthn.Credential
}

func (u *webAuthnUser) WebAuthnID() []byte {
	return []byte(u.id)
}

func (u *webAuthnUser) WebAuthnName() string {
	return u.email
}

func (u *webAuthnUser) WebAuthnDisplayName() string {
	return u.email
}

func (u *webAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	return u.credentials
}

// getWebAuthn returns a configured WebAuthn instance for the current environment
func (s *Server) getWebAuthn() (*webauthn.WebAuthn, error) {
	rpID := s.env.WebHost
	rpOrigins := []string{
		fmt.Sprintf("https://%s", s.env.WebHost),
	}

	// For localhost, add http origins with ports
	if s.env.WebHost == "localhost" {
		if port := s.httpPort(); port > 0 {
			rpOrigins = append(rpOrigins, fmt.Sprintf("http://localhost:%d", port))
		}
		if port := s.httpsPort(); port > 0 {
			rpOrigins = append(rpOrigins, fmt.Sprintf("https://localhost:%d", port))
		}
	}

	cfg := &webauthn.Config{
		RPDisplayName: "EXE.DEV",
		RPID:          rpID,
		RPOrigins:     rpOrigins,
	}

	return webauthn.New(cfg)
}

// PasskeyInfo represents passkey information for display in templates
type PasskeyInfo struct {
	ID         int64
	Name       string
	CreatedAt  string
	LastUsedAt string
}

// handlePasskeyRegisterStart begins the WebAuthn registration ceremony
func (s *Server) handlePasskeyRegisterStart(w http.ResponseWriter, r *http.Request, userID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Get user info
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to get user for passkey registration", "error", err)
		http.Error(w, "Failed to get user", http.StatusInternalServerError)
		return
	}

	// Get existing credentials for this user (to exclude them)
	existingPasskeys, err := withRxRes1(s, ctx, (*exedb.Queries).GetPasskeysByUserID, userID)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to get existing passkeys", "error", err)
		http.Error(w, "Failed to get existing passkeys", http.StatusInternalServerError)
		return
	}

	if len(existingPasskeys) >= maxPasskeysPerUser {
		http.Error(w, "Maximum number of passkeys reached", http.StatusBadRequest)
		return
	}

	var credentials []webauthn.Credential
	for _, pk := range existingPasskeys {
		credentials = append(credentials, webauthn.Credential{
			ID:        pk.CredentialID,
			PublicKey: pk.PublicKey,
		})
	}

	webAuthnUser := &webAuthnUser{
		id:          userID,
		email:       user.Email,
		credentials: credentials,
	}

	wa, err := s.getWebAuthn()
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to create WebAuthn instance", "error", err)
		http.Error(w, "WebAuthn configuration error", http.StatusInternalServerError)
		return
	}

	options, session, err := wa.BeginRegistration(webAuthnUser,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationPreferred,
		}),
	)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to begin registration", "error", err)
		http.Error(w, "Failed to begin registration", http.StatusInternalServerError)
		return
	}

	// Store the challenge in the database
	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		// Clean up expired challenges first
		if err := queries.CleanupExpiredPasskeyChallenges(ctx, time.Now()); err != nil {
			s.slog().WarnContext(ctx, "Failed to cleanup expired challenges", "error", err)
		}

		sessionData, err := json.Marshal(session)
		if err != nil {
			return fmt.Errorf("failed to marshal session: %w", err)
		}

		return queries.InsertPasskeyChallenge(ctx, exedb.InsertPasskeyChallengeParams{
			Challenge:   session.Challenge,
			SessionData: sessionData,
			UserID:      &userID,
			ExpiresAt:   time.Now().Add(passkeyChallengeTimeout),
		})
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to store challenge", "error", err)
		http.Error(w, "Failed to store challenge", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(options)
}

// handlePasskeyRegisterFinish completes the WebAuthn registration ceremony
func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request, userID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Parse the passkey name from query params
	passkeyName := r.URL.Query().Get("name")
	if passkeyName == "" {
		passkeyName = "Passkey"
	}

	// Parse the credential creation response to extract the challenge
	parsedResponse, err := protocol.ParseCredentialCreationResponse(r)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to parse credential response", "error", err)
		http.Error(w, "Failed to parse credential", http.StatusBadRequest)
		return
	}

	// Get user info
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to get user for passkey registration finish", "error", err)
		http.Error(w, "Failed to get user", http.StatusInternalServerError)
		return
	}

	// Get existing credentials
	existingPasskeys, err := withRxRes1(s, ctx, (*exedb.Queries).GetPasskeysByUserID, userID)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to get existing passkeys", "error", err)
		http.Error(w, "Failed to get existing passkeys", http.StatusInternalServerError)
		return
	}

	var credentials []webauthn.Credential
	for _, pk := range existingPasskeys {
		credentials = append(credentials, webauthn.Credential{
			ID:        pk.CredentialID,
			PublicKey: pk.PublicKey,
		})
	}

	webAuthnUser := &webAuthnUser{
		id:          userID,
		email:       user.Email,
		credentials: credentials,
	}

	// Find the session challenge that matches the response
	var session webauthn.SessionData
	challengeStr := parsedResponse.Response.CollectedClientData.Challenge
	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		challenge, err := queries.GetPasskeyChallenge(ctx, challengeStr)
		if err != nil {
			return fmt.Errorf("challenge lookup failed: %w", err)
		}

		// Check expiration in Go to avoid timezone issues
		if time.Now().After(challenge.ExpiresAt) {
			return errors.New("challenge expired")
		}

		// Verify the challenge belongs to this user
		if challenge.UserID == nil || *challenge.UserID != userID {
			return errors.New("challenge user mismatch")
		}

		if err := json.Unmarshal(challenge.SessionData, &session); err != nil {
			return fmt.Errorf("session unmarshal failed: %w", err)
		}

		// Delete the challenge
		return queries.DeletePasskeyChallenge(ctx, challengeStr)
	})
	if err != nil {
		s.slog().InfoContext(ctx, "Failed to get challenge", "error", err)
		http.Error(w, "Registration challenge not found or expired", http.StatusBadRequest)
		return
	}

	wa, err := s.getWebAuthn()
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to create WebAuthn instance", "error", err)
		http.Error(w, "WebAuthn configuration error", http.StatusInternalServerError)
		return
	}

	credential, err := wa.CreateCredential(webAuthnUser, session, parsedResponse)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to finish registration", "error", err)
		http.Error(w, "Failed to verify registration", http.StatusBadRequest)
		return
	}

	// Store the credential in the database
	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var flags int64
		if credential.Flags.BackupEligible {
			flags |= 0x2
		}
		if credential.Flags.BackupState {
			flags |= 0x1
		}
		return queries.InsertPasskey(ctx, exedb.InsertPasskeyParams{
			UserID:       userID,
			CredentialID: credential.ID,
			PublicKey:    credential.PublicKey,
			SignCount:    int64(credential.Authenticator.SignCount),
			Aaguid:       credential.Authenticator.AAGUID,
			Name:         passkeyName,
			Flags:        flags,
		})
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to store passkey", "error", err)
		http.Error(w, "Failed to store passkey", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handlePasskeyLoginStart begins the WebAuthn authentication ceremony
func (s *Server) handlePasskeyLoginStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	wa, err := s.getWebAuthn()
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to create WebAuthn instance", "error", err)
		http.Error(w, "WebAuthn configuration error", http.StatusInternalServerError)
		return
	}

	// For discoverable credentials (passkey login without username),
	// we don't specify any allowed credentials
	options, session, err := wa.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationPreferred),
	)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to begin login", "error", err)
		http.Error(w, "Failed to begin login", http.StatusInternalServerError)
		return
	}

	// Store the challenge in the database (no user ID for login)
	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		// Clean up expired challenges first
		if err := queries.CleanupExpiredPasskeyChallenges(ctx, time.Now()); err != nil {
			s.slog().WarnContext(ctx, "Failed to cleanup expired challenges", "error", err)
		}

		sessionData, err := json.Marshal(session)
		if err != nil {
			return fmt.Errorf("failed to marshal session: %w", err)
		}

		return queries.InsertPasskeyChallenge(ctx, exedb.InsertPasskeyChallengeParams{
			Challenge:   session.Challenge,
			SessionData: sessionData,
			UserID:      nil, // No user ID for login
			ExpiresAt:   time.Now().Add(passkeyChallengeTimeout),
		})
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to store challenge", "error", err)
		http.Error(w, "Failed to store challenge", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(options)
}

// handlePasskeyLoginFinish completes the WebAuthn authentication ceremony
func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Parse the credential assertion
	parsedResponse, err := protocol.ParseCredentialRequestResponse(r)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to parse credential response", "error", err)
		http.Error(w, "Failed to parse credential", http.StatusBadRequest)
		return
	}

	// Look up the credential to find the user
	passkey, err := withRxRes1(s, ctx, (*exedb.Queries).GetPasskeyByCredentialID, parsedResponse.RawID)
	if errors.Is(err, sql.ErrNoRows) {
		s.slog().WarnContext(ctx, "Passkey not found", "credential_id", base64.URLEncoding.EncodeToString(parsedResponse.RawID))
		http.Error(w, "This passkey is no longer registered. It may have been deleted.", http.StatusUnauthorized)
		return
	}
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to look up passkey", "error", err)
		http.Error(w, "Failed to look up passkey", http.StatusInternalServerError)
		return
	}

	// Get user info
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, passkey.UserID)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to get user", "error", err)
		http.Error(w, "Failed to get user", http.StatusInternalServerError)
		return
	}

	webAuthnUser := &webAuthnUser{
		id:    passkey.UserID,
		email: user.Email,
		credentials: []webauthn.Credential{
			{
				ID:        passkey.CredentialID,
				PublicKey: passkey.PublicKey,
				Authenticator: webauthn.Authenticator{
					AAGUID:    passkey.Aaguid,
					SignCount: uint32(passkey.SignCount),
				},
				Flags: webauthn.CredentialFlags{
					BackupEligible: passkey.Flags&0x2 != 0,
					BackupState:    passkey.Flags&0x1 != 0,
				},
			},
		},
	}

	// Find the session challenge
	var session webauthn.SessionData
	challengeStr := parsedResponse.Response.CollectedClientData.Challenge
	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		challenge, err := queries.GetPasskeyChallenge(ctx, challengeStr)
		if err != nil {
			return fmt.Errorf("challenge lookup failed: %w", err)
		}

		// Check expiration in Go to avoid timezone issues
		if time.Now().After(challenge.ExpiresAt) {
			return errors.New("challenge expired")
		}

		if err := json.Unmarshal(challenge.SessionData, &session); err != nil {
			return fmt.Errorf("failed to unmarshal session: %w", err)
		}

		return nil
	})
	if err != nil {
		s.slog().InfoContext(ctx, "Failed to find challenge", "error", err)
		http.Error(w, "Login challenge not found or expired", http.StatusBadRequest)
		return
	}

	wa, err := s.getWebAuthn()
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to create WebAuthn instance", "error", err)
		http.Error(w, "WebAuthn configuration error", http.StatusInternalServerError)
		return
	}

	// Validate the credential
	credential, err := wa.ValidateDiscoverableLogin(
		func(rawID, userHandle []byte) (webauthn.User, error) {
			return webAuthnUser, nil
		},
		session,
		parsedResponse,
	)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to validate login", "error", err)
		http.Error(w, "Failed to validate login", http.StatusUnauthorized)
		return
	}

	// Delete the used challenge and update sign count
	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		if err := queries.DeletePasskeyChallenge(ctx, challengeStr); err != nil {
			s.slog().WarnContext(ctx, "Failed to delete challenge", "error", err)
		}
		return queries.UpdatePasskeySignCount(ctx, exedb.UpdatePasskeySignCountParams{
			SignCount:    int64(credential.Authenticator.SignCount),
			CredentialID: passkey.CredentialID,
			UserID:       passkey.UserID,
		})
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to update passkey", "error", err)
		// Don't fail the login for this
	}

	// Check for app token flow (iOS/native app authentication).
	// For passkey login, the flow params come from query parameters set by the login page JS.
	flow := parseAppTokenFlowParams(r)
	if flow.isAppTokenFlow() {
		if err := validateCallbackURI(flow.CallbackURI); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tokenValue, err := s.createAppToken(context.WithoutCancel(ctx), passkey.UserID)
		if err != nil {
			s.slog().ErrorContext(ctx, "failed to create app token during passkey login", "error", err)
			http.Error(w, "Failed to create app token", http.StatusInternalServerError)
			return
		}
		callbackURL, err := url.Parse(flow.CallbackURI)
		if err != nil {
			s.slog().ErrorContext(ctx, "failed to parse callback_uri in passkey login", "error", err, "callback_uri", flow.CallbackURI)
			http.Error(w, "Invalid callback_uri", http.StatusBadRequest)
			return
		}
		q := callbackURL.Query()
		q.Set("token", tokenValue)
		callbackURL.RawQuery = q.Encode()

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok", "redirect": callbackURL.String()}); err != nil {
			s.slog().ErrorContext(ctx, "failed to encode passkey app token response", "error", err)
		}
		return
	}

	// Create an auth cookie for the user
	cookieValue, err := s.createAuthCookie(ctx, passkey.UserID, r.Host)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to create auth cookie", "error", err)
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	// Set the cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "exe-auth",
		Value:    cookieValue,
		Path:     "/",
		MaxAge:   30 * 24 * 60 * 60, // 30 days
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})

	// Check for redirect_to parameter (used by login with exe flow)
	// Validate to prevent open redirect attacks
	redirectTo := r.URL.Query().Get("redirect_to")
	if redirectTo == "" || !exeweb.IsValidRedirectURL(redirectTo) {
		redirectTo = "/"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "redirect": redirectTo})
}

// handlePasskeyDelete deletes a passkey for the authenticated user
func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request, userID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Parse form data
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	passkeyIDStr := r.FormValue("id")
	if passkeyIDStr == "" {
		http.Error(w, "Missing passkey ID", http.StatusBadRequest)
		return
	}

	passkeyID, err := strconv.ParseInt(passkeyIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid passkey ID", http.StatusBadRequest)
		return
	}

	// Delete the passkey (ensuring it belongs to this user)
	err = withTx1(s, ctx, (*exedb.Queries).DeletePasskey, exedb.DeletePasskeyParams{
		ID:     passkeyID,
		UserID: userID,
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to delete passkey", "error", err)
		http.Error(w, "Failed to delete passkey", http.StatusInternalServerError)
		return
	}

	// Redirect back to user profile
	http.Redirect(w, r, "/user", http.StatusSeeOther)
}

// getPasskeysForUser returns passkey info for display
func (s *Server) getPasskeysForUser(ctx context.Context, userID string) ([]PasskeyInfo, error) {
	passkeys, err := withRxRes1(s, ctx, (*exedb.Queries).GetPasskeysByUserID, userID)
	if err != nil {
		return nil, err
	}

	var result []PasskeyInfo
	for _, pk := range passkeys {
		info := PasskeyInfo{
			ID:        pk.ID,
			Name:      pk.Name,
			CreatedAt: pk.CreatedAt.Format("Jan 2, 2006"),
		}
		if pk.LastUsedAt != nil {
			info.LastUsedAt = pk.LastUsedAt.Format("Jan 2, 2006")
		} else {
			info.LastUsedAt = "Never"
		}
		result = append(result, info)
	}
	return result, nil
}

// handlePasskeyRoutes routes passkey-related requests to the appropriate handlers
func (s *Server) handlePasskeyRoutes(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Login routes don't require authentication
	switch path {
	case "/passkey/login/start":
		s.handlePasskeyLoginStart(w, r)
		return
	case "/passkey/login/finish":
		s.handlePasskeyLoginFinish(w, r)
		return
	}

	// All other passkey routes require authentication
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	switch path {
	case "/passkey/register/start":
		s.handlePasskeyRegisterStart(w, r, userID)
	case "/passkey/register/finish":
		s.handlePasskeyRegisterFinish(w, r, userID)
	case "/passkey/delete":
		s.handlePasskeyDelete(w, r, userID)
	default:
		http.NotFound(w, r)
	}
}
