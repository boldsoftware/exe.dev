package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"exe.dev/exedb"
	"exe.dev/oidcauth"
	"exe.dev/sqlite"
)

// getTeamSSOProviderForUser returns the OIDC provider config for a user's team.
// Returns nil if the user is not in a team or the team has no SSO provider.
func (s *Server) getTeamSSOProviderForUser(ctx context.Context, userID string) *exedb.TeamSsoProvider {
	team, err := s.GetTeamForUser(ctx, userID)
	if err != nil || team == nil {
		return nil
	}
	provider, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamSSOProvider, team.TeamID)
	if err != nil {
		return nil
	}
	return &provider
}

// getTeamSSOProviderForInvite returns the OIDC provider config for a team invite.
func (s *Server) getTeamSSOProviderForInvite(ctx context.Context, teamInviteToken string) *exedb.TeamSsoProvider {
	if teamInviteToken == "" {
		return nil
	}
	invite, err := withRxRes1(s, ctx, (*exedb.Queries).GetPendingTeamInviteByToken, teamInviteToken)
	if err != nil {
		return nil
	}
	if invite.AuthProvider == nil || *invite.AuthProvider != oidcauth.ProviderName {
		return nil
	}
	provider, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamSSOProvider, invite.TeamID)
	if err != nil {
		return nil
	}
	return &provider
}

// shouldUseTeamOIDC determines if the given auth context requires team OIDC.
// Returns the SSO provider if OIDC should be used, nil otherwise.
func (s *Server) shouldUseTeamOIDC(ctx context.Context, email, userID string, isNewUser bool, teamInviteToken string) *exedb.TeamSsoProvider {
	if userID != "" && !isNewUser {
		// Check user's personal auth_provider='oidc'
		ap, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserAuthProvider, userID)
		if err == nil && ap.AuthProvider != nil && *ap.AuthProvider == oidcauth.ProviderName {
			if provider := s.getTeamSSOProviderForUser(ctx, userID); provider != nil {
				return provider
			}
		}
		// Check team-level auth_provider='oidc'
		if team, err := s.GetTeamForUser(ctx, userID); err == nil && team != nil {
			if tap, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamAuthProvider, team.TeamID); err == nil && tap != nil && *tap == oidcauth.ProviderName {
				if provider := s.getTeamSSOProviderForUser(ctx, userID); provider != nil {
					return provider
				}
			}
		}
	}

	// Team invite with auth_provider='oidc'
	if provider := s.getTeamSSOProviderForInvite(ctx, teamInviteToken); provider != nil {
		return provider
	}

	return nil
}

// buildOIDCProviderConfig converts a DB record into an oidcauth.ProviderConfig.
func (s *Server) buildOIDCProviderConfig(provider *exedb.TeamSsoProvider) *oidcauth.ProviderConfig {
	cfg := &oidcauth.ProviderConfig{
		IssuerURL:    provider.IssuerUrl,
		ClientID:     provider.ClientID,
		ClientSecret: provider.ClientSecret,
		RedirectURL:  s.webBaseURLNoRequest() + "/oauth/oidc/callback",
	}
	if provider.AuthUrl != nil {
		cfg.AuthURL = *provider.AuthUrl
	}
	if provider.TokenUrl != nil {
		cfg.TokenURL = *provider.TokenUrl
	}
	if provider.UserinfoUrl != nil {
		cfg.UserinfoURL = *provider.UserinfoUrl
	}
	return cfg
}

// startTeamOIDC initiates the team OIDC flow by creating state and redirecting.
func (s *Server) startTeamOIDC(w http.ResponseWriter, r *http.Request, params oauthStartParams, provider *exedb.TeamSsoProvider) {
	ctx := r.Context()

	providerCfg := s.buildOIDCProviderConfig(provider)

	state, err := oidcauth.GenerateState()
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to generate oidc state", "error", err)
		s.showAuthError(w, r, "Internal error. Please try again.", "")
		return
	}

	params.providerName = oidcauth.ProviderName
	params.ssoProviderID = &provider.ID
	if err := s.insertOAuthState(ctx, state, params); err != nil {
		s.slog().ErrorContext(ctx, "failed to store oidc state", "error", err)
		s.showAuthError(w, r, "Internal error. Please try again.", "")
		return
	}

	authURL := providerCfg.BuildAuthURL(state, params.email)
	s.slog().InfoContext(ctx, "oidc redirect",
		"issuer", provider.IssuerUrl,
		"team_id", provider.TeamID)
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// handleOAuthOIDCCallback handles the OIDC callback for team SSO providers.
func (s *Server) handleOAuthOIDCCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		s.slog().InfoContext(ctx, "oidc oauth error", "error", errParam,
			"error_description", r.URL.Query().Get("error_description"))
		s.showAuthError(w, r, "Authentication was cancelled or failed. Please try again.", "")
		return
	}

	code := r.URL.Query().Get("code")
	stateParam := r.URL.Query().Get("state")
	if code == "" || stateParam == "" {
		s.showAuthError(w, r, "Invalid authentication response. Please try again.", "")
		return
	}

	oauthState, err := withTxRes1(s, ctx, (*exedb.Queries).ConsumeOAuthState, stateParam)
	if err != nil {
		s.slog().InfoContext(ctx, "oidc state not found or expired", "error", err)
		s.showAuthError(w, r, "Authentication session expired. Please try again.", "")
		return
	}

	if oauthState.SsoProviderID == nil {
		s.showAuthError(w, r, "Invalid authentication session. Please try again.", "")
		return
	}

	provider, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamSSOProviderByID, *oauthState.SsoProviderID)
	if err != nil {
		s.slog().ErrorContext(ctx, "oidc provider not found", "sso_provider_id", *oauthState.SsoProviderID, "error", err)
		s.showAuthError(w, r, "SSO provider configuration not found. Please contact your administrator.", "")
		return
	}

	providerCfg := s.buildOIDCProviderConfig(&provider)

	token, err := providerCfg.Exchange(ctx, code)
	if err != nil {
		s.slog().ErrorContext(ctx, "oidc token exchange failed", "error", err, "issuer", provider.IssuerUrl)
		s.showAuthError(w, r, "Authentication failed. Please try again.", "")
		return
	}

	claims, err := providerCfg.ExtractClaims(ctx, token)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to extract oidc claims", "error", err, "issuer", provider.IssuerUrl)
		s.showAuthError(w, r, "Authentication failed. Please try again.", "")
		return
	}

	if !claims.IsEmailVerified() {
		s.showAuthError(w, r, "Your email is not verified with your identity provider. Please verify your email first.", "")
		return
	}

	// Trust the IdP's email. For SP-initiated login the email may be empty.
	// If it differs from what user typed, re-resolve.
	if oauthState.Email == "" || !strings.EqualFold(claims.Email, oauthState.Email) {
		if oauthState.Email != "" {
			s.slog().InfoContext(ctx, "oidc email substitution",
				"original", oauthState.Email, "idp", claims.Email)
		}
		oauthState.Email = claims.Email
		if oauthState.Email == "" {
			// IdP-initiated but no team invite token: clear it
			oauthState.TeamInviteToken = nil
		}

		existingUserID, err := s.GetUserIDByEmail(ctx, claims.Email)
		if err == nil {
			oauthState.IsNewUser = false
			oauthState.UserID = &existingUserID
		} else {
			oauthState.IsNewUser = true
			oauthState.UserID = nil
		}
	}

	if oauthState.IsNewUser {
		s.handleOIDCNewUser(w, r, oauthState, &provider, claims)
	} else {
		s.handleOIDCExistingUser(w, r, oauthState, &provider, claims)
	}
}

// handleOIDCNewUser handles OIDC callback for new user registration.
func (s *Server) handleOIDCNewUser(w http.ResponseWriter, r *http.Request, oauthState exedb.OauthState, provider *exedb.TeamSsoProvider, claims *oidcauth.IDTokenClaims) {
	ctx := r.Context()

	// SSH flow
	if oauthState.SshVerificationToken != nil {
		if err := s.completeSSHOIDCAuth(ctx, *oauthState.SshVerificationToken, oauthState, provider, claims); err != nil {
			s.slog().ErrorContext(ctx, "failed to complete SSH OIDC auth", "error", err)
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
		s.slog().ErrorContext(ctx, "failed to create user during oidc", "error", err)
		s.showAuthError(w, r, "Failed to create account. Please try again.", "")
		return
	}

	s.setOIDCAuthProvider(ctx, userID, claims.Sub)

	// Auto-join the team
	s.autoJoinTeam(ctx, userID, provider.TeamID, oauthState.TeamInviteToken)

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

	cookieValue, err := s.createAuthCookie(context.WithoutCancel(ctx), userID, r.Host)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to create auth cookie during oidc", "error", err)
		s.showAuthError(w, r, "Failed to create session. Please try again.", "")
		return
	}
	setExeAuthCookie(w, r, cookieValue)

	// Honor redirect/return_host that were stored in the OAuth state.
	var redirectURL, returnHost string
	if oauthState.RedirectUrl != nil {
		redirectURL = *oauthState.RedirectUrl
	}
	if oauthState.ReturnHost != nil {
		returnHost = *oauthState.ReturnHost
	}
	if redirectURL != "" || returnHost != "" {
		s.redirectAfterAuthWithParams(w, r, userID, redirectURL, returnHost)
		return
	}

	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

// handleOIDCExistingUser handles OIDC callback for existing users.
func (s *Server) handleOIDCExistingUser(w http.ResponseWriter, r *http.Request, oauthState exedb.OauthState, provider *exedb.TeamSsoProvider, claims *oidcauth.IDTokenClaims) {
	ctx := r.Context()

	if oauthState.UserID == nil {
		s.showAuthError(w, r, "Internal error. Please try again.", "")
		return
	}
	userID := *oauthState.UserID

	// SSH flow
	if oauthState.SshVerificationToken != nil {
		if err := s.completeSSHOIDCAuth(ctx, *oauthState.SshVerificationToken, oauthState, provider, claims); err != nil {
			s.slog().ErrorContext(ctx, "failed to complete SSH OIDC auth", "error", err)
			s.showAuthError(w, r, "Failed to complete SSH authentication.", "")
			return
		}
		s.renderTemplate(ctx, w, "oauth-ssh-success.html", struct {
			Email string
		}{Email: oauthState.Email})
		return
	}

	if err := s.verifyAndSetOIDCSubject(ctx, userID, claims.Sub); err != nil {
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

	// Apply invite code for login-with-exe users who are re-authenticating with an invite code.
	if oauthState.InviteCodeID != nil {
		s.maybeApplyInviteCode(ctx, s.lookupInviteCodeByID(ctx, *oauthState.InviteCodeID), userID)
	}

	s.finishOAuthWebLogin(w, r, userID, oauthState)
}

// completeSSHOIDCAuth handles the SSH session side of OIDC authentication.
func (s *Server) completeSSHOIDCAuth(ctx context.Context, token string, oauthState exedb.OauthState, provider *exedb.TeamSsoProvider, claims *oidcauth.IDTokenClaims) error {
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

		s.setOIDCAuthProvider(ctx, user.UserID, claims.Sub)
		s.autoJoinTeam(ctx, user.UserID, provider.TeamID, oauthState.TeamInviteToken)

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

		if err := s.verifyAndSetOIDCSubject(ctx, userID, claims.Sub); err != nil {
			verification.Err = err
			verification.Close()
			return err
		}

		err := s.addSSHKeyForUser(ctx, userID, oauthState.Email, verification.PublicKey)
		if err != nil {
			s.slog().ErrorContext(ctx, "failed to add SSH key during oidc", "error", err)
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

// setOIDCAuthProvider sets auth_provider='oidc' on the user.
func (s *Server) setOIDCAuthProvider(ctx context.Context, userID, sub string) {
	op := oidcauth.ProviderName
	if err := withTx1(s, ctx, (*exedb.Queries).SetUserAuthProvider, exedb.SetUserAuthProviderParams{
		AuthProvider:   &op,
		AuthProviderID: &sub,
		UserID:         userID,
	}); err != nil {
		s.slog().ErrorContext(ctx, "failed to set oidc auth provider", "error", err, "user_id", userID)
	}
}

// verifyAndSetOIDCSubject verifies the OIDC subject for users with auth_provider='oidc'.
func (s *Server) verifyAndSetOIDCSubject(ctx context.Context, userID, sub string) error {
	ap, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserAuthProvider, userID)
	if err != nil {
		return nil
	}
	if ap.AuthProvider == nil || *ap.AuthProvider != oidcauth.ProviderName {
		return nil
	}
	if ap.AuthProviderID != nil && *ap.AuthProviderID != sub {
		s.slog().WarnContext(ctx, "oidc subject mismatch",
			"user_id", userID, "expected", *ap.AuthProviderID, "got", sub)
		return fmt.Errorf("This identity doesn't match the one associated with your exe.dev account.")
	}
	if ap.AuthProviderID == nil {
		op := oidcauth.ProviderName
		_ = withTx1(s, ctx, (*exedb.Queries).SetUserAuthProvider, exedb.SetUserAuthProviderParams{
			AuthProvider:   &op,
			AuthProviderID: &sub,
			UserID:         userID,
		})
	}
	return nil
}

// autoJoinTeam adds a user to the SSO provider's team if they're not already in one.
func (s *Server) autoJoinTeam(ctx context.Context, userID, teamID string, teamInviteToken *string) {
	// If they came via a team invite, resolvePendingTeamInvites will handle it.
	// For SP-initiated (no invite), auto-join.
	existingTeam, _ := s.GetTeamForUser(ctx, userID)
	if existingTeam != nil {
		return
	}

	err := withTx1(s, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
		TeamID: teamID,
		UserID: userID,
		Role:   "user",
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to auto-join team", "error", err,
			"user_id", userID, "team_id", teamID)
	}
}

// startOIDCVerification creates an in-memory verification and an OIDC OAuth state,
// then returns a verification with OAuthURL set for the SSH session to print.
func (ss *SSHServer) startOIDCVerification(s *shellSession, publicKey, email string, isNewAccount bool, inviteCode *exedb.InviteCode, userID string, provider *exedb.TeamSsoProvider) (*EmailVerification, error) {
	verif := ss.server.addEmailVerification(publicKey, email, isNewAccount, inviteCode)

	params := oauthStartParams{
		providerName:         oidcauth.ProviderName,
		email:                email,
		userID:               userID,
		isNewUser:            isNewAccount,
		sshVerificationToken: verif.Token,
		ssoProviderID:        &provider.ID,
	}
	if inviteCode != nil {
		params.inviteCodeID = &inviteCode.ID
	}

	state, err := oidcauth.GenerateState()
	if err != nil {
		ss.server.deleteEmailVerification(verif)
		return nil, err
	}

	if err := ss.server.insertOAuthState(s.Context(), state, params); err != nil {
		ss.server.deleteEmailVerification(verif)
		return nil, fmt.Errorf("failed to store oidc state: %w", err)
	}

	providerCfg := ss.server.buildOIDCProviderConfig(provider)
	verif.OAuthURL = providerCfg.BuildAuthURL(state, email)
	verif.OAuthLabel = "SSO"
	if provider.DisplayName != nil && *provider.DisplayName != "" {
		verif.OAuthLabel = *provider.DisplayName
	}

	return verif, nil
}

// handleOAuthOIDCLogin handles SP-initiated SSO login (e.g., from Okta dashboard).
// GET /oauth/oidc/login?issuer=https://acme.okta.com
func (s *Server) handleOAuthOIDCLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	issuerURL := r.URL.Query().Get("issuer")
	if issuerURL == "" {
		s.showAuthError(w, r, "Missing issuer parameter.", "")
		return
	}

	// Normalize: strip trailing slash
	issuerURL = strings.TrimRight(issuerURL, "/")

	provider, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamSSOProviderByIssuer, issuerURL)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.slog().InfoContext(ctx, "oidc sp-initiated login: unknown issuer", "issuer", issuerURL)
			s.showAuthError(w, r, "Unknown identity provider. Please contact your administrator.", "")
		} else {
			s.slog().ErrorContext(ctx, "oidc sp-initiated login: db error", "error", err)
			s.showAuthError(w, r, "Internal error. Please try again.", "")
		}
		return
	}

	providerCfg := s.buildOIDCProviderConfig(&provider)

	state, err := oidcauth.GenerateState()
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to generate oidc state", "error", err)
		s.showAuthError(w, r, "Internal error. Please try again.", "")
		return
	}

	// SP-initiated: we don't know the email yet. The IdP will tell us.
	params := oauthStartParams{
		email:         "",
		isNewUser:     true, // will be re-resolved in callback
		ssoProviderID: &provider.ID,
	}

	insertParams := exedb.InsertOAuthStateParams{
		State:         state,
		Provider:      oidcauth.ProviderName,
		Email:         "",
		IsNewUser:     true,
		ExpiresAt:     sqlite.NormalizeTime(time.Now().Add(oidcauth.StateExpiry)),
		SsoProviderID: params.ssoProviderID,
	}

	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		_ = queries.CleanupExpiredOAuthStates(ctx, time.Now())
		return queries.InsertOAuthState(ctx, insertParams)
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to store oidc state", "error", err)
		s.showAuthError(w, r, "Internal error. Please try again.", "")
		return
	}

	authURL := providerCfg.BuildAuthURL(state, "")
	s.slog().InfoContext(ctx, "oidc sp-initiated redirect",
		"issuer", provider.IssuerUrl,
		"team_id", provider.TeamID)
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}
