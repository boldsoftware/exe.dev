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

// handleGitHubUnlink removes a GitHub installation connection.
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
		if err := queries.DeleteGitHubInstallation(ctx, exedb.DeleteGitHubInstallationParams{
			UserID:                  userID,
			GitHubAppInstallationID: req.InstallationID,
		}); err != nil {
			return err
		}
		// Clean up tokens that no longer have any installations.
		return queries.DeleteOrphanedGitHubUserTokens(ctx, userID)
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

	installations, err := withRxRes1(s, r.Context(), (*exedb.Queries).ListGitHubInstallations, userID)
	if err != nil {
		writeGitHubJSON(w, false, "Failed to look up installations", nil)
		return
	}

	// If installation_id specified, filter to that one installation.
	if idStr := r.URL.Query().Get("installation_id"); idStr != "" {
		installationID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeGitHubJSON(w, false, "invalid installation_id", nil)
			return
		}
		var found bool
		for _, inst := range installations {
			if inst.GitHubAppInstallationID == installationID {
				installations = []exedb.GithubInstallation{inst}
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
	for _, inst := range installations {
		accessToken, err := s.resolveGitHubTokenWeb(r.Context(), userID, inst.GitHubLogin)
		if err != nil {
			continue
		}
		repos, err := s.githubApp.GetInstallationRepositories(r.Context(), accessToken, inst.GitHubAppInstallationID)
		if err != nil {
			continue
		}
		allRepos = append(allRepos, repos...)
	}

	writeGitHubJSON(w, true, "", allRepos)
}

// resolveGitHubTokenWeb returns a valid access token for the given user.
// If the stored access token has expired, it refreshes on demand using the
// refresh token (under the refresh mutex to prevent concurrent rotation).
func (s *Server) resolveGitHubTokenWeb(ctx context.Context, userID, githubLogin string) (string, error) {
	tok, err := withRxRes1(s, ctx, (*exedb.Queries).GetGitHubUserToken, exedb.GetGitHubUserTokenParams{
		UserID:      userID,
		GitHubLogin: githubLogin,
	})
	if err != nil {
		return "", fmt.Errorf("no GitHub token for user: %w", err)
	}

	if tok.AccessTokenExpiresAt == nil {
		return tok.AccessToken, nil
	}
	expires, err := parseGitHubTokenExpiry(*tok.AccessTokenExpiresAt)
	if err != nil {
		return "", fmt.Errorf("GitHub access token has unparseable expiry %q", *tok.AccessTokenExpiresAt)
	}
	if time.Now().Before(expires) {
		return tok.AccessToken, nil
	}

	// Access token is expired. Refresh on demand.
	if tok.RefreshToken == "" {
		return "", fmt.Errorf("GitHub access token expired and no refresh token available")
	}

	s.githubRefreshMu.Lock()
	defer s.githubRefreshMu.Unlock()

	// Re-read the token under the lock — another goroutine may have
	// already refreshed it.
	fresh, err := withRxRes1(s, ctx, (*exedb.Queries).GetGitHubUserToken, exedb.GetGitHubUserTokenParams{
		UserID:      userID,
		GitHubLogin: githubLogin,
	})
	if err != nil {
		return "", fmt.Errorf("failed to re-read GitHub token: %w", err)
	}
	if fresh.AccessTokenExpiresAt != nil {
		if exp, err := parseGitHubTokenExpiry(*fresh.AccessTokenExpiresAt); err == nil && time.Now().Before(exp) {
			return fresh.AccessToken, nil
		}
	}

	// Still expired — do the refresh.
	tokenResp, err := s.githubApp.RefreshUserToken(ctx, fresh.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("GitHub token refresh failed: %w", err)
	}

	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpdateGitHubUserToken(ctx, exedb.UpdateGitHubUserTokenParams{
			AccessToken:           tokenResp.AccessToken,
			RefreshToken:          tokenResp.RefreshToken,
			AccessTokenExpiresAt:  tokenResp.AccessTokenExpiresAt(),
			RefreshTokenExpiresAt: tokenResp.RefreshTokenExpiresAt(),
			UserID:                userID,
			GitHubLogin:           githubLogin,
		})
	})
	if err != nil {
		return "", fmt.Errorf("failed to save refreshed GitHub tokens: %w", err)
	}

	s.slog().InfoContext(ctx, "on-demand GitHub token refresh", "user_id", userID)
	return tokenResp.AccessToken, nil
}

// saveGitHubSetupWeb saves a completed web-initiated GitHub setup to the database.
func (s *Server) saveGitHubSetupWeb(ctx context.Context, setup *GitHubSetup) error {
	// Always upsert the user's OAuth token (one row per user).
	if err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpsertGitHubUserToken(ctx, exedb.UpsertGitHubUserTokenParams{
			UserID:                setup.UserID,
			GitHubLogin:           setup.GitHubLogin,
			AccessToken:           setup.AccessToken,
			RefreshToken:          setup.RefreshToken,
			AccessTokenExpiresAt:  setup.AccessTokenExpiresAt,
			RefreshTokenExpiresAt: setup.RefreshTokenExpiresAt,
		})
	}); err != nil {
		return fmt.Errorf("failed to save token: %w", err)
	}

	if setup.InstallationID != 0 {
		targetLogin := setup.GitHubLogin
		if s.githubApp.InstallationTokensEnabled() {
			if acctLogin, err := s.githubApp.GetInstallationAccount(ctx, setup.InstallationID); err == nil {
				targetLogin = acctLogin
			}
		}
		return s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			// Remove any stale installation for the same target account
			// (e.g., after uninstall/reinstall gives a new installation ID).
			if err := queries.DeleteGitHubInstallationByTarget(ctx, exedb.DeleteGitHubInstallationByTargetParams{
				UserID:                  setup.UserID,
				GitHubAccountLogin:      targetLogin,
				GitHubAppInstallationID: setup.InstallationID,
			}); err != nil {
				return fmt.Errorf("failed to clean stale installation: %w", err)
			}
			return queries.UpsertGitHubInstallation(ctx, exedb.UpsertGitHubInstallationParams{
				UserID:                  setup.UserID,
				GitHubLogin:             setup.GitHubLogin,
				GitHubAppInstallationID: setup.InstallationID,
				GitHubAccountLogin:      targetLogin,
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
			// Remove any stale installation for the same target account.
			if err := queries.DeleteGitHubInstallationByTarget(ctx, exedb.DeleteGitHubInstallationByTargetParams{
				UserID:                  setup.UserID,
				GitHubAccountLogin:      inst.Account.Login,
				GitHubAppInstallationID: inst.ID,
			}); err != nil {
				return fmt.Errorf("failed to clean stale installation: %w", err)
			}
			return queries.UpsertGitHubInstallation(ctx, exedb.UpsertGitHubInstallationParams{
				UserID:                  setup.UserID,
				GitHubLogin:             setup.GitHubLogin,
				GitHubAppInstallationID: inst.ID,
				GitHubAccountLogin:      inst.Account.Login,
			})
		}); err != nil {
			return fmt.Errorf("failed to save installation: %w", err)
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

	// Verify the installation exists for this user.
	inst, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetGitHubInstallation, exedb.GetGitHubInstallationParams{
		UserID:                  userID,
		GitHubAppInstallationID: installationID,
	})
	if err != nil {
		writeGitHubJSON(w, false, "Installation not found", nil)
		return
	}

	accessToken, err := s.resolveGitHubTokenWeb(r.Context(), userID, inst.GitHubLogin)
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

// fetchGitHubAccountDisplayInfo loads installations and the user's GitHub login
// for rendering in the web UI. Returns display-only and full info slices.
func (s *Server) fetchGitHubAccountDisplayInfo(ctx context.Context, userID string) ([]GitHubAccountDisplayInfo, []GitHubAccountFullInfo) {
	installations, err := withRxRes1(s, ctx, (*exedb.Queries).ListGitHubInstallations, userID)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to get GitHub installations", "error", err, "user_id", userID)
		return nil, nil
	}
	var display []GitHubAccountDisplayInfo
	var full []GitHubAccountFullInfo
	for _, inst := range installations {
		display = append(display, GitHubAccountDisplayInfo{
			GitHubLogin: inst.GitHubLogin,
			TargetLogin: inst.GitHubAccountLogin,
		})
		full = append(full, GitHubAccountFullInfo{
			GitHubLogin:    inst.GitHubLogin,
			TargetLogin:    inst.GitHubAccountLogin,
			InstallationID: inst.GitHubAppInstallationID,
		})
	}
	return display, full
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
