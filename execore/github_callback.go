package execore

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// handleGitHubCallback handles the GitHub App installation callback.
// GitHub redirects here after the user installs/authorizes the app.
//
// OAuth authorize flow sends: code, state (and optionally installation_id).
// App install flow sends: code, installation_id, setup_action — but NOT state.
// We handle both: state-based lookup for OAuth, fallback scan for installs.
func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

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

	// Look up and consume the pending setup.
	// OAuth flow: match by state. Install flow: state is absent, so scan
	// for a pending setup (GitHub's install redirect doesn't relay state).
	s.githubSetupsMu.Lock()
	var setup *GitHubSetup
	if state != "" {
		setup = s.githubSetups[state]
		delete(s.githubSetups, state)
	} else if installationID != 0 {
		// Install flow without state — find the single pending setup.
		for st, gs := range s.githubSetups {
			setup = gs
			delete(s.githubSetups, st)
			break
		}
	}
	s.githubSetupsMu.Unlock()

	if setup == nil {
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
		s.slog().ErrorContext(ctx, "GitHub user lookup failed", "error", err)
		setup.Err = fmt.Errorf("user lookup failed: %w", err)
		setup.Close()
		http.Error(w, "failed to look up GitHub user", http.StatusInternalServerError)
		return
	}

	// Fill in the setup and signal the SSH session.
	setup.GitHubLogin = login
	setup.InstallationID = installationID
	setup.AccessToken = tokenResp.AccessToken
	setup.RefreshToken = tokenResp.RefreshToken
	setup.Close()

	// Wait for the SSH session to decide the response.
	// It may redirect the browser (e.g., to the install URL) or show "Connected".
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
