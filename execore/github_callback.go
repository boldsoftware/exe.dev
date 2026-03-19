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
		// relay it for org-admin approvals). The SSH session detects new
		// installations via polling, so just show a friendly page.
		if installationID != 0 {
			s.renderTemplate(ctx, w, "github-installed.html", nil)
			return
		}
		http.Error(w, "unknown or expired setup — please try again from SSH", http.StatusBadRequest)
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

	// Web-initiated flow: save account and redirect to /user#github.
	if setup.WebFlow {
		if err := s.saveGitHubSetupWeb(ctx, setup); err != nil {
			s.slog().ErrorContext(ctx, "Failed to save GitHub connection", "error", err)
			http.Error(w, "Failed to save GitHub connection", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/user?callout=add-repo-integration#github", http.StatusFound)
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

	s.renderTemplate(r.Context(), w, "github-connected.html", struct {
		GitHubLogin string
	}{
		GitHubLogin: login,
	})
}
