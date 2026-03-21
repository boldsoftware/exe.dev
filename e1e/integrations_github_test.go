package e1e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
)

// connectGitHubViaWeb connects a GitHub account through the web flow.
// It hits /github/signin to initiate OAuth, extracts the state from the
// redirect URL, then simulates the OAuth callback with the given code.
// Returns the callback's redirect Location (or empty if 200 OK).
func connectGitHubViaWeb(t *testing.T, cookies []*http.Cookie, code string) string {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	base := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)
	setCookiesForJar(t, jar, base, cookies)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Hit /github/signin — should redirect to GitHub OAuth URL with state.
	resp, err := client.Get(base + "/github/signin")
	if err != nil {
		t.Fatalf("signin request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 from /github/signin, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("invalid redirect URL: %v", err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatalf("no state in redirect URL: %s", loc)
	}

	// Simulate OAuth callback with the given code.
	return simulateGitHubOAuthCallback(t, code, state, 0)
}

// connectGitHubViaWebDefault connects a GitHub account using the default mock code.
func connectGitHubViaWebDefault(t *testing.T, cookies []*http.Cookie) {
	t.Helper()
	connectGitHubViaWeb(t, cookies, "mock-code")
}

// doGitHubOAuthCallback sends an OAuth callback and returns the HTTP status and Location header.
func doGitHubOAuthCallback(t *testing.T, code, state string, installationID int) (int, string) {
	t.Helper()
	callbackURL := fmt.Sprintf("http://localhost:%d/github/callback?code=%s&state=%s",
		Env.servers.Exed.HTTPPort, url.QueryEscape(code), url.QueryEscape(state))
	if installationID != 0 {
		callbackURL += fmt.Sprintf("&installation_id=%d", installationID)
	}
	resp, err := noRedirectClient(nil).Get(callbackURL)
	if err != nil {
		t.Fatalf("callback request failed: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location")
}

// simulateGitHubOAuthCallback sends an OAuth callback with the given code and state.
// The mock server will issue access token "ghu_<code>".
// If installationID is 0, no installation_id parameter is sent.
// Returns the redirect Location if the callback responded with a redirect, or empty string.
func simulateGitHubOAuthCallback(t *testing.T, code, state string, installationID int) string {
	t.Helper()
	status, loc := doGitHubOAuthCallback(t, code, state, installationID)
	switch status {
	case http.StatusOK:
		return ""
	case http.StatusFound:
		if loc == "" {
			t.Fatal("redirect with no Location header")
		}
		return loc
	default:
		t.Fatalf("callback returned %d", status)
		return ""
	}
}

// simulateGitHubInstallCallback sends a callback request simulating GitHub's
// redirect after an app installation (includes installation_id but no state
// parameter — matching GitHub's real behavior for org admin approvals).
func simulateGitHubInstallCallback(t *testing.T, installationID int) (statusCode int, body string) {
	t.Helper()
	callbackURL := fmt.Sprintf("http://localhost:%d/github/callback?code=install-code&installation_id=%d",
		Env.servers.Exed.HTTPPort, installationID)
	resp, err := noRedirectClient(nil).Get(callbackURL)
	if err != nil {
		t.Fatalf("callback request failed: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestIntegrationsSetupGitHub tests the GitHub App setup flow via the web UI:
// SSH command prints web URL → connect via web → list → delete → re-connect → delete.
func TestIntegrationsSetupGitHub(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, cookies, _, _ := registerForExeDev(t)

	// SSH setup command should enable the feature flag and print a web URL.
	pty.SendLine("integrations setup github")
	pty.Want("GitHub integration enabled")
	pty.Want("/user#github")
	pty.WantPrompt()

	// Connect a GitHub account via the web flow.
	connectGitHubViaWebDefault(t, cookies)

	// List shows the connection.
	pty.SendLine("integrations setup github --list")
	pty.Want("GitHub accounts:")
	pty.Want("testghuser")
	pty.WantPrompt()

	// Running setup again is idempotent — just prints the URL again.
	pty.SendLine("integrations setup github")
	pty.Want("GitHub integration enabled")
	pty.WantPrompt()

	// Connect again via web — upserts idempotently.
	connectGitHubViaWebDefault(t, cookies)

	pty.SendLine("integrations setup github --list")
	pty.Want("GitHub accounts:")
	pty.Want("testghuser")
	pty.WantPrompt()

	// Delete all connections.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected:")
	pty.WantPrompt()

	// Delete again: should error because no account connected.
	pty.SendLine("integrations setup github -d")
	pty.Want("no GitHub account connected")
	pty.WantPrompt()

	// Re-connect via web.
	connectGitHubViaWebDefault(t, cookies)

	pty.SendLine("integrations setup github --list")
	pty.Want("GitHub accounts:")
	pty.Want("testghuser")
	pty.WantPrompt()

	// Delete.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected:")
	pty.WantPrompt()

	pty.SendLine("integrations setup github --list")
	pty.Want("No GitHub accounts connected")
	pty.WantPrompt()
}

// TestIntegrationsSetupGitHubNoInstallations tests the web flow when the user
// has no GitHub App installations. The OAuth callback discovers no installations,
// so no accounts are saved.
func TestIntegrationsSetupGitHubNoInstallations(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Pre-configure: OAuth code "no-installs" → token "ghu_no-installs".
	// Start with no installations for this token.
	const code = "no-installs"
	const token = "ghu_" + code
	Env.servers.GitHubMock.SetInstallationsForToken(token, nil)

	pty, cookies, _, _ := registerForExeDev(t)

	// Enable the feature flag.
	pty.SendLine("integrations setup github")
	pty.Want("GitHub integration enabled")
	pty.WantPrompt()

	// Connect via web with the no-installs code — callback completes but
	// no installations are discovered, so no accounts are saved.
	connectGitHubViaWeb(t, cookies, code)

	// Should show no accounts.
	pty.SendLine("integrations setup github --list")
	pty.Want("No GitHub accounts connected")
	pty.WantPrompt()

	// Now add an installation and connect again — should discover it.
	Env.servers.GitHubMock.AddInstallationForToken(token, 99999, "new-org")
	connectGitHubViaWeb(t, cookies, code)

	pty.SendLine("integrations setup github --list")
	pty.Want("new-org")
	pty.WantPrompt()

	// Clean up.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected:")
	pty.WantPrompt()
}

// TestIntegrationsSetupGitHubOrg tests the web flow for a user who is part of
// an org that already has the app installed — both personal and org installations
// are discovered and synced.
func TestIntegrationsSetupGitHubOrg(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Pre-configure: token for code "org-user" has personal + org installations.
	const code = "org-user"
	const token = "ghu_" + code
	Env.servers.GitHubMock.SetInstallationsForToken(token, []testinfra.MockInstallation{
		{ID: 12345, Login: "testghuser"},
		{ID: 67890, Login: "test-org"},
	})

	pty, cookies, _, _ := registerForExeDev(t)

	// Enable the feature flag.
	pty.SendLine("integrations setup github")
	pty.Want("GitHub integration enabled")
	pty.WantPrompt()

	// Connect via web — discovers both installations.
	connectGitHubViaWeb(t, cookies, code)

	// Both should be synced.
	pty.SendLine("integrations setup github --list")
	pty.Want("GitHub accounts:")
	pty.Want("test-org")
	pty.WantPrompt()

	// Can add integration for the org's repos.
	pty.SendLine("integrations add github --name=orgtest --repository=test-org/repo")
	pty.Want("Added integration orgtest")
	pty.WantPrompt()

	// Clean up.
	pty.SendLine("integrations remove orgtest")
	pty.Want("Removed")
	pty.WantPrompt()

	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected:")
	pty.WantPrompt()
}

// TestIntegrationsGitHubOrphanInstallCallback tests that an install callback
// arriving after the SSH session has already completed shows a friendly page
// instead of an error.
func TestIntegrationsGitHubOrphanInstallCallback(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Send an install callback with no matching pending setup.
	// This simulates what happens when a user installs the app on an org
	// after the SSH session has already completed.
	status, body := simulateGitHubInstallCallback(t, 117180919)
	if status != http.StatusOK {
		t.Fatalf("expected 200 OK for orphan install callback, got %d: %s", status, body)
	}
	if !regexp.MustCompile(`(?i)installed`).MatchString(body) {
		t.Fatalf("expected friendly 'installed' page, got: %s", body)
	}
}

// TestIntegrationsGitHubOrphanOAuthCallback tests that an OAuth callback
// with a state that doesn't match any pending setup still returns an error.
func TestIntegrationsGitHubOrphanOAuthCallback(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// OAuth callback with an unknown state and no installation_id.
	callbackURL := fmt.Sprintf("http://localhost:%d/github/callback?code=mock-code&state=nonexistent-state",
		Env.servers.Exed.HTTPPort)
	resp, err := noRedirectClient(nil).Get(callbackURL)
	if err != nil {
		t.Fatalf("callback request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for orphan OAuth callback, got %d: %s", resp.StatusCode, string(body))
	}
	if !regexp.MustCompile(`unknown or expired`).MatchString(string(body)) {
		t.Fatalf("expected 'unknown or expired' error, got: %s", string(body))
	}
}

// TestIntegrationsSetupGitHubWrongAccount tests the web flow when a user
// picks the wrong GitHub account. The OAuth callback returns 401, then the
// user retries with the correct account.
func TestIntegrationsSetupGitHubWrongAccount(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// The first OAuth code will produce a token that returns 401 on /user.
	const badCode = "wrong-acct"
	const badToken = "ghu_" + badCode
	Env.servers.GitHubMock.SetUserForToken(badToken, "") // empty = 401

	pty, cookies, _, _ := registerForExeDev(t)

	// Enable the feature flag.
	pty.SendLine("integrations setup github")
	pty.Want("GitHub integration enabled")
	pty.WantPrompt()

	// Initiate web signin flow with the bad code.
	jar, _ := cookiejar.New(nil)
	base := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)
	setCookiesForJar(t, jar, base, cookies)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(base + "/github/signin")
	if err != nil {
		t.Fatalf("signin request failed: %v", err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("invalid redirect URL: %v", err)
	}
	state := u.Query().Get("state")

	// Simulate OAuth callback with the bad token — server gets 401 on /user.
	status, _ := doGitHubOAuthCallback(t, badCode, state, 0)
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 from callback with bad token, got %d", status)
	}

	// Retry with the correct account via a new web flow.
	connectGitHubViaWebDefault(t, cookies)

	// Verify the correct account is connected via SSH --list.
	pty.SendLine("integrations setup github --list")
	pty.Want("GitHub accounts:")
	pty.Want("testghuser")
	pty.WantPrompt()

	// Clean up.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected:")
	pty.WantPrompt()
}

// TestIntegrationsVerifyGitHub tests that --verify checks both OAuth token
// validity AND whether the GitHub App installation is still active.
func TestIntegrationsVerifyGitHub(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Pre-configure: custom code/token with one installation.
	const code = "verify-test"
	const token = "ghu_" + code
	installs := Env.servers.GitHubMock.SetInstallationsForToken(token, []testinfra.MockInstallation{
		{ID: 55555, Login: "verify-user"},
	})

	pty, cookies, _, _ := registerForExeDev(t)

	// Enable the feature flag and connect via web.
	pty.SendLine("integrations setup github")
	pty.Want("GitHub integration enabled")
	pty.WantPrompt()

	connectGitHubViaWeb(t, cookies, code)

	// Verify: should pass — token is valid and installation exists.
	pty.SendLine("integrations setup github --verify")
	pty.WantRE(`verify-user.*✓`)
	pty.WantPrompt()

	// Simulate uninstalling the GitHub App: remove the installation.
	*installs = nil

	// Verify again: token is still valid but installation is gone.
	pty.SendLine("integrations setup github --verify")
	pty.WantRE(`verify-user.*✗`)
	pty.Want("app not installed")
	pty.WantPrompt()

	// Clean up.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected:")
	pty.WantPrompt()
}

// TestIntegrationsAddGitHub tests the GitHub integration add/list/remove flow
// and validates error cases.
func TestIntegrationsAddGitHub(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, cookies, _, _ := registerForExeDev(t)

	// Error: add github without a GitHub account connected.
	pty.SendLine("integrations add github --name=ghtest --repository=testghuser/empty")
	pty.Want("no GitHub account connected")
	pty.WantPrompt()

	// Enable the feature flag and connect via web.
	pty.SendLine("integrations setup github")
	pty.Want("GitHub integration enabled")
	pty.WantPrompt()

	connectGitHubViaWebDefault(t, cookies)

	// Error: missing --repository.
	pty.SendLine("integrations add github --name=ghtest")
	pty.Want("--repository is required")
	pty.WantPrompt()

	// Error: missing --name.
	pty.SendLine("integrations add github --repository=testghuser/empty")
	pty.Want("--name is required")
	pty.WantPrompt()

	// Error: bad repository format (no slash).
	pty.SendLine("integrations add github --name=ghtest --repository=noslash")
	pty.Want("owner/repo")
	pty.WantPrompt()

	// Error: repo owner doesn't match any installation.
	pty.SendLine("integrations add github --name=ghtest --repository=unknown-org/repo")
	pty.Want("no GitHub App installed on")
	pty.WantPrompt()

	// Successfully add a github integration.
	pty.SendLine("integrations add github --name=ghtest --repository=testghuser/empty")
	pty.Want("Added integration ghtest")
	pty.WantPrompt()

	// List should show the integration with repos summary.
	pty.SendLine("integrations list")
	pty.Want("ghtest")
	pty.Want("github")
	pty.Want("repos=testghuser/empty")
	pty.WantPrompt()

	// Remove the integration.
	pty.SendLine("integrations remove ghtest")
	pty.Want("Removed integration ghtest")
	pty.WantPrompt()

	// Clean up GitHub account.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected:")
	pty.WantPrompt()
}

// TestIntegrationsGitHubTokenRefresh tests that refreshing a GitHub token
// works correctly with the normalized data model (one token row per user,
// separate installation rows). With token rotation enabled, each refresh
// invalidates the old refresh token. After refreshing, all installations
// remain accessible because they share the single token row.
func TestIntegrationsGitHubTokenRefresh(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Enable strict token rotation on the mock so that each refresh
	// invalidates the old refresh token.
	Env.servers.GitHubMock.EnableTokenRotation()

	// OAuth code "refresh-test" → access token "ghu_refresh-test".
	// Configure the token to discover 2 installations (personal + org).
	// Set expires_in=1 so the debug refresh endpoint doesn't skip
	// the refresh due to the "already fresh" check.
	const code = "refresh-test"
	const token = "ghu_" + code
	Env.servers.GitHubMock.SetExpiresInForToken(token, 1)
	Env.servers.GitHubMock.SetInstallationsForToken(token, []testinfra.MockInstallation{
		{ID: 80001, Login: "refresh-user"},
		{ID: 80002, Login: "refresh-org"},
	})

	pty, cookies, _, email := registerForExeDev(t)

	// Enable the feature flag.
	pty.SendLine("integrations setup github")
	pty.Want("GitHub integration enabled")
	pty.WantPrompt()

	// Connect via web — discovers both installations, saves token once
	// and creates two installation rows.
	connectGitHubViaWeb(t, cookies, code)

	// Verify both installations are stored.
	pty.SendLine("integrations setup github --list")
	pty.Want("GitHub accounts:")
	pty.Want("refresh-user")
	pty.WantPrompt()

	pty.SendLine("integrations setup github --list")
	pty.Want("refresh-org")
	pty.WantPrompt()

	// Look up the user_id so we can call the debug refresh endpoint.
	userID := getUserIDByEmail(t, email)

	base := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)

	// Refresh the user's token via the debug endpoint.
	// The mock will rotate the refresh token: old token is invalidated,
	// new token is issued.
	resp := postRefresh(t, base, userID, "testghuser")
	if resp != "OK" {
		t.Fatalf("refresh failed: %s", resp)
	}

	// A second refresh should see the token is already fresh.
	resp = postRefresh(t, base, userID, "testghuser")
	if resp != "OK" && resp != "OK (already fresh)" {
		t.Fatalf("second refresh failed: %s", resp)
	}

	// Verify both installations still list correctly after the refresh.
	pty.SendLine("integrations setup github --list")
	pty.Want("GitHub accounts:")
	pty.Want("refresh-user")
	pty.WantPrompt()

	pty.SendLine("integrations setup github --list")
	pty.Want("refresh-org")
	pty.WantPrompt()

	// Clean up.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected:")
	pty.WantPrompt()
}

// TestIntegrationsGitHubOnDemandRefresh tests that web/SSH operations
// automatically refresh expired access tokens on demand.
func TestIntegrationsGitHubOnDemandRefresh(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Enable token rotation with expires_in=1 so the access token
	// expires immediately. The on-demand refresh in resolveGitHubTokenWeb
	// should transparently refresh the token when the web UI tries to use it.
	Env.servers.GitHubMock.EnableTokenRotation()

	const code = "ondemand-test"
	const token = "ghu_" + code
	// Set expires_in=1 so the access token expires immediately,
	// forcing on-demand refresh when the web/SSH path tries to use it.
	Env.servers.GitHubMock.SetExpiresInForToken(token, 1)
	Env.servers.GitHubMock.SetInstallationsForToken(token, []testinfra.MockInstallation{
		{ID: 90001, Login: "ondemand-user"},
	})

	pty, cookies, _, _ := registerForExeDev(t)

	// Enable the feature flag and connect via web.
	pty.SendLine("integrations setup github")
	pty.Want("GitHub integration enabled")
	pty.WantPrompt()

	connectGitHubViaWeb(t, cookies, code)

	// The access token expired immediately (expires_in=1). Wait to ensure
	// the 1-second access token has definitely expired.
	time.Sleep(2 * time.Second)

	// Verify via SSH should still work because resolveGitHubTokenWeb
	// refreshes the expired access token on demand.
	pty.SendLine("integrations setup github --verify")
	pty.WantRE(`ondemand-user.*✓`)
	pty.WantPrompt()

	// Clean up.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected:")
	pty.WantPrompt()
}

// postRefresh calls the debug refresh endpoint and returns the response body.
func postRefresh(t *testing.T, base, userID, githubLogin string) string {
	t.Helper()
	resp, err := http.PostForm(
		base+"/debug/github-integrations/refresh",
		url.Values{
			"user_id":      {userID},
			"github_login": {githubLogin},
		},
	)
	if err != nil {
		t.Fatalf("refresh request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("HTTP %d: %s", resp.StatusCode, body)
	}
	return string(body)
}

// TestIntegrationsGitHubReinstall tests that reinstalling a GitHub App on the
// same org (which gives a new installation_id) works without UNIQUE constraint
// violations. The new installation should replace the old one.
func TestIntegrationsGitHubReinstall(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Pre-configure: first install with installation_id 11111.
	const code1 = "reinstall-v1"
	const token1 = "ghu_" + code1
	Env.servers.GitHubMock.SetInstallationsForToken(token1, []testinfra.MockInstallation{
		{ID: 11111, Login: "myorg"},
	})

	pty, cookies, _, _ := registerForExeDev(t)

	pty.SendLine("integrations setup github")
	pty.Want("GitHub integration enabled")
	pty.WantPrompt()

	// First install.
	connectGitHubViaWeb(t, cookies, code1)

	pty.SendLine("integrations setup github --list")
	pty.Want("GitHub accounts:")
	pty.Want("myorg")
	pty.WantPrompt()

	// Simulate reinstall: same org, new installation_id 22222.
	const code2 = "reinstall-v2"
	const token2 = "ghu_" + code2
	Env.servers.GitHubMock.SetInstallationsForToken(token2, []testinfra.MockInstallation{
		{ID: 22222, Login: "myorg"},
	})

	// Connect again — should replace the old installation without error.
	connectGitHubViaWeb(t, cookies, code2)

	// Should still show exactly one "myorg" entry.
	pty.SendLine("integrations setup github --list")
	pty.Want("GitHub accounts:")
	pty.Want("myorg")
	pty.WantPrompt()

	// Clean up.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected:")
	pty.WantPrompt()
}
