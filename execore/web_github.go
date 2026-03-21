package execore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"exe.dev/exedb"
	"exe.dev/githubapp"
)

// handleGitHubInstall initiates the GitHub App install flow from the web UI.
// Creates a pending setup, then redirects the browser to GitHub's install page.
func (s *Server) handleGitHubInstall(w http.ResponseWriter, r *http.Request) {
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Redirect(w, r, "/auth?redirect=/user%23github", http.StatusTemporaryRedirect)
		return
	}

	setup, _, err := s.registerGitHubSetup(userID, true)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "GitHub install setup failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, s.githubApp.InstallURL(setup.State), http.StatusFound)
}

// handleGitHubSignin initiates the GitHub OAuth sign-in flow from the web UI.
// Used when the app is already installed and the user just needs to link their account.
func (s *Server) handleGitHubSignin(w http.ResponseWriter, r *http.Request) {
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Redirect(w, r, "/auth?redirect=/user%23github", http.StatusTemporaryRedirect)
		return
	}

	setup, _, err := s.registerGitHubSetup(userID, true)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "GitHub signin setup failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, s.githubApp.AuthorizeURL(setup.State), http.StatusFound)
}

// handleGitHubUnlink removes a GitHub account connection.
func (s *Server) handleGitHubUnlink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	var req struct {
		InstallationID int64 `json:"installation_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGitHubJSON(w, false, "Invalid request", nil)
		return
	}
	if req.InstallationID == 0 {
		writeGitHubJSON(w, false, "installation_id is required", nil)
		return
	}

	err = s.withTx(r.Context(), func(ctx context.Context, queries *exedb.Queries) error {
		return queries.DeleteGitHubAccount(ctx, exedb.DeleteGitHubAccountParams{
			UserID:         userID,
			InstallationID: req.InstallationID,
		})
	})
	if err != nil {
		writeGitHubJSON(w, false, "Failed to unlink", nil)
		return
	}

	writeGitHubJSON(w, true, "", nil)
}

// handleGitHubRepos returns repos accessible through GitHub installations.
// If installation_id is provided, returns repos for that installation only.
// Otherwise returns repos across all connected installations.
func (s *Server) handleGitHubRepos(w http.ResponseWriter, r *http.Request) {
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	accounts, err := withRxRes1(s, r.Context(), (*exedb.Queries).ListGitHubAccounts, userID)
	if err != nil {
		writeGitHubJSON(w, false, "Failed to look up accounts", nil)
		return
	}

	// If installation_id specified, filter to that one account.
	if idStr := r.URL.Query().Get("installation_id"); idStr != "" {
		installationID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeGitHubJSON(w, false, "invalid installation_id", nil)
			return
		}
		var found bool
		for _, a := range accounts {
			if a.InstallationID == installationID {
				accounts = []exedb.GithubAccount{a}
				found = true
				break
			}
		}
		if !found {
			writeGitHubJSON(w, false, "Installation not found", nil)
			return
		}
	}

	var allRepos []githubapp.Repository
	for _, acct := range accounts {
		accessToken, err := s.resolveGitHubTokenWeb(r.Context(), acct)
		if err != nil {
			continue // skip accounts with token issues
		}
		repos, err := s.githubApp.GetInstallationRepositories(r.Context(), accessToken, acct.InstallationID)
		if err != nil {
			continue
		}
		allRepos = append(allRepos, repos...)
	}

	writeGitHubJSON(w, true, "", allRepos)
}

// resolveGitHubTokenWeb returns the stored access token if it hasn't expired.
// Token refresh is handled by the background renewal loop, not here, to avoid
// concurrent refresh races that can invalidate the stored refresh token.
func (s *Server) resolveGitHubTokenWeb(ctx context.Context, acct exedb.GithubAccount) (string, error) {
	if acct.AccessTokenExpiresAt == nil {
		// No known expiry (e.g. token expiration not enabled on the GitHub App,
		// or legacy row). Return the token and let the caller handle API errors.
		return acct.AccessToken, nil
	}
	expires, err := parseGitHubTokenExpiry(*acct.AccessTokenExpiresAt)
	if err != nil {
		return "", fmt.Errorf("GitHub access token has unparseable expiry %q", *acct.AccessTokenExpiresAt)
	}
	if time.Now().Before(expires) {
		return acct.AccessToken, nil
	}
	return "", fmt.Errorf("GitHub access token expired, waiting for background refresh")
}

// saveGitHubSetupWeb saves a completed web-initiated GitHub setup to the database.
func (s *Server) saveGitHubSetupWeb(ctx context.Context, setup *GitHubSetup) error {
	if setup.InstallationID != 0 {
		targetLogin := setup.GitHubLogin
		if s.githubApp.InstallationTokensEnabled() {
			if acctLogin, err := s.githubApp.GetInstallationAccount(ctx, setup.InstallationID); err == nil {
				targetLogin = acctLogin
			}
		}
		return s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.UpsertGitHubAccount(ctx, exedb.UpsertGitHubAccountParams{
				UserID:                setup.UserID,
				GitHubLogin:           setup.GitHubLogin,
				InstallationID:        setup.InstallationID,
				TargetLogin:           targetLogin,
				AccessToken:           setup.AccessToken,
				RefreshToken:          setup.RefreshToken,
				AccessTokenExpiresAt:  setup.AccessTokenExpiresAt,
				RefreshTokenExpiresAt: setup.RefreshTokenExpiresAt,
			})
		})
	}

	// OAuth-only — discover installations via API.
	installs, err := s.githubApp.GetUserInstallations(ctx, setup.AccessToken)
	if err != nil {
		return fmt.Errorf("failed to discover installations: %w", err)
	}
	for _, inst := range installs {
		if err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.UpsertGitHubAccount(ctx, exedb.UpsertGitHubAccountParams{
				UserID:                setup.UserID,
				GitHubLogin:           setup.GitHubLogin,
				InstallationID:        inst.ID,
				TargetLogin:           inst.Account.Login,
				AccessToken:           setup.AccessToken,
				RefreshToken:          setup.RefreshToken,
				AccessTokenExpiresAt:  setup.AccessTokenExpiresAt,
				RefreshTokenExpiresAt: setup.RefreshTokenExpiresAt,
			})
		}); err != nil {
			return fmt.Errorf("failed to save connection: %w", err)
		}
	}
	return nil
}

// handleGitHubVerify tests the GitHub connection by listing repos for a specific installation.
func (s *Server) handleGitHubVerify(w http.ResponseWriter, r *http.Request) {
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	idStr := r.URL.Query().Get("installation_id")
	if idStr == "" {
		writeGitHubJSON(w, false, "installation_id is required", nil)
		return
	}
	installationID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeGitHubJSON(w, false, "invalid installation_id", nil)
		return
	}

	accounts, err := withRxRes1(s, r.Context(), (*exedb.Queries).ListGitHubAccounts, userID)
	if err != nil {
		writeGitHubJSON(w, false, "Failed to look up accounts", nil)
		return
	}
	var account *exedb.GithubAccount
	for _, a := range accounts {
		if a.InstallationID == installationID {
			account = &a
			break
		}
	}
	if account == nil {
		writeGitHubJSON(w, false, "Installation not found", nil)
		return
	}

	accessToken, err := s.resolveGitHubTokenWeb(r.Context(), *account)
	if err != nil {
		writeGitHubJSON(w, false, fmt.Sprintf("Token error: %v — try unlinking and reconnecting", err), nil)
		return
	}

	repos, err := s.githubApp.GetInstallationRepositories(r.Context(), accessToken, installationID)
	if err != nil {
		writeGitHubJSON(w, false, fmt.Sprintf("GitHub API error: %v", err), nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success":    true,
		"repo_count": len(repos),
	})
}

// parseGitHubTokenExpiry parses a token expiry string stored in the DB.
// Handles both SQLite format ("2006-01-02 15:04:05") and RFC 3339 ("2006-01-02T15:04:05Z").
func parseGitHubTokenExpiry(s string) (time.Time, error) {
	for _, fmt := range []string{"2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.Parse(fmt, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized format: %s", s)
}

func writeGitHubJSON(w http.ResponseWriter, success bool, errMsg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"success": success}
	if errMsg != "" {
		resp["error"] = errMsg
	}
	if data != nil {
		resp["repos"] = data
	}
	json.NewEncoder(w).Encode(resp)
}
