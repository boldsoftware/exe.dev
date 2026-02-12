package execore

// This file contains the handlers and helpers for web-based authentication.
//
// See devdocs/auth_flows.d2 for a complete diagram of all authentication flows
// including SSH, web, and proxy ("Login with Exe") flows.

import (
	"cmp"
	"context"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	sloghttp "github.com/samber/slog-http"

	"exe.dev/billing"
	"exe.dev/domz"
	"exe.dev/email"
	"exe.dev/exedb"
	"exe.dev/llmgateway"
	"exe.dev/pow"
	"exe.dev/sqlite"
	"exe.dev/stage"
	"exe.dev/tracing"
	_ "modernc.org/sqlite"
)

// handleEmailVerificationHTTP handles web-based email verification
func (s *Server) handleEmailVerificationHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Missing token parameter", http.StatusBadRequest)
			return
		}
		s.showEmailVerificationForm(w, r, token, r.URL.Query().Get("s"))
		return
	case http.MethodPost:
		// continued below
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data to get the token from POST
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	if token == "" {
		http.Error(w, "Missing token in form data", http.StatusBadRequest)
		return
	}

	// Extract source parameter (from query params or form data)
	source := r.URL.Query().Get("s")
	if source == "" {
		source = r.FormValue("source")
	}

	// Track the verified email and user ID for the success page
	var verifiedEmail string
	var verifiedUserID string
	var isNewUser bool

	// First check if this is an SSH session token (in-memory)
	verification := s.lookUpEmailVerification(token)

	if verification != nil {
		// This is an SSH session email verification
		verifiedEmail = verification.Email

		// Create user record immediately - billing is checked when creating VMs, not at signup
		// Skip email quality check if user has an invite code
		qc := AllQualityChecks
		if verification.InviteCode != nil {
			qc = SkipQualityChecks
		}
		inviterEmail := s.getInviteGiverEmail(r.Context(), verification.InviteCode)
		user, err := s.createUserWithSSHKey(r.Context(), verification.Email, verification.PublicKey, qc, inviterEmail)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "failed to create user with SSH key during email verification", "error", err, "token", token)
			http.Error(w, "failed to create user account", http.StatusInternalServerError)
			return
		}
		verifiedUserID = user.UserID
		s.slackFeed.EmailVerified(r.Context(), user.UserID)

		// Apply invite code if one was provided during signup
		if verification.InviteCode != nil {
			if err := s.applyInviteCode(r.Context(), verification.InviteCode, user.UserID); err != nil {
				s.slog().ErrorContext(r.Context(), "failed to apply invite code", "error", err, "code", verification.InviteCode.Code)
				// Don't fail registration, just log the error
			} else {
				s.slog().InfoContext(r.Context(), "invite code applied successfully", "code", verification.InviteCode.Code, "user_id", user.UserID, "plan_type", verification.InviteCode.PlanType)
			}
		}

		// Create HTTP auth cookie for this user
		cookieValue, err := s.createAuthCookie(context.WithoutCancel(r.Context()), user.UserID, r.Host)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to create auth cookie during SSH email verification", "error", err)
			// Continue anyway - SSH auth will still work
		} else {
			setExeAuthCookie(w, r, cookieValue)
		}

		// Signal completion to SSH session
		verification.Close()

		// Clean up email verification
		s.deleteEmailVerification(verification)
	} else {
		// Not an SSH token, check database for HTTP auth token
		// Try to validate as database token
		emailVerif, err := s.validateEmailVerificationToken(r.Context(), token)
		if err != nil {
			s.slog().InfoContext(r.Context(), "invalid email verification token during verification", "error", err, "token", token, "remote_addr", r.RemoteAddr)
			s.render401(w, r, unauthorizedData{InvalidToken: true})
			return
		}
		verifiedUserID = emailVerif.UserID
		isNewUser = emailVerif.IsNewUser
		s.slackFeed.EmailVerified(r.Context(), emailVerif.UserID)

		// Look up the user to get their email for the success page
		user, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserWithDetails, verifiedUserID)
		if err == nil {
			verifiedEmail = user.Email

			// Resolve any pending shares for this email
			// This handles the case where someone shared a box with this email before the user registered
			if err := s.resolvePendingShares(r.Context(), user.Email, verifiedUserID); err != nil {
				s.slog().ErrorContext(r.Context(), "Failed to resolve pending shares during web login", "error", err, "email", user.Email)
				http.Error(w, "Failed to resolve pending shares", http.StatusInternalServerError)
				return
			}
		}

		// Create HTTP auth cookie for this user
		cookieValue, err := s.createAuthCookie(context.WithoutCancel(r.Context()), verifiedUserID, r.Host)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to create auth cookie during HTTP email verification", "error", err)
			http.Error(w, "Failed to create authentication session", http.StatusInternalServerError)
			return
		}

		setExeAuthCookie(w, r, cookieValue)

		// Check if redirect info was stored with the token (login-with-exe flow)
		// This replaces the need for redirect params in the email URL
		var redirectURL, returnHost string
		if emailVerif.RedirectUrl != nil {
			redirectURL = *emailVerif.RedirectUrl
		}
		if emailVerif.ReturnHost != nil {
			returnHost = *emailVerif.ReturnHost
		}
		if redirectURL != "" || returnHost != "" {
			s.redirectAfterAuthWithParams(w, r, verifiedUserID, redirectURL, returnHost)
			return
		}

		// Check for pending mobile VM creation tied to this token
		pendingVM, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetMobilePendingVMByToken, token)
		if err == nil && pendingVM.Hostname != "" {
			hostname := pendingVM.Hostname
			prompt := ""
			if pendingVM.Prompt != nil {
				prompt = *pendingVM.Prompt
			}
			// Clean up the pending record
			_ = withTx1(s, context.WithoutCancel(r.Context()), (*exedb.Queries).DeleteMobilePendingVMByToken, token)

			// Check if user needs billing before starting creation
			if !s.env.SkipBilling {
				billingStatus, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserBillingStatus, verifiedUserID)
				if err == nil && userNeedsBilling(&billingStatus) {
					// Preserve hostname/prompt through billing flow
					billingURL := "/billing/update?name=" + url.QueryEscape(hostname)
					if prompt != "" {
						billingURL += "&prompt=" + url.QueryEscape(prompt)
					}
					http.Redirect(w, r, billingURL, http.StatusSeeOther)
					return
				}
			}

			// Start box creation in background and redirect to dashboard
			s.startBoxCreation(r.Context(), hostname, prompt, verifiedUserID)
			http.Redirect(w, r, "/?filter="+urlQueryEscape(hostname), http.StatusSeeOther)
			return
		}
	}

	// Check if user has passkeys for the success page
	hasPasskeys := false
	if verifiedUserID != "" {
		passkeys, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetPasskeysByUserID, verifiedUserID)
		if err == nil && len(passkeys) > 0 {
			hasPasskeys = true
		}
	}

	// Determine if this is a new user
	// SSH verifications always create new users; HTTP verifications track isNewUser in the token
	isWelcome := verification != nil || isNewUser

	// Send success response (for SSH registrations or standalone verifications)
	data := struct {
		stage.Env
		SSHCommand   string
		Source       string
		Email        string
		HasPasskeys  bool
		NeedsBilling bool
		BillingToken string
		IsWelcome    bool
	}{
		Env:          s.env,
		SSHCommand:   s.replSSHConnectionCommand(),
		Source:       source,
		Email:        verifiedEmail,
		HasPasskeys:  hasPasskeys,
		NeedsBilling: false,
		BillingToken: "",
		IsWelcome:    isWelcome,
	}
	s.renderTemplate(r.Context(), w, "email-verified.html", data)
}

// handleBillingUpdate manages billing for authenticated users.
// Uses the billing package to automatically redirect to the appropriate destination:
// - Stripe billing portal if user has an active subscription
// - Stripe checkout if user needs to create/renew subscription
// Also supports new user registration flow when token query param is present.
func (s *Server) handleBillingUpdate(w http.ResponseWriter, r *http.Request) {
	// Check for pending registration token (new user flow)
	if token := r.URL.Query().Get("token"); token != "" {
		s.handleNewUserBillingSubscribe(w, r, token)
		return
	}

	// Require authentication
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Redirect(w, r, "/auth?redirect="+url.QueryEscape(r.URL.String()), http.StatusSeeOther)
		return
	}

	// Read VM creation params to preserve through checkout
	vmName := strings.TrimSpace(r.URL.Query().Get("name"))
	vmPrompt := strings.TrimSpace(r.URL.Query().Get("prompt"))
	source := r.URL.Query().Get("source")

	// Get user email
	user, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "failed to get user details", "error", err)
		http.Error(w, "failed to get user details", http.StatusInternalServerError)
		return
	}

	// Get or create account
	existingAccount, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetAccountWithBillingStatus, userID)
	var accountID string
	var hasActiveBilling bool
	if errors.Is(err, sql.ErrNoRows) {
		// Create new account record
		accountID = "exe_" + crand.Text()[:16]
		if err := withTx1(s, r.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
			ID:        accountID,
			CreatedBy: userID,
		}); err != nil {
			s.slog().ErrorContext(r.Context(), "failed to create account", "error", err)
			http.Error(w, "failed to create account", http.StatusInternalServerError)
			return
		}
		hasActiveBilling = false
	} else if err != nil {
		s.slog().ErrorContext(r.Context(), "failed to check existing account", "error", err)
		http.Error(w, "failed to check billing status", http.StatusInternalServerError)
		return
	} else {
		accountID = existingAccount.ID
		// BillingStatus contains the computed status from billing_events
		hasActiveBilling = existingAccount.BillingStatus == "active"
	}

	// Skip billing for users without active billing if SkipBilling is set (for tests)
	// Users with active billing can always access the portal even in test environments
	if s.env.SkipBilling && !hasActiveBilling {
		http.Redirect(w, r, "/new", http.StatusSeeOther)
		return
	}

	// Build callback URLs.
	// VM params (name, prompt) are stored server-side and referenced by a short
	// token to keep URLs within Stripe's 5000-character limit.
	baseURL := s.webBaseURLNoRequest()
	var cpToken string
	if source != "" || vmName != "" || vmPrompt != "" {
		cpToken = crand.Text()
		if err := withTx1(s, r.Context(), (*exedb.Queries).InsertCheckoutParams, exedb.InsertCheckoutParamsParams{
			Token:    cpToken,
			UserID:   userID,
			Source:   source,
			VMName:   vmName,
			VMPrompt: vmPrompt,
		}); err != nil {
			s.slog().ErrorContext(r.Context(), "failed to save checkout params", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	successURL := baseURL + "/billing/success?session_id={CHECKOUT_SESSION_ID}"
	if cpToken != "" {
		successURL += "&cp=" + cpToken
	}

	// Return URL for billing portal (if user has active subscription)
	// Cancel URL for checkout (if user cancels)
	// Both should always go back to where the user came from
	var returnURL, cancelURL string
	if source == "profile" || hasActiveBilling {
		// User came from profile page OR has active billing (managing subscription)
		// Always return to /user profile page for active subscribers
		returnURL = baseURL + "/user"
		cancelURL = baseURL + "/user"
	} else {
		// User came from VM creation flow - return to /new
		returnURL = baseURL + "/new"
		cancelURL = baseURL + "/new"
		if cpToken != "" {
			cancelURL += "?cp=" + cpToken
		}
	}

	// Use billing package to determine correct redirect URL
	// This automatically returns portal URL for active subscribers, checkout URL otherwise
	redirectURL, err := s.billing.Subscribe(r.Context(), accountID, &billing.SubscribeParams{
		Email:            user.Email,
		SuccessURL:       successURL,
		CancelURL:        cancelURL,
		RedirectToPortal: true,
		PortalReturnURL:  returnURL,
	})
	if err != nil {
		s.slog().ErrorContext(r.Context(), "failed to create billing session", "error", err)
		http.Error(w, "failed to manage subscription", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

// handleBillingSuccess activates the account after Stripe checkout completes.
// Stripe redirects here with a session_id after successful checkout.
// Two flows are supported:
// 1. Authenticated users: Activates their account after billing.
// 2. New user registration: When token query param is present, creates user and sends verification email.
// If name and prompt query params are present, starts VM creation automatically.
func (s *Server) handleBillingSuccess(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")

	// Check for pending registration token (new user flow)
	if token := r.URL.Query().Get("token"); token != "" {
		s.handleNewUserBillingSuccess(w, r, sessionID, token)
		return
	}

	// Require authentication before reading checkout params,
	// so that an unauthenticated request cannot consume a valid token.
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Redirect(w, r, "/auth?redirect="+url.QueryEscape(r.URL.String()), http.StatusSeeOther)
		return
	}

	// VM params may come from the checkout_params table (referenced by cp token)
	// or directly as query parameters.
	// Read params first (don't delete yet) so that if Stripe verification fails
	// below, the token is preserved and the user can retry.
	// TODO: remove the query parameter fallback once all in-flight sessions have completed.
	var source, vmName, vmPrompt string
	cpToken := r.URL.Query().Get("cp")
	if cpToken != "" {
		cp, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetCheckoutParams, exedb.GetCheckoutParamsParams{
			Token:  cpToken,
			UserID: userID,
		})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.slog().ErrorContext(r.Context(), "failed to read checkout params", "error", err)
		}
		if err == nil {
			source = cp.Source
			vmName = cp.VMName
			vmPrompt = cp.VMPrompt
		}
	}
	source = cmp.Or(source, r.URL.Query().Get("source"))
	vmName = cmp.Or(vmName, strings.TrimSpace(r.URL.Query().Get("name")))
	vmPrompt = cmp.Or(vmPrompt, strings.TrimSpace(r.URL.Query().Get("prompt")))

	// Activate the account if we have a valid session_id (or dev bypass).
	// Verify the session with Stripe to prevent bypass attacks where users
	// craft fake session_id parameters without completing checkout.
	devBypass := s.env.WebDev && r.URL.Query().Get("dev_bypass") == "1"
	if sessionID != "" || devBypass {
		var billingID string
		if devBypass {
			billingID = "dev_bypass"
			s.slog().InfoContext(r.Context(), "billing success: dev bypass", "user_id", userID)
		} else {
			var err error
			billingID, err = s.billing.VerifyCheckout(r.Context(), sessionID)
			if err != nil {
				s.slog().ErrorContext(r.Context(), "failed to verify checkout session", "error", err, "session_id", sessionID)
				http.Error(w, "failed to verify billing", http.StatusBadRequest)
				return
			}
		}
		now := sqlite.NormalizeTime(time.Now())
		err = s.withTx(r.Context(), func(ctx context.Context, queries *exedb.Queries) error {
			if err := queries.ActivateAccount(ctx, exedb.ActivateAccountParams{
				CreatedBy: userID,
				EventAt:   now,
			}); err != nil {
				return fmt.Errorf("activate account: %w", err)
			}
			if _, err := queries.InsertBillingEvent(ctx, exedb.InsertBillingEventParams{
				AccountID: billingID,
				EventType: "active",
				EventAt:   now,
			}); err != nil {
				return fmt.Errorf("insert billing event: %w", err)
			}
			return nil
		})
		if err != nil {
			s.slog().ErrorContext(r.Context(), "failed to activate account", "error", err, "session_id", sessionID)
			http.Error(w, "failed to activate billing", http.StatusInternalServerError)
			return
		}
		// Top up LLM credits to the new (higher) max for paying users
		if err := llmgateway.NewCreditManager(&llmgateway.DBGatewayData{DB: s.db}).TopUpOnBillingUpgrade(r.Context(), userID); err != nil {
			s.slog().ErrorContext(r.Context(), "failed to top up LLM credit after billing upgrade", "error", err)
			// Don't fail the request - the account is activated, this is just a bonus
		}
		s.slog().InfoContext(r.Context(), "account activated after Stripe checkout", "user_id", userID, "session_id", sessionID, "billing_id", billingID)
		s.slackFeed.Subscribed(r.Context(), userID)

		// Best-effort cleanup of the checkout params token.
		// If this fails, the row is harmless junk cleaned up on next boot.
		if cpToken != "" {
			if _, err := withTxRes1(s, r.Context(), (*exedb.Queries).ConsumeCheckoutParams, exedb.ConsumeCheckoutParamsParams{
				Token:  cpToken,
				UserID: userID,
			}); err != nil && !errors.Is(err, sql.ErrNoRows) {
				s.slog().ErrorContext(r.Context(), "failed to delete checkout params", "error", err)
			}
		}
	}

	// If VM name was provided, start VM creation and redirect to dashboard
	if vmName != "" {
		s.startBoxCreation(r.Context(), vmName, vmPrompt, userID)
		http.Redirect(w, r, "/?filter="+url.QueryEscape(vmName), http.StatusSeeOther)
		return
	}

	// Redirect based on source
	if source == "profile" {
		// User came from profile page, redirect back to profile
		http.Redirect(w, r, "/user", http.StatusSeeOther)
		return
	} else if source != "exemenu" {
		// Default: redirect to /new to create a VM
		http.Redirect(w, r, "/new", http.StatusSeeOther)
		return
	}

	// For exemenu, show the success page
	data := struct {
		WebHost string
		Source  string
	}{
		WebHost: s.env.WebHost,
		Source:  source,
	}
	s.renderTemplate(r.Context(), w, "billing-success.html", data)
}

// handleNewUserBillingSubscribe handles billing subscription for new (unauthenticated) users.
// This is the billing-before-registration flow: new users get a pending registration token
// from /auth, then are redirected here to complete Stripe checkout before their user
// record is created.
func (s *Server) handleNewUserBillingSubscribe(w http.ResponseWriter, r *http.Request, token string) {
	// Get pending registration
	pending, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetPendingRegistrationByToken, token)
	if errors.Is(err, sql.ErrNoRows) {
		// Token invalid - redirect back to /auth
		http.Redirect(w, r, "/auth?error=expired", http.StatusSeeOther)
		return
	}
	if err != nil {
		s.slog().ErrorContext(r.Context(), "failed to get pending registration", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Check expiry in Go code (SQLite datetime format comparison issues)
	if time.Now().After(pending.ExpiresAt) {
		// Token expired - redirect back to /auth
		_ = withTx1(s, r.Context(), (*exedb.Queries).DeletePendingRegistrationByToken, token)
		http.Redirect(w, r, "/auth?error=expired", http.StatusSeeOther)
		return
	}

	// Create account ID for this registration
	accountID := "exe_" + crand.Text()[:16]

	// Build callback URLs.
	// This flow doesn't use checkout_params because we don't have a user_id yet
	// (the user is registering). The URLs here are short (just a token and email),
	// well within Stripe's 5000-character limit.
	baseURL := s.webBaseURLNoRequest()
	successURL := baseURL + "/billing/success?session_id={CHECKOUT_SESSION_ID}&token=" + url.QueryEscape(token)
	cancelURL := baseURL + "/auth?email=" + url.QueryEscape(pending.Email) + "&cancel=billing"

	checkoutURL, err := s.billing.Subscribe(r.Context(), accountID, &billing.SubscribeParams{
		Email:      pending.Email,
		SuccessURL: successURL,
		CancelURL:  cancelURL,
	})
	if err != nil {
		s.slog().ErrorContext(r.Context(), "failed to create billing checkout session", "error", err)
		http.Error(w, "failed to start subscription", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, checkoutURL, http.StatusSeeOther)
}

// handleNewUserBillingSuccess completes registration for new users after Stripe checkout.
// Creates the user record, activates the account, and sends the verification email.
func (s *Server) handleNewUserBillingSuccess(w http.ResponseWriter, r *http.Request, sessionID, token string) {
	ctx := r.Context()

	// Verify Stripe checkout
	billingID, err := s.billing.VerifyCheckout(ctx, sessionID)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to verify checkout session", "error", err, "session_id", sessionID)
		http.Error(w, "failed to verify billing", http.StatusBadRequest)
		return
	}

	// Get pending registration
	pending, err := withRxRes1(s, ctx, (*exedb.Queries).GetPendingRegistrationByToken, token)
	if errors.Is(err, sql.ErrNoRows) {
		// Possibly a retry after successful registration (back button, refresh)...or maybe an expired/invalid token.
		// If the billing account already exists, this is a retry and we should just log the user in.
		if account, acctErr := withRxRes1(s, ctx, (*exedb.Queries).GetAccount, billingID); acctErr == nil {
			s.slog().InfoContext(ctx, "billing retry success, user already registered", "billing_id", billingID, "user_id", account.CreatedBy)
			cookieValue, cookieErr := s.createAuthCookie(ctx, account.CreatedBy, r.Host)
			if cookieErr != nil {
				s.slog().ErrorContext(ctx, "failed to create auth cookie for billing retry success", "error", cookieErr)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			setExeAuthCookie(w, r, cookieValue)
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		s.slog().ErrorContext(ctx, "pending registration not found", "token", token)
		http.Error(w, "registration expired", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to get pending registration", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Check expiry in Go code (SQLite datetime format comparison issues)
	if time.Now().After(pending.ExpiresAt) {
		_ = withTx1(s, ctx, (*exedb.Queries).DeletePendingRegistrationByToken, token)
		s.slog().ErrorContext(ctx, "pending registration expired", "token", token)
		http.Error(w, "registration expired", http.StatusBadRequest)
		return
	}

	// Create user + account in transaction
	now := sqlite.NormalizeTime(time.Now())
	var userID string
	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		userID, err = s.createUserRecord(ctx, queries, pending.Email, false)
		if err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		if err := queries.InsertAccount(ctx, exedb.InsertAccountParams{
			ID:        billingID,
			CreatedBy: userID,
		}); err != nil {
			return fmt.Errorf("insert account: %w", err)
		}
		if err := queries.ActivateAccount(ctx, exedb.ActivateAccountParams{
			CreatedBy: userID,
			EventAt:   now,
		}); err != nil {
			return fmt.Errorf("activate account: %w", err)
		}
		if _, err := queries.InsertBillingEvent(ctx, exedb.InsertBillingEventParams{
			AccountID: billingID,
			EventType: "active",
			EventAt:   now,
		}); err != nil {
			return fmt.Errorf("insert billing event: %w", err)
		}
		return nil
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to create user account", "error", err)
		http.Error(w, "failed to create account", http.StatusInternalServerError)
		return
	}

	// Apply invite code if present and get inviter email for notification
	var inviterEmail string
	if pending.InviteCodeID != nil {
		invite, err := withRxRes1(s, ctx, (*exedb.Queries).GetInviteCodeByID, *pending.InviteCodeID)
		if err == nil && invite.UsedByUserID == nil {
			inviterEmail = s.getInviteGiverEmail(ctx, &invite)
			if err := s.applyInviteCode(ctx, &invite, userID); err != nil {
				s.slog().ErrorContext(ctx, "failed to apply invite code", "error", err)
			}
		}
	}

	// Clean up pending registration
	_ = withTx1(s, context.WithoutCancel(ctx), (*exedb.Queries).DeletePendingRegistrationByToken, token)

	// Notify about new user
	s.slackFeed.NewUser(ctx, userID, pending.Email, "web-billing-first", inviterEmail)
	s.slackFeed.Subscribed(ctx, userID)

	// Send verification email
	verifyToken := generateRegistrationToken()
	err = withTx1(s, ctx, (*exedb.Queries).InsertEmailVerification, exedb.InsertEmailVerificationParams{
		Token:     verifyToken,
		Email:     pending.Email,
		UserID:    userID,
		ExpiresAt: time.Now().Add(24 * time.Hour),
		IsNewUser: true, // billing-first flow is always for new users
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to create email verification", "error", err)
		// Don't fail the entire flow, user is created and billed
	} else {
		verifyURL := fmt.Sprintf("%s/verify-email?token=%s", s.webBaseURLNoRequest(), verifyToken)
		subject := fmt.Sprintf("Verify your email - %s", s.env.WebHost)
		body := fmt.Sprintf(`Hello,

Please click the link below to verify your email address:

%s

This link will expire in 24 hours.

Best regards,
The %s team`, verifyURL, s.env.WebHost)
		if err := s.sendEmail(ctx, email.TypeWebAuthVerification, pending.Email, subject, body); err != nil {
			s.slog().ErrorContext(ctx, "failed to send verification email", "error", err, "email", pending.Email)
		}
	}

	// Create auth cookie (user is logged in immediately)
	cookieValue, err := s.createAuthCookie(context.WithoutCancel(ctx), userID, r.Host)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to create auth cookie", "error", err)
	} else {
		setExeAuthCookie(w, r, cookieValue)
	}

	// Show check-email page
	var devURL string
	if s.env.WebDev {
		devURL = fmt.Sprintf("%s/verify-email?token=%s", s.webBaseURLNoRequest(), verifyToken)
	}
	s.showAuthEmailSent(w, r, pending.Email, devURL)
}

// unauthorizedData holds the template data for the 401.html page
type unauthorizedData struct {
	Email          string
	AuthURL        string
	RedirectURL    string
	ReturnHost     string
	LoginWithExe   bool
	InvalidSecret  bool
	InvalidToken   bool
	PasskeyEnabled bool
}

// render401 renders the 401.html unauthorized page.
// It extracts redirect and return_host from the request query or form values,
// using any non-empty values from data as overrides.
func (s *Server) render401(w http.ResponseWriter, r *http.Request, data unauthorizedData) {
	q := r.URL.Query()
	if data.RedirectURL == "" {
		data.RedirectURL = q.Get("redirect")
		if data.RedirectURL == "" {
			data.RedirectURL = r.FormValue("redirect")
		}
	}
	if data.ReturnHost == "" {
		data.ReturnHost = q.Get("return_host")
		if data.ReturnHost == "" {
			data.ReturnHost = r.FormValue("return_host")
		}
	}
	// Set LoginWithExe if return_host is present (proxy auth flow)
	data.LoginWithExe = data.ReturnHost != ""
	data.AuthURL = fmt.Sprintf("%s://%s/auth", getScheme(r), r.Host)
	// Enable passkeys on the main domain (RPID matches)
	data.PasskeyEnabled = true

	w.WriteHeader(http.StatusUnauthorized)
	s.renderTemplate(r.Context(), w, "401.html", data)
}

// Helper functions for authentication and reverse proxy

// createAuthCookie creates a new authentication cookie for the user
func (s *Server) createAuthCookie(ctx context.Context, userID, domain string) (string, error) {
	// Generate a random cookie value
	cookieBytes := make([]byte, 32)
	if _, err := crand.Read(cookieBytes); err != nil {
		return "", fmt.Errorf("failed to generate cookie: %w", err)
	}
	cookieValue := base64.URLEncoding.EncodeToString(cookieBytes)

	// Set expiration to 30 days from now
	expiresAt := time.Now().Add(30 * 24 * time.Hour)

	// Store in database
	// Strip port from domain since cookies are per-host, not per-host:port
	domainWithoutPort := domz.StripPort(domain)
	err := withTx1(s, ctx, (*exedb.Queries).InsertAuthCookie, exedb.InsertAuthCookieParams{
		CookieValue: cookieValue,
		UserID:      userID,
		Domain:      domainWithoutPort,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		return "", fmt.Errorf("failed to store auth cookie: %w", err)
	}

	return cookieValue, nil
}

// validateAuthCookie validates the primary authentication cookie and returns the user_id
func (s *Server) validateAuthCookie(r *http.Request) (string, error) {
	return s.validateNamedAuthCookie(r, "exe-auth")
}

// validateProxyAuthCookie validates the proxy authentication cookie and returns the user_id.
// The cookie name is port-specific: "login-with-exe-<port>".
func (s *Server) validateProxyAuthCookie(r *http.Request) (string, error) {
	port, err := getRequestPort(r)
	if err != nil {
		return "", fmt.Errorf("failed to get port from request: %w", err)
	}
	return s.validateNamedAuthCookie(r, proxyAuthCookieName(port))
}

func (s *Server) validateNamedAuthCookie(r *http.Request, cookieName string) (string, error) {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		// NB: many callers check for errors.Is(err, http.ErrNoCookie),
		// so be sure to wrap the error returned from r.Cookie.
		return "", fmt.Errorf("failed to read %s cookie: %w", cookieName, err)
	}
	if cookie.Value == "" {
		return "", fmt.Errorf("empty %s: %w", cookieName, http.ErrNoCookie)
	}

	ctx := r.Context()
	cookieValue := cookie.Value
	// Strip port from domain since cookies are per-host, not per-host:port
	domain := domz.StripPort(r.Host)

	// Get auth cookie info
	row, err := withRxRes1(s, ctx, (*exedb.Queries).GetAuthCookieInfo, exedb.GetAuthCookieInfoParams{
		CookieValue: cookieValue,
		Domain:      domain,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("invalid cookie")
	}
	if err != nil {
		return "", fmt.Errorf("database error: %w", err)
	}

	// Check if cookie has expired
	if time.Now().After(row.ExpiresAt) {
		// Clean up expired cookie.
		s.deleteAuthCookie(ctx, cookieValue)
		return "", fmt.Errorf("cookie expired")
	}

	// Update last used time
	withTx1(s, ctx, (*exedb.Queries).UpdateAuthCookieLastUsed, cookieValue)

	return row.UserID, nil
}

// userHasActiveAuthCookie returns true when the user has at least one non-expired auth cookie record.
func (s *Server) userHasActiveAuthCookie(ctx context.Context, userID string) (bool, error) {
	hasCookie, err := withRxRes1(s, ctx, (*exedb.Queries).UserHasAuthCookie, userID)
	if err != nil {
		return false, err
	}
	return hasCookie > 0, nil
}

// userHasActiveAuthCookieBestEffort logs on error and returns false when the query fails.
func (s *Server) userHasActiveAuthCookieBestEffort(ctx context.Context, userID string) bool {
	hasCookie, err := s.userHasActiveAuthCookie(ctx, userID)
	if err != nil {
		s.slog().WarnContext(ctx, "userHasActiveAuthCookie database error", "userID", userID, "error", err)
		return false
	}
	return hasCookie
}

// isUserLockedOut checks if a user account is locked out.
func (s *Server) isUserLockedOut(ctx context.Context, userID string) (bool, error) {
	return withRxRes1(s, ctx, (*exedb.Queries).GetUserIsLockedOut, userID)
}

// renderLockedOutPage renders the account-locked page and reports whether userID is locked out.
// If there's an error checking lockout status, it logs the error and returns false (allows access).
func (s *Server) renderLockedOutPage(w http.ResponseWriter, r *http.Request, userID string) bool {
	ctx := r.Context()
	isLockedOut, err := s.isUserLockedOut(ctx, userID)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to check user lockout status", "userID", userID, "error", err)
		return false
	}
	if !isLockedOut {
		return false
	}

	traceID := tracing.TraceIDFromContext(ctx)
	s.slog().WarnContext(ctx, "locked out user attempted access", "userID", userID, "trace_id", traceID)

	w.WriteHeader(http.StatusForbidden)
	data := struct {
		TraceID string
	}{
		TraceID: traceID,
	}
	if err := s.renderTemplate(ctx, w, "account-locked.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to render account-locked template", "error", err)
	}
	return true
}

// createMagicSecret creates a temporary magic secret for proxy authentication
func (s *Server) createMagicSecret(userID, boxName, redirectURL string) (string, error) {
	// Generate a random secret
	secret := crand.Text()

	// Clean up expired secrets while we're here
	s.cleanupExpiredMagicSecrets()

	// Store in memory with 2-minute expiration
	s.magicSecretsMu.Lock()
	defer s.magicSecretsMu.Unlock()

	s.magicSecrets[secret] = &MagicSecret{
		UserID:      userID,
		BoxName:     boxName,
		RedirectURL: redirectURL,
		ExpiresAt:   time.Now().Add(2 * time.Minute),
		CreatedAt:   time.Now(),
	}

	return secret, nil
}

// validateMagicSecret validates and consumes a magic secret
func (s *Server) validateMagicSecret(secret string) (*MagicSecret, error) {
	s.magicSecretsMu.Lock()
	defer s.magicSecretsMu.Unlock()

	magicSecret, exists := s.magicSecrets[secret]
	if !exists {
		return nil, fmt.Errorf("invalid secret")
	}

	// Check expiration
	if time.Now().After(magicSecret.ExpiresAt) {
		// Clean up expired secret
		delete(s.magicSecrets, secret)
		return nil, fmt.Errorf("secret expired")
	}

	// Secret is valid, consume it (single use)
	result := *magicSecret // Copy the struct
	delete(s.magicSecrets, secret)

	return &result, nil
}

// cleanupExpiredMagicSecrets removes expired magic secrets from memory
func (s *Server) cleanupExpiredMagicSecrets() {
	s.magicSecretsMu.Lock()
	defer s.magicSecretsMu.Unlock()

	now := time.Now()
	for secret, magicSecret := range s.magicSecrets {
		if now.After(magicSecret.ExpiresAt) {
			delete(s.magicSecrets, secret)
		}
	}
}

// deleteAuthCookie deletes a cookie from the database.
// This logs any errors but doesn't return them,
// as there is nothing useful for the caller to do.
func (s *Server) deleteAuthCookie(ctx context.Context, cookieValue string) {
	// Use context.WithoutCancel to ensure that cleanup completes
	// even if the client disconnected.
	ctx = context.WithoutCancel(ctx)
	if err := withTx1(s, ctx, (*exedb.Queries).DeleteAuthCookie, cookieValue); err != nil {
		s.slog().ErrorContext(ctx, "deleting auth cookie failed", "cookievalue", cookieValue, "error", err)
		return
	}
	proxyChangeDeletedCookie(cookieValue)
}

// deleteAuthCookiesForUser deletes all cookies for a user.
// This logs any error but doesn't return them,
// as there is nothing useful for the caller to do.
func (s *Server) deleteAuthCookiesForUser(ctx context.Context, userID string) {
	// Use context.WithoutCancel to ensure that the cleanup completes
	// even if the client disconnected.
	ctx = context.WithoutCancel(ctx)
	if err := withTx1(s, ctx, (*exedb.Queries).DeleteAuthCookiesByUserID, userID); err != nil {
		s.slog().ErrorContext(ctx, "deleting user's auth cookies failed", "userID", userID, "error", err)
		return
	}
	proxyChangeDeletedCookiesForUser(userID)
}

// redirectAfterAuth handles redirecting user after successful authentication.
// It extracts redirect params from the request query/form values.
func (s *Server) redirectAfterAuth(w http.ResponseWriter, r *http.Request, userID string) {
	// Check both URL query params (for GET) and form values (for POST)
	redirectURL := r.URL.Query().Get("redirect")
	if redirectURL == "" {
		redirectURL = r.FormValue("redirect")
	}
	returnHost := r.URL.Query().Get("return_host")
	if returnHost == "" {
		returnHost = r.FormValue("return_host")
	}
	s.redirectAfterAuthWithParams(w, r, userID, redirectURL, returnHost)
}

// redirectAfterAuthWithParams handles redirecting user after successful authentication
// with explicit redirect parameters (used when params come from DB rather than request).
func (s *Server) redirectAfterAuthWithParams(w http.ResponseWriter, r *http.Request, userID, redirectURL, returnHost string) {
	s.slog().DebugContext(r.Context(), "[REDIRECT] redirectAfterAuth called", "redirectURL", redirectURL, "returnHost", returnHost, "user_id", userID)

	// Check if returnHost is actually a proxy/terminal host that needs subdomain auth
	// Skip the proxy/terminal flow if returnHost is the main domain itself
	shouldDoProxyFlow := returnHost != "" && redirectURL != "" && (s.isTerminalRequest(returnHost) || s.isProxyRequest(returnHost))

	if shouldDoProxyFlow {
		if s.isTerminalRequest(returnHost) {
			s.slog().DebugContext(r.Context(), "[REDIRECT] redirectAfterAuth: detected terminal request", "returnHost", returnHost)
			// Parse hostname to extract box name
			hostname := domz.StripPort(returnHost)

			boxName, err := s.parseTerminalHostname(hostname)
			if err != nil {
				s.slog().ErrorContext(r.Context(), "Failed to parse terminal hostname", "hostname", hostname, "error", err)
				http.Error(w, "Invalid hostname format", http.StatusBadRequest)
				return
			}

			// Create magic secret for the terminal subdomain
			secret, err := s.createMagicSecret(userID, boxName, redirectURL)
			if err != nil {
				s.slog().ErrorContext(r.Context(), "Failed to create magic secret", "error", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}

			// Redirect to terminal subdomain with magic secret
			magicURL := fmt.Sprintf("%s://%s/__exe.dev/auth?secret=%s&redirect=%s",
				getScheme(r), returnHost, secret, url.QueryEscape(redirectURL))
			http.Redirect(w, r, magicURL, http.StatusTemporaryRedirect)
			return
		} else if s.isProxyRequest(returnHost) {
			s.slog().DebugContext(r.Context(), "[REDIRECT] redirectAfterAuth: detected proxy request", "returnHost", returnHost)
			// Parse hostname to extract box name (including custom domains via CNAME)
			hostname := domz.StripPort(returnHost)

			boxName, err := s.resolveBoxName(r.Context(), hostname)
			if err != nil || boxName == "" {
				s.slog().InfoContext(r.Context(), "redirectAfterAuth failed to resolve box name", "hostname", hostname, "error", err)
				http.Error(w, "invalid hostname format", http.StatusBadRequest)
				return
			}

			// Note: Access is NOT checked here. The confirmation page (/auth/confirm)
			// and ultimately the proxy handler will check access when serving content.
			// Checking access here would prevent the redirect flow from completing,
			// leaving users stuck on the main domain with cookies set there instead
			// of on the box subdomain.

			// Create magic secret for the proxy subdomain
			secret, err := s.createMagicSecret(userID, boxName, redirectURL)
			if err != nil {
				s.slog().ErrorContext(r.Context(), "Failed to create magic secret", "error", err)
				http.Error(w, "Failed to create authentication secret", http.StatusInternalServerError)
				return
			}

			// Redirect to confirmation page with magic secret
			confirmURL := fmt.Sprintf("/auth/confirm?secret=%s&return_host=%s", secret, url.QueryEscape(returnHost))
			s.slog().DebugContext(r.Context(), "[REDIRECT] redirectAfterAuth creating confirmation URL", "confirmURL", confirmURL)
			http.Redirect(w, r, confirmURL, http.StatusTemporaryRedirect)
			return
		}
	}

	// Default redirect - validate to prevent open redirect attacks
	if isValidRedirectURL(redirectURL) {
		http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
	} else {
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
	}
}

// handleLogout logs out the user by clearing their auth cookie
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Get the current user's ID from the main auth cookie
	var userID string
	if id, err := s.validateAuthCookie(r); err == nil {
		// Get the user ID before deleting
		userID = id
	}

	// Clear ALL auth cookies for this user across all domains
	if userID != "" {
		s.deleteAuthCookiesForUser(r.Context(), userID)
	}

	// Clear both cookies in the browser
	http.SetCookie(w, &http.Cookie{
		Name:     "exe-auth",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	http.SetCookie(w, &http.Cookie{
		Name:     "exe-proxy-auth",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	// Redirect to home page
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

// handleLoggedOut displays a logged out confirmation page
func (s *Server) handleLoggedOut(w http.ResponseWriter, r *http.Request) {
	data := struct {
		stage.Env
		MainDomain string
	}{
		Env:        s.env,
		MainDomain: s.env.WebHost,
	}
	_ = s.renderTemplate(r.Context(), w, "proxy-logged-out.html", data)
}

func setExeAuthCookie(w http.ResponseWriter, r *http.Request, cookieValue string) {
	setAuthCookie(w, r, "exe-auth", cookieValue)
}

func setAuthCookie(w http.ResponseWriter, r *http.Request, domain, cookieValue string) {
	cookie := &http.Cookie{
		Name:     domain,
		Value:    cookieValue,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   30 * 24 * 60 * 60, // 30 days
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(w, cookie)
}

// handleAuthConfirm handles the interstitial confirmation page for magic auth
func (s *Server) handleAuthConfirm(w http.ResponseWriter, r *http.Request) {
	// Get magic secret from query parameter
	secret := r.URL.Query().Get("secret")
	if secret == "" {
		http.Error(w, "Missing secret parameter", http.StatusBadRequest)
		return
	}

	// Validate the magic secret WITHOUT consuming it (peek only)
	s.magicSecretsMu.RLock()
	magicSecret, exists := s.magicSecrets[secret]
	s.magicSecretsMu.RUnlock()

	if !exists || time.Now().After(magicSecret.ExpiresAt) {
		// Invalid or expired secret - show 401 page with email form
		s.render401(w, r, unauthorizedData{InvalidSecret: true})
		return
	}

	returnHost := r.URL.Query().Get("return_host")
	if returnHost == "" {
		http.Error(w, "Missing return_host parameter", http.StatusBadRequest)
		return
	}

	// Extract hostname without port for display
	hostname := domz.StripPort(returnHost)
	boxName, err := s.resolveBoxName(r.Context(), hostname)
	if errors.Is(err, errInvalidBoxName) {
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}
	if err != nil {
		// TODO(bmizerany): return a nicer error page
		http.Error(w, "Failed to resolve VM name", http.StatusInternalServerError)
		return
	}
	if boxName == "" {
		http.Error(w, "Invalid VM name", http.StatusBadRequest)
		return
	}

	// Verify the box exists and get owner info
	box, err := withRxRes1(s, r.Context(), (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		// Box doesn't exist or error - show 401 page (don't reveal box existence)
		// Clean up the magic secret since we're not going to use it
		s.magicSecretsMu.Lock()
		delete(s.magicSecrets, secret)
		s.magicSecretsMu.Unlock()

		userEmail, _ := withRxRes1(s, r.Context(), (*exedb.Queries).GetEmailByUserID, magicSecret.UserID)

		s.render401(w, r, unauthorizedData{
			Email:       userEmail,
			RedirectURL: magicSecret.RedirectURL,
			ReturnHost:  returnHost,
		})
		return
	}

	// Build the magic URL for completing auth
	magicURL := fmt.Sprintf("%s://%s/__exe.dev/auth?secret=%s&redirect=%s",
		getScheme(r), returnHost, secret, url.QueryEscape(magicSecret.RedirectURL))

	// If user is the box owner, redirect directly without confirmation
	if box.CreatedByUserID == magicSecret.UserID {
		http.Redirect(w, r, magicURL, http.StatusTemporaryRedirect)
		return
	}

	// Non-owner: show confirmation page so user can confirm sharing their email with the site
	userEmail, _ := withRxRes1(s, r.Context(), (*exedb.Queries).GetEmailByUserID, magicSecret.UserID)

	data := struct {
		WebHost    string
		UserEmail  string
		SiteDomain string
		CancelURL  string
		ConfirmURL string
	}{
		WebHost:    s.env.WebHost,
		UserEmail:  userEmail,
		SiteDomain: hostname,
		CancelURL:  "/",
		ConfirmURL: magicURL,
	}
	s.renderTemplate(r.Context(), w, "login-confirmation.html", data)
}

// handleAuthCallback handles authentication callbacks with magic tokens
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	var userID string
	var redirectURL, returnHost string // From email verification token, if present

	// Check if this is an email verification request (/auth/verify?token=...)
	if strings.HasPrefix(r.URL.Path, "/auth/verify") {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Missing token parameter", http.StatusBadRequest)
			return
		}

		// Validate email verification token
		emailVerif, err := s.validateEmailVerificationToken(r.Context(), token)
		if err != nil {
			s.slog().InfoContext(r.Context(), "invalid email verification token during auth callback", "error", err, "token", token, "remote_addr", r.RemoteAddr)
			s.render401(w, r, unauthorizedData{InvalidToken: true})
			return
		}
		userID = emailVerif.UserID
		// Extract redirect info stored with the token
		if emailVerif.RedirectUrl != nil {
			redirectURL = *emailVerif.RedirectUrl
		}
		if emailVerif.ReturnHost != nil {
			returnHost = *emailVerif.ReturnHost
		}
	} else {
		// Extract token from path /auth/<token>
		token := strings.TrimPrefix(r.URL.Path, "/auth/")
		if token == "" {
			http.Error(w, "Missing authentication token", http.StatusBadRequest)
			return
		}

		// Validate the auth token
		var err error
		userID, err = s.validateAuthToken(r.Context(), token, "")
		if err != nil {
			s.slog().InfoContext(r.Context(), "invalid auth token in callback", "error", err)
			http.Error(w, "Invalid or expired authentication token", http.StatusUnauthorized)
			return
		}
	}

	// Create main domain auth cookie
	cookieValue, err := s.createAuthCookie(context.WithoutCancel(r.Context()), userID, r.Host)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to create main auth cookie", "error", err)
		http.Error(w, "Failed to create authentication cookie", http.StatusInternalServerError)
		return
	}

	setExeAuthCookie(w, r, cookieValue)
	s.recordUserEventBestEffort(r.Context(), userID, userEventSetBrowserCookies)

	// Handle redirect after authentication
	// If redirect info was stored with the token, use it; otherwise fall back to request params
	if redirectURL != "" || returnHost != "" {
		s.redirectAfterAuthWithParams(w, r, userID, redirectURL, returnHost)
	} else {
		s.redirectAfterAuth(w, r, userID)
	}
}

// handleAuth handles the main domain authentication flow
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	// Check if user already has a valid exe.dev auth cookie
	if userID, err := s.validateAuthCookie(r); err == nil {
		// User is already authenticated, handle redirect
		s.redirectAfterAuth(w, r, userID)
		return
	}

	// Handle POST request (email submission)
	if r.Method == "POST" {
		s.handleAuthEmailSubmission(w, r)
		return
	}

	q := r.URL.Query()

	// If this is a proxy auth flow (return_host is set), show 401 page
	// instead of the generic "Request a link" form
	returnHost := q.Get("return_host")
	if returnHost != "" {
		s.render401(w, r, unauthorizedData{})
		return
	}

	// Check invite code validity if provided
	inviteCodeStr := q.Get("invite")
	var inviteCodeValid, inviteCodeInvalid bool
	var invitePlanType string
	if inviteCodeStr != "" {
		if invite := s.lookupUnusedInviteCode(r.Context(), inviteCodeStr); invite != nil {
			inviteCodeValid = true
			invitePlanType = invite.PlanType
		} else {
			inviteCodeInvalid = true
		}
	}

	// Show authentication form with query parameters
	data := authFormData{
		Env:               s.env,
		SSHCommand:        s.replSSHConnectionCommand(),
		RedirectURL:       q.Get("redirect"),
		ReturnHost:        returnHost,
		InviteCode:        inviteCodeStr,
		InviteCodeValid:   inviteCodeValid,
		InviteCodeInvalid: inviteCodeInvalid,
		InvitePlanType:    invitePlanType,
	}
	s.renderTemplate(r.Context(), w, "auth-form.html", data)
}

// verifySignupPOW verifies the proof-of-work submitted with a signup request.
func (s *Server) verifySignupPOW(r *http.Request) error {
	token := r.FormValue("pow_token")
	nonceStr := r.FormValue("pow_nonce")

	if token == "" || nonceStr == "" {
		return errors.New("missing proof-of-work")
	}

	nonce, err := strconv.ParseUint(nonceStr, 10, 64)
	if err != nil {
		return errors.New("invalid nonce format")
	}

	if err := s.signupPOW.Verify(token, nonce); err != nil {
		if errors.Is(err, pow.ErrTokenExpired) {
			return errors.New("challenge expired, please try again")
		}
		return errors.New("invalid proof-of-work")
	}

	return nil
}

// showPOWInterstitial renders the proof-of-work interstitial page.
// This page solves the challenge in JavaScript and re-submits to /auth.
func (s *Server) showPOWInterstitial(w http.ResponseWriter, r *http.Request, email string) {
	token, err := s.signupPOW.NewToken()
	if err != nil {
		s.slog().ErrorContext(r.Context(), "failed to generate POW token", "error", err)
		s.showAuthError(w, r, "Internal error. Please try again.", "")
		return
	}

	data := struct {
		stage.Env
		Email         string
		POWToken      string
		POWDifficulty int
		Redirect      string
		ReturnHost    string
		LoginWithExe  bool
		InviteCode    string
	}{
		Env:           s.env,
		Email:         email,
		POWToken:      token,
		POWDifficulty: s.signupPOW.GetDifficulty(),
		Redirect:      r.FormValue("redirect"),
		ReturnHost:    r.FormValue("return_host"),
		LoginWithExe:  r.FormValue("login_with_exe") == "1",
		InviteCode:    r.FormValue("invite"),
	}
	s.renderTemplate(r.Context(), w, "auth-pow.html", data)
}

// handleAuthEmailSubmission handles the email form submission for web auth
func (s *Server) handleAuthEmailSubmission(w http.ResponseWriter, r *http.Request) {
	addr := strings.TrimSpace(r.FormValue("email"))
	if addr == "" {
		s.showAuthError(w, r, "Please enter a valid email address", "")
		return
	}

	// Basic email validation
	if !isValidEmail(addr) {
		s.showAuthError(w, r, "Please enter a valid email address", "")
		return
	}

	// TODO: This applies to existing users, which seems wrong.
	ip, allowed := s.checkSignupRateLimit(r)
	if !allowed {
		s.slog().WarnContext(r.Context(), "signup rate limit exceeded", "ip", ip, "email", addr)
		s.signupMetrics.IncBlocked("rate_limit", "web")
		http.Error(w, "Too many requests. Please try again later. + "+tracing.TraceIDFromContext(r.Context()), http.StatusTooManyRequests)
		return
	}

	// login_with_exe is explicitly set when logging into a site hosted by exe (proxy auth flow)
	isLoginWithExe := r.FormValue("login_with_exe") == "1"

	// Check for invite code early so we can bypass abuse checks if valid
	var hasValidInviteCode bool
	if inviteCodeStr := r.FormValue("invite"); inviteCodeStr != "" {
		if invite := s.lookupUnusedInviteCode(r.Context(), inviteCodeStr); invite != nil {
			hasValidInviteCode = true
		}
	}

	// Validate signup eligibility (checks if new user and runs IPQS/disabled checks)
	if err := s.validateNewSignup(r.Context(), signupValidationParams{
		ip:               ip.String(),
		email:            addr,
		source:           "web",
		trustedGitHubKey: false,
		hasInviteCode:    hasValidInviteCode,
	}); err != nil {
		s.slog().InfoContext(r.Context(), "signup validation failed", "error", err, "ip", ip, "email", addr)
		s.showAuthError(w, r, err.Error(), "")
		return
	}

	// Get or create the user
	userID, err := s.GetUserIDByEmail(r.Context(), addr)
	isNewUser := errors.Is(err, sql.ErrNoRows)
	if err != nil && !isNewUser {
		s.slog().ErrorContext(r.Context(), "Database error fetching user", "error", err)
		s.showAuthError(w, r, "Database error occurred. Please try again.", "")
		return
	}

	// Check for valid invite code early - users with valid invite codes skip billing
	var invite *exedb.InviteCode
	if inviteCodeStr := r.FormValue("invite"); inviteCodeStr != "" {
		invite = s.lookupUnusedInviteCode(r.Context(), inviteCodeStr)
	}

	// NEW FLOW: Redirect new users to billing first (unless SkipBilling for tests or valid invite code).
	// POW is skipped for billing-first flow - Stripe serves as sufficient friction.
	// Users with valid invite codes get a billing exemption, so they skip Stripe.
	// Users signing in via "Login with Exe" (proxy auth flow) skip billing - they're just
	// authenticating to access someone else's app, not signing up to use exe.dev resources.
	if isNewUser && !s.env.SkipBilling && invite == nil && !isLoginWithExe {
		// Create pending registration to track email through Stripe
		token := generateRegistrationToken()
		err = withTx1(s, r.Context(), (*exedb.Queries).InsertPendingRegistration, exedb.InsertPendingRegistrationParams{
			Token:        token,
			Email:        addr,
			InviteCodeID: nil, // No invite code in billing flow
			ExpiresAt:    time.Now().Add(1 * time.Hour),
		})
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to create pending registration", "error", err)
			s.showAuthError(w, r, "Failed to start registration. Please try again.", "")
			return
		}
		http.Redirect(w, r, "/billing/update?token="+url.QueryEscape(token), http.StatusSeeOther)
		return
	}

	// Require proof-of-work for new users when enabled
	if isNewUser && s.IsSignupPOWEnabled(r.Context()) {
		// Check if POW was submitted
		if r.FormValue("pow_token") == "" {
			// No POW submitted - show interstitial to solve it
			s.showPOWInterstitial(w, r, addr)
			return
		}
		// Verify the submitted POW
		if err := s.verifySignupPOW(r); err != nil {
			s.slog().InfoContext(r.Context(), "signup POW verification failed", "error", err, "ip", ip, "email", addr)
			// Show interstitial again with fresh challenge
			s.showPOWInterstitial(w, r, addr)
			return
		}
		// Record the client-reported solve time in the HTTP request log
		if timeMs := r.FormValue("pow_time_ms"); timeMs != "" {
			sloghttp.AddCustomAttributes(r, slog.String("pow_time_ms", timeMs))
		}
	}

	// Use the invite code we already looked up (needed for new user notification and email verification)
	var inviteCodeID *int64
	var inviterEmail string
	if invite != nil {
		inviteCodeID = &invite.ID
		inviterEmail = s.getInviteGiverEmail(r.Context(), invite)
		s.slog().InfoContext(r.Context(), "valid invite code provided via web auth", "code", invite.Code)
	}

	if isNewUser {
		err = s.withTx(r.Context(), func(ctx context.Context, queries *exedb.Queries) error {
			userID, err = s.createUserRecord(ctx, queries, addr, isLoginWithExe)
			return err
		})
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Database error during user creation", "error", err)
			s.showAuthError(w, r, "Database error occurred. Please try again.", "")
			return
		}
	}
	if isNewUser {
		source := "web"
		if returnHost := r.FormValue("return_host"); returnHost != "" {
			source = "login " + returnHost
		}
		s.slackFeed.NewUser(r.Context(), userID, addr, source, inviterEmail)
		// Check email quality and disable VM creation if disposable
		if err := s.checkEmailQuality(context.WithoutCancel(r.Context()), userID, addr); err != nil {
			s.slog().WarnContext(r.Context(), "email quality check failed", "error", err, "email", addr)
		}
	}

	// Generate verification token
	token := generateRegistrationToken()

	// Get redirect params to store with the token (avoids putting them in email URLs which get spam-filtered)
	redirectURL := r.FormValue("redirect")
	returnHost := r.FormValue("return_host")

	// Store verification in database with redirect info
	insertParams := exedb.InsertEmailVerificationParams{
		Token:        token,
		Email:        addr,
		UserID:       userID,
		ExpiresAt:    time.Now().Add(24 * time.Hour),
		InviteCodeID: inviteCodeID,
		IsNewUser:    isNewUser,
	}
	if redirectURL != "" {
		insertParams.RedirectUrl = &redirectURL
	}
	if returnHost != "" {
		insertParams.ReturnHost = &returnHost
	}
	err = withTx1(s, context.WithoutCancel(r.Context()), (*exedb.Queries).InsertEmailVerification, insertParams)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to store email verification", "error", err)
		s.showAuthError(w, r, "Failed to create verification. Please try again.", "")
		return
	}

	// Send email with clean verification URL (no redirect params - they're stored in DB)
	verifyEmailURL := fmt.Sprintf("%s://%s/verify-email?token=%s", getScheme(r), r.Host, token)

	// Send custom email for web auth with the proper URL
	webHost := s.env.WebHost
	subject := fmt.Sprintf("Verify your email - %s", webHost)
	body := fmt.Sprintf(`Hello,

Please click the link below to verify your email address:

%s

This link will expire in 24 hours.

Best regards,
The %s team`, verifyEmailURL, webHost)

	emailType := email.TypeWebAuthVerification
	if isLoginWithExe {
		emailType = email.TypeLoginWithExeVerification
	}
	err = s.sendEmail(r.Context(), emailType, addr, subject, body)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to send auth email", "error", err, "email", addr)
		s.showAuthError(w, r, "Failed to send email. Please try again or contact support.", "")
		return
	}

	// Show success page
	var devURL string
	if s.env.WebDev {
		devURL = verifyEmailURL
	}
	s.showAuthEmailSent(w, r, addr, devURL)
}

// showAuthError displays an authentication error page
func (s *Server) showAuthError(w http.ResponseWriter, r *http.Request, message, command string) {
	data := struct {
		stage.Env
		Message     string
		Command     string
		QueryString string
	}{
		Env:         s.env,
		Message:     message,
		Command:     command,
		QueryString: r.URL.RawQuery,
	}

	w.WriteHeader(http.StatusBadRequest)
	s.renderTemplate(r.Context(), w, "auth-error.html", data)
}

// showAuthEmailSent displays the email sent confirmation page
func (s *Server) showAuthEmailSent(w http.ResponseWriter, r *http.Request, email, devURL string) {
	data := struct {
		stage.Env
		Email       string
		QueryString string
		DevURL      string // Development-only URL for easy testing
	}{
		Env:         s.env,
		Email:       email,
		QueryString: r.URL.RawQuery,
		DevURL:      devURL,
	}

	s.renderTemplate(r.Context(), w, "email-sent.html", data)
}

// checkSignupRateLimit checks if the request should be rate limited.
// Returns the client IP and whether the request is allowed.
func (s *Server) checkSignupRateLimit(r *http.Request) (netip.Addr, bool) {
	ipStr := clientIPFromRemoteAddr(r.RemoteAddr)
	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
		// Can't parse IP, allow the request (don't break on edge cases)
		return netip.Addr{}, true
	}
	return ip, s.signupLimiter.Allow(ip)
}

// handleLinkDiscord handles Discord account linking via HMAC'd links from the Discord bot.
// The link format is: /link-discord?discord_id=X&discord_username=Y&ts=Z&hmac=H
func (s *Server) handleLinkDiscord(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Require authentication
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		authURL := fmt.Sprintf("/auth?redirect=%s", url.QueryEscape(r.URL.String()))
		http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
		return
	}

	discordID := r.URL.Query().Get("discord_id")
	discordUsername := r.URL.Query().Get("discord_username")
	ts := r.URL.Query().Get("ts")
	hmacParam := r.URL.Query().Get("hmac")

	if discordID == "" || discordUsername == "" || ts == "" || hmacParam == "" {
		http.Error(w, "Missing required parameters", http.StatusBadRequest)
		return
	}

	// Verify HMAC
	if !s.verifyDiscordLinkHMAC(discordID, discordUsername, ts, hmacParam) {
		http.Error(w, "Invalid or expired link", http.StatusBadRequest)
		return
	}

	// Check if user already has Discord linked (to prevent multiple invite grants)
	existingUser, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to get user details", "error", err, "user_id", userID)
		http.Error(w, "Failed to get user details", http.StatusInternalServerError)
		return
	}
	alreadyLinked := existingUser.DiscordID != nil

	// Add email to canonical log line
	sloghttp.AddCustomAttributes(r, slog.String("email", existingUser.Email))

	// Link the Discord account
	err = withTx1(s, ctx, (*exedb.Queries).SetUserDiscord, exedb.SetUserDiscordParams{
		DiscordID:       &discordID,
		DiscordUsername: &discordUsername,
		UserID:          userID,
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to link Discord account", "error", err, "user_id", userID, "discord_id", discordID)
		http.Error(w, "Failed to link Discord account", http.StatusInternalServerError)
		return
	}

	// Give the user 5 invite codes for linking Discord (only if not already linked)
	invitesAdded := 0
	if !alreadyLinked {
		err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			for range 5 {
				code, err := queries.GenerateUniqueInviteCode(ctx)
				if err != nil {
					return fmt.Errorf("generate invite code: %w", err)
				}

				_, err = queries.CreateInviteCode(ctx, exedb.CreateInviteCodeParams{
					Code:             code,
					PlanType:         "trial",
					AssignedToUserID: &userID,
					AssignedBy:       "discord-link",
					AssignedFor:      nil,
				})
				if err != nil {
					return fmt.Errorf("create invite code: %w", err)
				}
			}
			return nil
		})
		if err != nil {
			s.slog().ErrorContext(ctx, "Failed to grant invite codes for Discord link", "error", err, "user_id", userID, "email", existingUser.Email)
		} else {
			invitesAdded = 5
		}
	}

	// Add invites count to canonical log line
	sloghttp.AddCustomAttributes(r, slog.Int("invites_added", invitesAdded))

	// Show success page
	data := struct {
		stage.Env
		DiscordUsername string
	}{
		Env:             s.env,
		DiscordUsername: discordUsername,
	}
	s.renderTemplate(ctx, w, "discord-linked.html", data)
}

// verifyDiscordLinkHMAC verifies the HMAC signature for Discord account linking.
// Returns true if the HMAC is valid and the timestamp is within 10 minutes.
func (s *Server) verifyDiscordLinkHMAC(discordID, discordUsername, ts, providedHMAC string) bool {
	if s.discordLinkSecret == "" {
		return false
	}

	// Check timestamp isn't too old (10 minutes)
	timestamp, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if time.Now().Unix()-timestamp > 600 {
		return false
	}

	// Compute expected HMAC
	data := fmt.Sprintf("%s:%s:%s", discordID, discordUsername, ts)
	mac := hmac.New(sha256.New, []byte(s.discordLinkSecret))
	mac.Write([]byte(data))
	expected := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(providedHMAC))
}
