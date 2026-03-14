package execore

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"time"
)

// handleGitHubCallback handles the GitHub App installation callback.
// GitHub redirects here after the user installs/authorizes the app.
// Query params: code, installation_id, state.
func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	code := q.Get("code")
	installationIDStr := q.Get("installation_id")
	state := q.Get("state")

	if code == "" || state == "" {
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
	// Consuming immediately prevents replay of the callback.
	s.githubSetupsMu.Lock()
	setup := s.githubSetups[state]
	delete(s.githubSetups, state)
	s.githubSetupsMu.Unlock()

	if setup == nil {
		http.Error(w, "unknown or expired state parameter — please try again from SSH", http.StatusBadRequest)
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>GitHub Connected</title></head>
<body style="font-family: system-ui, sans-serif; max-width: 480px; margin: 80px auto; text-align: center;">
<h1>GitHub Connected</h1>
<p>Connected as <strong>%s</strong>. You can close this tab and return to your terminal.</p>
</body></html>`, html.EscapeString(login))
}
