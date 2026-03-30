package execore

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"exe.dev/githubapp"
)

// handleGitHubCallback handles the GitHub OAuth/install callback.
// OAuth authorize flow: matched by state parameter.
// App install flow: no pending setup expected (the SSH session uses polling
// to detect new installations); orphan install callbacks show a friendly page.
func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	// GitHub sends error/error_description when the user denies the OAuth
	// request (e.g. error=access_denied). Signal the waiting SSH session
	// so it doesn't hang until the 10-minute timeout.
	if errCode := q.Get("error"); errCode != "" {
		state := q.Get("state")
		s.githubSetupsMu.Lock()
		setup := s.githubSetups[state]
		delete(s.githubSetups, state)
		s.githubSetupsMu.Unlock()

		desc := q.Get("error_description")
		if desc == "" {
			desc = errCode
		}
		if setup != nil {
			setup.Err = errors.New(desc)
			setup.Close()
		}
		s.slog().WarnContext(ctx, "GitHub OAuth error", "error", errCode, "description", desc, "state", state)
		http.Error(w, "GitHub authorization was denied. You may close this tab.", http.StatusForbidden)
		return
	}

	code := q.Get("code")
	installationIDStr := q.Get("installation_id")
	state := q.Get("state")

	if code == "" {
		http.Error(w, "missing required parameters", http.StatusBadRequest)
		return
	}

	var installationID int64
	if installationIDStr != "" {
		var err error
		installationID, err = strconv.ParseInt(installationIDStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid installation_id", http.StatusBadRequest)
			return
		}
	}

	// Look up and consume the pending setup by state.
	s.githubSetupsMu.Lock()
	var setup *GitHubSetup
	if state != "" {
		setup = s.githubSetups[state]
		delete(s.githubSetups, state)
	}
	s.githubSetupsMu.Unlock()

	if setup == nil {
		// Install callbacks arrive without state (GitHub doesn't reliably
		// relay it for org-admin approvals, and the Step 1 install link
		// goes directly to GitHub without state).
		if installationID != 0 {
			// If the user is logged in and we have a code, exchange it
			// and save the installation so they land back on integrations
			// with everything linked.
			if userID, err := s.validateAuthCookie(r); err == nil && code != "" {
				orphanSetup := &GitHubSetup{
					UserID:         userID,
					WebFlow:        true,
					InstallationID: installationID,
				}
				tokenResp, err := s.githubApp.ExchangeCode(ctx, code)
				if err == nil {
					login, err := s.githubApp.GetUser(ctx, tokenResp.AccessToken)
					if err == nil {
						orphanSetup.GitHubLogin = login
						orphanSetup.AccessToken = tokenResp.AccessToken
						orphanSetup.RefreshToken = tokenResp.RefreshToken
						orphanSetup.AccessTokenExpiresAt = tokenResp.AccessTokenExpiresAt()
						orphanSetup.RefreshTokenExpiresAt = tokenResp.RefreshTokenExpiresAt()
						if err := s.saveGitHubSetupWeb(ctx, orphanSetup); err != nil {
							s.slog().ErrorContext(ctx, "Failed to save orphan GitHub install", "error", err)
						} else {
							http.Redirect(w, r, "/integrations?callout=github-connected#github", http.StatusFound)
							return
						}
					}
				}
				// If any step failed, fall through to redirect without saving.
				s.slog().WarnContext(ctx, "Orphan install: token exchange or user lookup failed, redirecting anyway")
			}
			// No auth cookie or code exchange failed — just redirect to integrations.
			// The SSH session detects new installations via polling.
			http.Redirect(w, r, "/integrations#github", http.StatusFound)
			return
		}
		http.Error(w, "unknown or expired setup — please try again", http.StatusBadRequest)
		return
	}

	// Exchange the code for a user access token.
	tokenResp, err := s.githubApp.ExchangeCode(ctx, code)
	if err != nil {
		s.slog().ErrorContext(ctx, "GitHub token exchange failed", "error", err)
		setup.Err = fmt.Errorf("token exchange failed: %w", err)
		setup.Close()
		http.Error(w, "failed to exchange authorization code", http.StatusInternalServerError)
		return
	}

	// Look up the GitHub user.
	login, err := s.githubApp.GetUser(ctx, tokenResp.AccessToken)
	if err != nil {
		if githubapp.IsAuthError(err) {
			s.slog().WarnContext(ctx, "GitHub user lookup auth failure (may be wrong account)", "error", err)
		} else {
			s.slog().ErrorContext(ctx, "GitHub user lookup failed", "error", err)
		}
		setup.Err = fmt.Errorf("user lookup failed: %w", err)
		setup.Close()
		if githubapp.IsAuthError(err) {
			http.Error(w, "GitHub authorization failed \u2014 you may have selected the wrong account. Return to your terminal to try again.", http.StatusUnauthorized)
		} else {
			http.Error(w, "failed to look up GitHub user", http.StatusInternalServerError)
		}
		return
	}

	// Fill in the setup.
	setup.GitHubLogin = login
	setup.InstallationID = installationID
	setup.AccessToken = tokenResp.AccessToken
	setup.RefreshToken = tokenResp.RefreshToken
	setup.AccessTokenExpiresAt = tokenResp.AccessTokenExpiresAt()
	setup.RefreshTokenExpiresAt = tokenResp.RefreshTokenExpiresAt()

	// Web-initiated flow: save account and redirect.
	if setup.WebFlow {
		// If we have an installation_id (from install flow), save everything.
		// Otherwise, check if the app is already installed for this user.
		if setup.InstallationID != 0 {
			if err := s.saveGitHubSetupWeb(ctx, setup); err != nil {
				s.slog().ErrorContext(ctx, "Failed to save GitHub connection", "error", err)
				http.Error(w, "Failed to save GitHub connection", http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, "/integrations?callout=github-connected#github", http.StatusFound)
			return
		}

		// OAuth-only callback (no installation_id). Check if the app is
		// installed for this user by listing their accessible installations.
		installs, err := s.githubApp.GetUserInstallations(ctx, setup.AccessToken)
		if err != nil {
			s.slog().WarnContext(ctx, "GitHub: failed to list user installations", "error", err, "login", login)
			// Fall through — treat as no installations.
		}

		if len(installs) > 0 {
			// App is already installed. Save token + discovered installations.
			if err := s.saveGitHubSetupWeb(ctx, setup); err != nil {
				s.slog().ErrorContext(ctx, "Failed to save GitHub connection", "error", err)
				http.Error(w, "Failed to save GitHub connection", http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, "/integrations?callout=github-connected#github", http.StatusFound)
			return
		}

		// App is NOT installed for this user. Save the OAuth token so the
		// user's GitHub identity is preserved, then redirect to the GitHub
		// App installation page. When they finish installing, GitHub will
		// callback with installation_id + a new code.
		if err := s.saveGitHubSetupWeb(ctx, setup); err != nil {
			s.slog().ErrorContext(ctx, "Failed to save GitHub token", "error", err)
			http.Error(w, "Failed to save GitHub connection", http.StatusInternalServerError)
			return
		}

		// Create a new setup for the install callback.
		installSetup, _, err := s.registerGitHubSetup(setup.UserID, true)
		if err != nil {
			s.slog().ErrorContext(ctx, "GitHub install setup failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		s.slog().InfoContext(ctx, "GitHub: app not installed, redirecting to install", "login", login)
		http.Redirect(w, r, s.githubApp.InstallURL(installSetup.State), http.StatusFound)
		return
	}

	// SSH-initiated flow: signal the waiting SSH session.
	setup.Close()

	// Wait for the SSH session to decide the response.
	var redirectURL string
	select {
	case redirectURL = <-setup.RespondCh:
	case <-time.After(30 * time.Second):
	}

	if redirectURL != "" {
		http.Redirect(w, r, redirectURL, http.StatusFound)
		return
	}

	s.renderPage(r.Context(), w, "pages/github-connected.html", GithubConnectedPage{
		GitHubLogin: login,
	})
}
