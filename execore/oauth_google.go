package execore

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"exe.dev/exedb"
	"exe.dev/googleoauth"
	"exe.dev/sshkey"
)

// shouldUseGoogleOAuth determines if the given auth context requires Google OAuth.
// It fetches the necessary auth_provider data from the DB, then delegates the
// decision to googleoauth.Client.ShouldUse.
func (s *Server) shouldUseGoogleOAuth(ctx context.Context, email string, userID string, isNewUser bool, teamInviteToken string) bool {
	var userAuthProvider, inviteAuthProvider string

	if userID != "" && !isNewUser {
		if ap, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserAuthProvider, userID); err == nil {
			if ap.AuthProvider != nil {
				userAuthProvider = *ap.AuthProvider
			}
		}
	}

	if teamInviteToken != "" {
		if invite, err := withRxRes1(s, ctx, (*exedb.Queries).GetPendingTeamInviteByToken, teamInviteToken); err == nil {
			if invite.AuthProvider != nil {
				inviteAuthProvider = *invite.AuthProvider
			}
		}
	}

	return s.googleOAuth.ShouldUse(email, isNewUser, userAuthProvider, inviteAuthProvider)
}

// startGoogleOAuth initiates the Google OAuth flow by creating state and redirecting.
func (s *Server) startGoogleOAuth(w http.ResponseWriter, r *http.Request, params oauthStartParams) {
	ctx := r.Context()

	state, err := googleoauth.GenerateState()
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to generate oauth state", "error", err)
		s.showAuthError(w, r, "Internal error. Please try again.", "")
		return
	}

	if err := s.insertOAuthState(ctx, state, params); err != nil {
		s.slog().ErrorContext(ctx, "failed to store oauth state", "error", err)
		s.showAuthError(w, r, "Internal error. Please try again.", "")
		return
	}

	authURL := s.googleOAuth.AuthURL(state, params.email)
	s.slog().InfoContext(ctx, "google oauth redirect",
		"auth_url", authURL,
		"web_base_url", s.googleOAuth.WebBaseURL)
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// insertOAuthState stores an OAuth state in the database.
func (s *Server) insertOAuthState(ctx context.Context, state string, params oauthStartParams) error {
	insertParams := exedb.InsertOAuthStateParams{
		State:     state,
		Provider:  googleoauth.ProviderName,
		Email:     params.email,
		IsNewUser: params.isNewUser,
		ExpiresAt: time.Now().Add(googleoauth.StateExpiry),
	}
	if params.userID != "" {
		insertParams.UserID = &params.userID
	}
	if params.inviteCodeID != nil {
		insertParams.InviteCodeID = params.inviteCodeID
	}
	if params.teamInviteToken != "" {
		insertParams.TeamInviteToken = &params.teamInviteToken
	}
	if params.redirectURL != "" {
		insertParams.RedirectUrl = &params.redirectURL
	}
	if params.returnHost != "" {
		insertParams.ReturnHost = &params.returnHost
	}
	insertParams.LoginWithExe = params.loginWithExe
	if params.sshVerificationToken != "" {
		insertParams.SshVerificationToken = &params.sshVerificationToken
	}
	if params.hostname != "" {
		insertParams.Hostname = &params.hostname
	}
	if params.prompt != "" {
		insertParams.Prompt = &params.prompt
	}
	if params.image != "" {
		insertParams.Image = &params.image
	}
	return s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		_ = queries.CleanupExpiredOAuthStates(ctx, time.Now())
		return queries.InsertOAuthState(ctx, insertParams)
	})
}

// handleOAuthGoogleCallback handles the Google OAuth callback.
func (s *Server) handleOAuthGoogleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Check for OAuth error
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		s.slog().InfoContext(ctx, "google oauth error", "error", errParam)
		s.showAuthError(w, r, "Authentication was cancelled or failed. Please try again.", "")
		return
	}

	code := r.URL.Query().Get("code")
	stateParam := r.URL.Query().Get("state")
	if code == "" || stateParam == "" {
		s.showAuthError(w, r, "Invalid authentication response. Please try again.", "")
		return
	}

	// Atomically look up and delete the state (one-time use)
	oauthState, err := withTxRes1(s, ctx, (*exedb.Queries).ConsumeOAuthState, stateParam)
	if err != nil {
		s.slog().InfoContext(ctx, "oauth state not found or expired", "error", err)
		s.showAuthError(w, r, "Authentication session expired. Please try again.", "")
		return
	}

	// Exchange code for token and extract claims
	cfg := s.googleOAuth.OAuth2Config()
	token, err := cfg.Exchange(ctx, code)
	if err != nil {
		s.slog().ErrorContext(ctx, "google oauth token exchange failed", "error", err)
		s.showAuthError(w, r, "Authentication failed. Please try again.", "")
		return
	}

	claims, err := googleoauth.ExtractClaims(ctx, cfg, token)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to extract google id token", "error", err)
		s.showAuthError(w, r, "Authentication failed. Please try again.", "")
		return
	}

	if !claims.EmailVerified {
		s.showAuthError(w, r, "Your Google email is not verified. Please verify your email with Google first.", "")
		return
	}

	// Google may return a different email than what the user originally typed
	// (e.g. user typed a plus-alias like user+test@gmail.com but Google
	// authenticates them as user@gmail.com). Trust Google's verified email.
	if !strings.EqualFold(claims.Email, oauthState.Email) {
		s.slog().InfoContext(ctx, "google oauth email substitution",
			"original", oauthState.Email, "google", claims.Email)
		oauthState.Email = claims.Email

		// Team invite tokens are scoped to the original email. Don't let
		// a user authenticate as a different Google account and inherit
		// a team invite meant for someone else.
		oauthState.TeamInviteToken = nil

		// Re-resolve whether this is a new or existing user with the Google email.
		existingUserID, err := s.GetUserIDByEmail(ctx, claims.Email)
		if err == nil {
			oauthState.IsNewUser = false
			oauthState.UserID = &existingUserID
		} else {
			oauthState.IsNewUser = true
			oauthState.UserID = nil
		}
	}

	// Dispatch based on new vs existing user
	if oauthState.IsNewUser {
		s.handleGoogleOAuthNewUser(w, r, oauthState, claims)
	} else {
		s.handleGoogleOAuthExistingUser(w, r, oauthState, claims)
	}
}

// handleGoogleOAuthNewUser handles Google OAuth callback for new user registration.
func (s *Server) handleGoogleOAuthNewUser(w http.ResponseWriter, r *http.Request, oauthState exedb.OauthState, claims *googleoauth.IDTokenClaims) {
	ctx := r.Context()

	// SSH flow: delegate entirely to completeSSHGoogleAuth which creates user + adds SSH key
	if oauthState.SshVerificationToken != nil {
		if err := s.completeSSHGoogleAuth(ctx, *oauthState.SshVerificationToken, oauthState, claims); err != nil {
			s.slog().ErrorContext(ctx, "failed to complete SSH google auth", "error", err)
			s.showAuthError(w, r, "Failed to complete SSH authentication.", "")
			return
		}
		s.renderTemplate(ctx, w, "oauth-ssh-success.html", struct {
			Email string
		}{Email: oauthState.Email})
		return
	}

	// Web flow: create user
	var userID string
	err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		userID, err = s.createUserRecord(ctx, queries, oauthState.Email, oauthState.LoginWithExe)
		return err
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to create user during google oauth", "error", err)
		s.showAuthError(w, r, "Failed to create account. Please try again.", "")
		return
	}

	s.maybeSetGoogleAuthProvider(ctx, userID, claims.Sub, oauthState.TeamInviteToken)

	source := "web"
	if oauthState.ReturnHost != nil {
		source = "login " + *oauthState.ReturnHost
	}
	s.slackFeed.NewUser(ctx, userID, oauthState.Email, source, "")
	s.slackFeed.EmailVerified(ctx, userID)

	if err := s.checkEmailQuality(context.WithoutCancel(ctx), userID, oauthState.Email); err != nil {
		s.slog().WarnContext(ctx, "email quality check failed", "error", err, "email", oauthState.Email)
	}

	if oauthState.InviteCodeID != nil {
		if invite := s.lookupInviteCodeByID(ctx, *oauthState.InviteCodeID); invite != nil {
			if err := s.applyInviteCode(ctx, invite, userID); err != nil {
				s.slog().ErrorContext(ctx, "failed to apply invite code", "error", err)
			}
		}
	}

	if err := s.resolvePendingShares(ctx, oauthState.Email, userID); err != nil {
		s.slog().ErrorContext(ctx, "failed to resolve pending shares", "error", err)
	}
	if err := s.resolvePendingTeamInvites(ctx, oauthState.Email, userID); err != nil {
		s.slog().ErrorContext(ctx, "failed to resolve pending team invites", "error", err)
	}

	s.finishOAuthWebLogin(w, r, userID, oauthState)
}

// handleGoogleOAuthExistingUser handles Google OAuth callback for existing users.
func (s *Server) handleGoogleOAuthExistingUser(w http.ResponseWriter, r *http.Request, oauthState exedb.OauthState, claims *googleoauth.IDTokenClaims) {
	ctx := r.Context()

	if oauthState.UserID == nil {
		s.showAuthError(w, r, "Internal error. Please try again.", "")
		return
	}
	userID := *oauthState.UserID

	// SSH flow: delegate to completeSSHGoogleAuth which adds the SSH key
	if oauthState.SshVerificationToken != nil {
		if err := s.completeSSHGoogleAuth(ctx, *oauthState.SshVerificationToken, oauthState, claims); err != nil {
			s.slog().ErrorContext(ctx, "failed to complete SSH google auth", "error", err)
			s.showAuthError(w, r, "Failed to complete SSH authentication.", "")
			return
		}
		s.renderTemplate(ctx, w, "oauth-ssh-success.html", struct {
			Email string
		}{Email: oauthState.Email})
		return
	}

	// Web flow: verify GAIA ID for users with auth_provider='google'
	if err := s.verifyAndSetGoogleGAIAID(ctx, userID, claims.Sub); err != nil {
		s.showAuthError(w, r, err.Error(), "")
		return
	}

	s.slackFeed.EmailVerified(ctx, userID)

	if err := s.resolvePendingShares(ctx, oauthState.Email, userID); err != nil {
		s.slog().ErrorContext(ctx, "failed to resolve pending shares", "error", err)
	}
	if err := s.resolvePendingTeamInvites(ctx, oauthState.Email, userID); err != nil {
		s.slog().ErrorContext(ctx, "failed to resolve pending team invites", "error", err)
	}

	s.finishOAuthWebLogin(w, r, userID, oauthState)
}

// finishOAuthWebLogin creates an auth cookie and handles post-login redirect.
func (s *Server) finishOAuthWebLogin(w http.ResponseWriter, r *http.Request, userID string, oauthState exedb.OauthState) {
	ctx := r.Context()

	cookieValue, err := s.createAuthCookie(context.WithoutCancel(ctx), userID, r.Host)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to create auth cookie during google oauth", "error", err)
		s.showAuthError(w, r, "Failed to create session. Please try again.", "")
		return
	}
	setExeAuthCookie(w, r, cookieValue)

	var redirectURL, returnHost string
	if oauthState.RedirectUrl != nil {
		redirectURL = *oauthState.RedirectUrl
	}
	if oauthState.ReturnHost != nil {
		returnHost = *oauthState.ReturnHost
	}

	// Handle pending VM creation
	if oauthState.Hostname != nil && *oauthState.Hostname != "" {
		prompt := ""
		if oauthState.Prompt != nil {
			prompt = *oauthState.Prompt
		}
		image := ""
		if oauthState.Image != nil {
			image = *oauthState.Image
		}
		token := generateRegistrationToken()
		_ = withTx1(s, ctx, (*exedb.Queries).UpsertMobilePendingVM, exedb.UpsertMobilePendingVMParams{
			Token:    token,
			UserID:   userID,
			Hostname: *oauthState.Hostname,
			Prompt:   &prompt,
			VMImage:  &image,
		})
		http.Redirect(w, r, fmt.Sprintf("/creating?hostname=%s", url.QueryEscape(*oauthState.Hostname)), http.StatusTemporaryRedirect)
		return
	}

	if redirectURL != "" || returnHost != "" {
		s.redirectAfterAuthWithParams(w, r, userID, redirectURL, returnHost)
		return
	}

	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

// completeSSHGoogleAuth handles the SSH session side of Google OAuth.
// For new users: creates user + adds SSH key.
// For existing users: adds the SSH key.
// Then signals the waiting SSH session.
func (s *Server) completeSSHGoogleAuth(ctx context.Context, token string, oauthState exedb.OauthState, claims *googleoauth.IDTokenClaims) error {
	verification := s.lookUpEmailVerification(token)
	if verification == nil {
		return fmt.Errorf("ssh verification session not found")
	}

	if oauthState.IsNewUser {
		qc := AllQualityChecks
		if verification.InviteCode != nil {
			qc = SkipQualityChecks
		}
		inviterEmail := s.getInviteGiverEmail(ctx, verification.InviteCode)
		user, err := s.createUserWithSSHKey(ctx, oauthState.Email, verification.PublicKey, qc, inviterEmail)
		if err != nil {
			verification.Err = fmt.Errorf("failed to create account")
			verification.Close()
			return fmt.Errorf("failed to create user with SSH key: %w", err)
		}

		s.maybeSetGoogleAuthProvider(ctx, user.UserID, claims.Sub, oauthState.TeamInviteToken)

		if verification.InviteCode != nil {
			if err := s.applyInviteCode(ctx, verification.InviteCode, user.UserID); err != nil {
				s.slog().ErrorContext(ctx, "failed to apply invite code", "error", err)
			}
		}

		s.slackFeed.EmailVerified(ctx, user.UserID)
		_ = s.resolvePendingShares(ctx, oauthState.Email, user.UserID)
		_ = s.resolvePendingTeamInvites(ctx, oauthState.Email, user.UserID)

		verification.Close()
	} else {
		if oauthState.UserID == nil {
			verification.Err = fmt.Errorf("internal error")
			verification.Close()
			return fmt.Errorf("existing user oauth state missing user_id")
		}
		userID := *oauthState.UserID

		if err := s.verifyAndSetGoogleGAIAID(ctx, userID, claims.Sub); err != nil {
			verification.Err = err
			verification.Close()
			return err
		}

		err := s.addSSHKeyForUser(ctx, userID, oauthState.Email, verification.PublicKey)
		if err != nil {
			s.slog().ErrorContext(ctx, "failed to add SSH key during google oauth", "error", err)
			verification.Err = fmt.Errorf("failed to add SSH key")
			verification.Close()
			return err
		}

		s.slackFeed.EmailVerified(ctx, userID)
		_ = s.resolvePendingShares(ctx, oauthState.Email, userID)
		_ = s.resolvePendingTeamInvites(ctx, oauthState.Email, userID)

		verification.Close()
	}

	return nil
}

// maybeSetGoogleAuthProvider sets auth_provider='google' on the user if the
// team invite has auth_provider='google'. Gmail signups do NOT get auth_provider
// set (they create a passkey for future logins).
func (s *Server) maybeSetGoogleAuthProvider(ctx context.Context, userID, gaiaID string, teamInviteToken *string) {
	if teamInviteToken == nil {
		return
	}
	invite, err := withRxRes1(s, ctx, (*exedb.Queries).GetPendingTeamInviteByToken, *teamInviteToken)
	if err != nil {
		return
	}
	if invite.AuthProvider == nil || *invite.AuthProvider != googleoauth.ProviderName {
		return
	}
	gp := googleoauth.ProviderName
	if err := withTx1(s, ctx, (*exedb.Queries).SetUserAuthProvider, exedb.SetUserAuthProviderParams{
		AuthProvider:   &gp,
		AuthProviderID: &gaiaID,
		UserID:         userID,
	}); err != nil {
		s.slog().ErrorContext(ctx, "failed to set auth provider", "error", err)
	}
}

// verifyAndSetGoogleGAIAID verifies the GAIA ID for users with auth_provider='google',
// setting it on first login and rejecting mismatches on subsequent logins.
func (s *Server) verifyAndSetGoogleGAIAID(ctx context.Context, userID, gaiaID string) error {
	ap, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserAuthProvider, userID)
	if err != nil {
		return nil // not an auth_provider user, nothing to verify
	}
	if ap.AuthProvider == nil || *ap.AuthProvider != googleoauth.ProviderName {
		return nil
	}
	if ap.AuthProviderID != nil && *ap.AuthProviderID != gaiaID {
		s.slog().WarnContext(ctx, "google oauth GAIA ID mismatch",
			"user_id", userID, "expected", *ap.AuthProviderID, "got", gaiaID)
		return fmt.Errorf("This Google account doesn't match the one associated with your exe.dev account.")
	}
	if ap.AuthProviderID == nil {
		gp := googleoauth.ProviderName
		_ = withTx1(s, ctx, (*exedb.Queries).SetUserAuthProvider, exedb.SetUserAuthProviderParams{
			AuthProvider:   &gp,
			AuthProviderID: &gaiaID,
			UserID:         userID,
		})
	}
	return nil
}

// oauthStartParams holds the parameters for starting a Google OAuth flow.
type oauthStartParams struct {
	email                string
	userID               string
	isNewUser            bool
	inviteCodeID         *int64
	teamInviteToken      string
	redirectURL          string
	returnHost           string
	loginWithExe         bool
	sshVerificationToken string
	hostname             string
	prompt               string
	image                string
}

// addSSHKeyForUser adds an SSH key for an existing user (used during Google OAuth SSH flow).
func (s *Server) addSSHKeyForUser(ctx context.Context, userID, userEmail, publicKey string) error {
	return s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		fingerprint, err := sshkey.Fingerprint(publicKey)
		if err != nil {
			return fmt.Errorf("failed to parse public key: %w", err)
		}
		comment, err := nextSSHKeyComment(ctx, queries, userID)
		if err != nil {
			return fmt.Errorf("failed to generate SSH key comment: %w", err)
		}
		result, err := queries.InsertSSHKeyForEmailUserIfNotExists(ctx, exedb.InsertSSHKeyForEmailUserIfNotExistsParams{
			PublicKey:   publicKey,
			Comment:     comment,
			Fingerprint: fingerprint,
			Email:       userEmail,
		})
		if err != nil {
			return err
		}
		rowsAffected, _ := result.RowsAffected()
		if rowsAffected == 0 {
			existingUserID, err := queries.GetUserIDBySSHKey(ctx, publicKey)
			if err != nil {
				return fmt.Errorf("failed to verify key ownership: %w", err)
			}
			if existingUserID != userID {
				return fmt.Errorf("this SSH key is already registered to another account")
			}
		}
		return nil
	})
}

// lookupInviteCodeByID looks up an invite code by its ID.
func (s *Server) lookupInviteCodeByID(ctx context.Context, id int64) *exedb.InviteCode {
	ic, err := withRxRes1(s, ctx, (*exedb.Queries).GetInviteCodeByID, id)
	if err != nil {
		return nil
	}
	return &ic
}
