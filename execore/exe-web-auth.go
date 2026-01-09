package execore

// This file contains the handlers and helpers for web-based authentication.
//
// See devdocs/auth_flows.d2 for a complete diagram of all authentication flows
// including SSH, web, and proxy ("Login with Exe") flows.

import (
	"context"
	"crypto/rand"
	crand "crypto/rand"
	"database/sql"
	"encoding/base64"
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
	emailpkg "exe.dev/email"
	"exe.dev/exedb"
	"exe.dev/pow"
	"exe.dev/sqlite"
	"exe.dev/stage"
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

	// First check if this is an SSH session token (in-memory)
	verification := s.lookUpEmailVerification(token)

	if verification != nil {
		// This is an SSH session email verification
		verifiedEmail = verification.Email

		// Create user record immediately - billing is checked when creating VMs, not at signup
		user, err := s.createUserWithSSHKey(r.Context(), verification.Email, verification.PublicKey)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "failed to create user with SSH key during email verification", "error", err, "token", token)
			http.Error(w, "failed to create user account", http.StatusInternalServerError)
			return
		}
		verifiedUserID = user.UserID
		s.slackFeed.EmailVerified(r.Context(), user.UserID)

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
		userID, err := s.validateEmailVerificationToken(r.Context(), token)
		if err != nil {
			s.slog().InfoContext(r.Context(), "invalid email verification token during verification", "error", err, "token", token, "remote_addr", r.RemoteAddr)
			s.render401(w, r, unauthorizedData{InvalidToken: true})
			return
		}
		verifiedUserID = userID
		s.slackFeed.EmailVerified(r.Context(), userID)

		// Look up the user to get their email for the success page
		user, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserWithDetails, userID)
		if err == nil {
			verifiedEmail = user.Email

			// Resolve any pending shares for this email
			// This handles the case where someone shared a box with this email before the user registered
			if err := s.resolvePendingShares(r.Context(), user.Email, userID); err != nil {
				s.slog().ErrorContext(r.Context(), "Failed to resolve pending shares during web login", "error", err, "email", user.Email)
				http.Error(w, "Failed to resolve pending shares", http.StatusInternalServerError)
				return
			}
		}

		// Create HTTP auth cookie for this user
		cookieValue, err := s.createAuthCookie(context.WithoutCancel(r.Context()), userID, r.Host)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to create auth cookie during HTTP email verification", "error", err)
			http.Error(w, "Failed to create authentication session", http.StatusInternalServerError)
			return
		}

		setExeAuthCookie(w, r, cookieValue)

		// Clean up the database token (single use)
		err = withTx1(s, context.WithoutCancel(r.Context()), (*exedb.Queries).DeleteEmailVerificationByToken, token)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to cleanup email verification token", "error", err)
			// Continue anyway
		}

		// Check if this is part of a web auth flow with redirect parameters (from form for POST)
		redirectURL := r.FormValue("redirect")
		returnHost := r.FormValue("return_host")
		if redirectURL != "" || returnHost != "" {
			// This is a web auth flow, perform redirect after authentication
			s.redirectAfterAuth(w, r, userID)
			return
		}

		// Check for pending mobile VM creation tied to this token
		var hostname, prompt string
		err = s.db.Rx(r.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
			row := rx.Conn().QueryRowContext(ctx, `SELECT hostname, prompt FROM mobile_pending_vm WHERE token = ?`, token)
			return row.Scan(&hostname, &prompt)
		})
		if err == nil && hostname != "" {
			// Clean up the pending record
			_ = s.db.Tx(context.WithoutCancel(r.Context()), func(ctx context.Context, tx *sqlite.Tx) error {
				_, err := tx.Conn().ExecContext(ctx, `DELETE FROM mobile_pending_vm WHERE token = ?`, token)
				return err
			})

			// Check if user needs billing before starting creation
			if !s.env.SkipBilling {
				needsBilling, err := withRxRes1(s, r.Context(), (*exedb.Queries).UserNeedsBilling, userID)
				if err == nil && needsBilling != nil && *needsBilling {
					// Preserve hostname/prompt through billing flow
					billingURL := "/billing/subscribe?name=" + url.QueryEscape(hostname)
					if prompt != "" {
						billingURL += "&prompt=" + url.QueryEscape(prompt)
					}
					http.Redirect(w, r, billingURL, http.StatusSeeOther)
					return
				}
			}

			// Start box creation in background and redirect to dashboard
			s.startBoxCreation(r.Context(), hostname, prompt, userID)
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
		IsWelcome:    false,
	}
	s.renderTemplate(w, "email-verified.html", data)
}

// handleBillingSubscribe handles billing subscription for authenticated users.
// This is used when a user tries to create a VM but doesn't have billing info.
// Accepts name and prompt query params to preserve VM creation details through Stripe checkout.
func (s *Server) handleBillingSubscribe(w http.ResponseWriter, r *http.Request) {
	// Require authentication
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Redirect(w, r, "/auth?redirect="+url.QueryEscape(r.URL.String()), http.StatusSeeOther)
		return
	}

	// Read VM creation params to preserve through checkout
	vmName := strings.TrimSpace(r.URL.Query().Get("name"))
	vmPrompt := strings.TrimSpace(r.URL.Query().Get("prompt"))

	// Check if user needs billing (only new users need billing, legacy users are grandfathered)
	// Skip this check if SkipBilling is set (for tests)
	if s.env.SkipBilling {
		http.Redirect(w, r, "/new", http.StatusSeeOther)
		return
	}

	// _debug_force_billing=1 forces billing flow even for grandfathered users.
	// This is used for canary testing billing before the official billing start date.
	forceBilling := r.URL.Query().Get("_debug_force_billing") == "1"

	needsBilling, err := withRxRes1(s, r.Context(), (*exedb.Queries).UserNeedsBilling, userID)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "failed to check user account", "error", err)
		http.Error(w, "failed to check billing status", http.StatusInternalServerError)
		return
	}
	if !forceBilling && (needsBilling == nil || !*needsBilling) {
		// User doesn't need billing (already has it or is a legacy user), redirect to new VM page
		http.Redirect(w, r, "/new", http.StatusSeeOther)
		return
	}

	// Get user email
	user, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "failed to get user details", "error", err)
		http.Error(w, "failed to get user details", http.StatusInternalServerError)
		return
	}

	// Create account record
	accountID := "exe_" + rand.Text()[:16]
	if err := withTx1(s, r.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        accountID,
		CreatedBy: userID,
	}); err != nil {
		s.slog().ErrorContext(r.Context(), "failed to create account", "error", err)
		http.Error(w, "failed to create account", http.StatusInternalServerError)
		return
	}

	// Build callback URLs
	// Always route through /billing/success to activate the account after Stripe checkout.
	// The {CHECKOUT_SESSION_ID} placeholder is replaced by Stripe with the actual session ID.
	baseURL := s.webBaseURLNoRequest()
	source := r.URL.Query().Get("source")
	successURL := baseURL + "/billing/success?session_id={CHECKOUT_SESSION_ID}"
	if source != "" {
		successURL += "&source=" + url.QueryEscape(source)
	}
	// Include VM creation params in success URL so we can create the VM after checkout
	if vmName != "" {
		successURL += "&name=" + url.QueryEscape(vmName)
	}
	if vmPrompt != "" {
		successURL += "&prompt=" + url.QueryEscape(vmPrompt)
	}

	// If user cancels checkout, send them back to /new with their form data preserved
	cancelURL := baseURL + "/new"
	if vmName != "" || vmPrompt != "" {
		cancelURL += "?"
		if vmName != "" {
			cancelURL += "name=" + url.QueryEscape(vmName)
			if vmPrompt != "" {
				cancelURL += "&"
			}
		}
		if vmPrompt != "" {
			cancelURL += "prompt=" + url.QueryEscape(vmPrompt)
		}
	}

	checkoutURL, err := s.billing.Subscribe(r.Context(), accountID, &billing.SubscribeParams{
		Email:      user.Email,
		SuccessURL: successURL,
		CancelURL:  cancelURL,
		TrialEnd:   time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		s.slog().ErrorContext(r.Context(), "failed to create billing checkout session", "error", err)
		http.Error(w, "failed to start subscription", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, checkoutURL, http.StatusSeeOther)
}

// handleBillingSuccess activates the account after Stripe checkout completes.
// Stripe redirects here with a session_id after successful checkout.
// If name and prompt query params are present, starts VM creation automatically.
func (s *Server) handleBillingSuccess(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	sessionID := r.URL.Query().Get("session_id")
	vmName := strings.TrimSpace(r.URL.Query().Get("name"))
	vmPrompt := strings.TrimSpace(r.URL.Query().Get("prompt"))

	// Require authentication
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Redirect(w, r, "/auth?redirect="+url.QueryEscape(r.URL.String()), http.StatusSeeOther)
		return
	}

	// Activate the account if we have a valid session_id.
	// Verify the session with Stripe to prevent bypass attacks where users
	// craft fake session_id parameters without completing checkout.
	if sessionID != "" {
		billingID, err := s.billing.VerifyCheckout(r.Context(), sessionID)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "failed to verify checkout session", "error", err, "session_id", sessionID)
			http.Error(w, "failed to verify billing", http.StatusBadRequest)
			return
		}
		if err := withTx1(s, r.Context(), (*exedb.Queries).ActivateAccount, userID); err != nil {
			s.slog().ErrorContext(r.Context(), "failed to activate account", "error", err, "session_id", sessionID)
			http.Error(w, "failed to activate billing", http.StatusInternalServerError)
			return
		}
		s.slog().InfoContext(r.Context(), "account activated after Stripe checkout", "user_id", userID, "session_id", sessionID, "billing_id", billingID)
		s.slackFeed.Subscribed(r.Context(), userID)
	}

	// If VM name was provided, start VM creation and redirect to dashboard
	if vmName != "" {
		s.startBoxCreation(r.Context(), vmName, vmPrompt, userID)
		http.Redirect(w, r, "/?filter="+url.QueryEscape(vmName), http.StatusSeeOther)
		return
	}

	// If not from exemenu, redirect to /new to create a VM
	if source != "exemenu" {
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
	s.renderTemplate(w, "billing-success.html", data)
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
	s.renderTemplate(w, "401.html", data)
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

// createProxyBearerToken creates a bearer token for HTTP Basic auth proxy access scoped to a box.
func (s *Server) createProxyBearerToken(ctx context.Context, userID string, boxID int) (string, error) {
	token := crand.Text()
	expiresAt := time.Now().Add(proxyBearerTokenTTL)

	err := withTx1(s, ctx, (*exedb.Queries).InsertProxyBearerToken, exedb.InsertProxyBearerTokenParams{
		Token:     token,
		UserID:    userID,
		BoxID:     int64(boxID),
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return "", fmt.Errorf("failed to store proxy bearer token: %w", err)
	}

	return token, nil
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
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("invalid cookie")
		}
		return "", fmt.Errorf("database error: %w", err)
	}

	// Check if cookie has expired
	if time.Now().After(row.ExpiresAt) {
		// Clean up expired cookie - use context.WithoutCancel to ensure cleanup completes even if client disconnects
		withTx1(s, context.WithoutCancel(ctx), (*exedb.Queries).DeleteAuthCookie, cookieValue)
		return "", fmt.Errorf("cookie expired")
	}

	// Update last used time
	withTx1(s, ctx, (*exedb.Queries).UpdateAuthCookieLastUsed, cookieValue)

	return row.UserID, nil
}

// validateProxyBearerToken ensures a bearer token is valid for the provided box and returns the associated user.
func (s *Server) validateProxyBearerToken(ctx context.Context, token string, boxID int) (string, error) {
	if token == "" {
		return "", fmt.Errorf("empty proxy bearer token")
	}

	record, err := withRxRes1(s, ctx, (*exedb.Queries).GetProxyBearerToken, token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("proxy bearer token not found")
		}
		return "", fmt.Errorf("fetching proxy bearer token: %w", err)
	}

	if record.BoxID != int64(boxID) {
		return "", fmt.Errorf("proxy bearer token is not valid for this VM")
	}

	if time.Now().After(record.ExpiresAt) {
		return "", fmt.Errorf("proxy bearer token expired")
	}

	if err := withTx1(s, ctx, (*exedb.Queries).UpdateProxyBearerTokenLastUsed, token); err != nil {
		s.slog().WarnContext(ctx, "failed to update proxy bearer token last used", "error", err)
	}

	return record.UserID, nil
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

// redirectAfterAuth handles redirecting user after successful authentication
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
		err := withTx1(s, r.Context(), (*exedb.Queries).DeleteAuthCookiesByUserID, userID)
		if err != nil {
			s.slog().ErrorContext(r.Context(), "Failed to delete user's auth cookies from database", "error", err)
		}
	}

	// Clear both cookies in the browser
	http.SetCookie(w, &http.Cookie{
		Name:     "exe-auth",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	http.SetCookie(w, &http.Cookie{
		Name:     "exe-proxy-auth",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	// Redirect to home page
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

// handleLoggedOut displays a logged out confirmation page
func (s *Server) handleLoggedOut(w http.ResponseWriter, _ *http.Request) {
	data := struct {
		stage.Env
		MainDomain string
	}{
		Env:        s.env,
		MainDomain: s.env.WebHost,
	}
	_ = s.renderTemplate(w, "proxy-logged-out.html", data)
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
	s.renderTemplate(w, "login-confirmation.html", data)
}

// handleAuthCallback handles authentication callbacks with magic tokens
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	var userID string

	// Check if this is an email verification request (/auth/verify?token=...)
	if strings.HasPrefix(r.URL.Path, "/auth/verify") {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Missing token parameter", http.StatusBadRequest)
			return
		}

		// Validate email verification token
		var err error
		userID, err = s.validateEmailVerificationToken(r.Context(), token)
		if err != nil {
			s.slog().InfoContext(r.Context(), "invalid email verification token during auth callback", "error", err, "token", token, "remote_addr", r.RemoteAddr)
			s.render401(w, r, unauthorizedData{InvalidToken: true})
			return
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
			s.slog().ErrorContext(r.Context(), "Invalid auth token in callback", "error", err)
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
	s.redirectAfterAuth(w, r, userID)
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

	// Show authentication form with query parameters
	data := authFormData{
		Env:         s.env,
		SSHCommand:  s.replSSHConnectionCommand(),
		RedirectURL: q.Get("redirect"),
		ReturnHost:  returnHost,
	}
	s.renderTemplate(w, "auth-form.html", data)
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
	}{
		Env:           s.env,
		Email:         email,
		POWToken:      token,
		POWDifficulty: s.signupPOW.GetDifficulty(),
		Redirect:      r.FormValue("redirect"),
		ReturnHost:    r.FormValue("return_host"),
		LoginWithExe:  r.FormValue("login_with_exe") == "1",
	}
	s.renderTemplate(w, "auth-pow.html", data)
}

// handleAuthEmailSubmission handles the email form submission for web auth
func (s *Server) handleAuthEmailSubmission(w http.ResponseWriter, r *http.Request) {
	ip, allowed := s.checkSignupRateLimit(r)
	if !allowed {
		s.slog().WarnContext(r.Context(), "signup rate limit exceeded", "ip", ip)
		s.signupMetrics.IncBlocked("rate_limit", "web")
		http.Error(w, "Too many requests. Please try again later.", http.StatusTooManyRequests)
		return
	}

	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" {
		s.showAuthError(w, r, "Please enter a valid email address", "")
		return
	}

	// Basic email validation
	if !isValidEmail(email) {
		s.showAuthError(w, r, "Please enter a valid email address", "")
		return
	}

	// login_with_exe is explicitly set when logging into a site hosted by exe (proxy auth flow)
	createdForLoginWithExe := r.FormValue("login_with_exe") == "1"

	// Validate signup eligibility (checks if new user and runs IPQS/disabled checks)
	if err := s.validateNewSignup(r.Context(), ip.String(), email, "web"); err != nil {
		s.slog().InfoContext(r.Context(), "signup validation failed", "error", err, "ip", ip, "email", email)
		s.showAuthError(w, r, err.Error(), "")
		return
	}

	// Get or create the user
	userID, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserIDByEmail, email)
	isNewUser := errors.Is(err, sql.ErrNoRows)
	if err != nil && !isNewUser {
		s.slog().ErrorContext(r.Context(), "Database error fetching user", "error", err)
		s.showAuthError(w, r, "Database error occurred. Please try again.", "")
		return
	}

	// Require proof-of-work for new users when enabled
	if isNewUser && s.IsSignupPOWEnabled(r.Context()) {
		// Check if POW was submitted
		if r.FormValue("pow_token") == "" {
			// No POW submitted - show interstitial to solve it
			s.showPOWInterstitial(w, r, email)
			return
		}
		// Verify the submitted POW
		if err := s.verifySignupPOW(r); err != nil {
			s.slog().InfoContext(r.Context(), "signup POW verification failed", "error", err, "ip", ip, "email", email)
			// Show interstitial again with fresh challenge
			s.showPOWInterstitial(w, r, email)
			return
		}
		// Record the client-reported solve time in the HTTP request log
		if timeMs := r.FormValue("pow_time_ms"); timeMs != "" {
			sloghttp.AddCustomAttributes(r, slog.String("pow_time_ms", timeMs))
		}
	}

	if isNewUser {
		err = s.withTx(r.Context(), func(ctx context.Context, queries *exedb.Queries) error {
			userID, err = s.createUserRecord(ctx, queries, email, createdForLoginWithExe)
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
		s.slackFeed.NewUser(r.Context(), userID, email, source)
		// Check email quality and disable VM creation if disposable
		if err := s.checkEmailQuality(context.WithoutCancel(r.Context()), userID, email); err != nil {
			s.slog().WarnContext(r.Context(), "email quality check failed", "error", err, "email", email)
		}
	}

	// Generate verification token
	token := generateRegistrationToken()

	// Store verification in database (reuse existing email_verifications table)
	err = withTx1(s, context.WithoutCancel(r.Context()), (*exedb.Queries).InsertEmailVerification, exedb.InsertEmailVerificationParams{
		Token:     token,
		Email:     email,
		UserID:    userID,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to store email verification", "error", err)
		s.showAuthError(w, r, "Failed to create verification. Please try again.", "")
		return
	}

	// Create verification link
	verificationURL := fmt.Sprintf("%s://%s/auth/verify?token=%s", getScheme(r), r.Host, token)

	// Add redirect parameters to the verification URL if present (from form values for POST)
	if redirect := r.FormValue("redirect"); redirect != "" {
		verificationURL += "&redirect=" + url.QueryEscape(redirect)
	}
	if returnHost := r.FormValue("return_host"); returnHost != "" {
		verificationURL += "&return_host=" + url.QueryEscape(returnHost)
	}

	// Send email with proper verification URL that includes redirect params
	verifyEmailURL := fmt.Sprintf("%s://%s/verify-email?token=%s", getScheme(r), r.Host, token)

	// Add redirect parameters to the verify-email URL if present (from form values for POST)
	// Both params needed: redirect=path, return_host=subdomain for cross-domain auth flow
	if redirect := r.FormValue("redirect"); redirect != "" {
		verifyEmailURL += "&redirect=" + url.QueryEscape(redirect)
	}
	if returnHost := r.FormValue("return_host"); returnHost != "" {
		verifyEmailURL += "&return_host=" + url.QueryEscape(returnHost)
	}

	// Send custom email for web auth with the proper URL
	webHost := s.env.WebHost
	subject := fmt.Sprintf("Verify your email - %s", webHost)
	body := fmt.Sprintf(`Hello,

Please click the link below to verify your email address:

%s

This link will expire in 24 hours.

Best regards,
The %s team`, verifyEmailURL, webHost)

	err = s.sendEmail(r.Context(), emailpkg.TypeWebAuthVerification, email, subject, body)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to send auth email", "error", err, "email", email)
		s.showAuthError(w, r, "Failed to send email. Please try again or contact support.", "")
		return
	}

	// Show success page
	var devURL string
	if s.env.WebDev {
		devURL = verifyEmailURL
	}
	s.showAuthEmailSent(w, r, email, devURL)
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
	s.renderTemplate(w, "auth-error.html", data)
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

	s.renderTemplate(w, "email-sent.html", data)
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
