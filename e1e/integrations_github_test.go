package e1e

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
)

// extractRedirectKey extracts the /r/<key> from a URL in PTY output.
// The key is the state token used as the redirect key.
func extractRedirectKey(t *testing.T, output string) string {
	t.Helper()
	re := regexp.MustCompile(`/r/([0-9a-f]{32})`)
	matches := re.FindStringSubmatch(output)
	if len(matches) < 2 {
		t.Fatalf("could not extract redirect key from output: %s", output)
	}
	return matches[1]
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

// simulateGitHubCallback is a convenience wrapper for simulateGitHubOAuthCallback
// using the default "mock-code" code.
func simulateGitHubCallback(t *testing.T, state string, installationID int) string {
	t.Helper()
	return simulateGitHubOAuthCallback(t, "mock-code", state, installationID)
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

// TestIntegrationsSetupGitHub tests the GitHub App setup flow:
// authorize → discover installations → connect → browser redirected to install page.
// Then: delete → verify delete-again error → re-setup → delete.
func TestIntegrationsSetupGitHub(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	// First setup: authorize flow discovers installation via API.
	pty.SendLine("integrations setup github")
	out := pty.WantREMatch(`Authorize your GitHub account`)
	out = pty.WantREMatch(`/r/[0-9a-f]{32}`)
	state := extractRedirectKey(t, out)

	// Simulate OAuth callback (no installation_id — authorize flow).
	// After syncing installations, the authorize callback's browser is
	// redirected to the install page for additional accounts.
	loc := simulateGitHubCallback(t, state, 0)
	if loc == "" {
		t.Fatal("expected redirect to install page after connecting existing installations")
	}

	// Server discovers installation 12345 via API and connects it.
	pty.Want("Connected:")
	pty.Want("testghuser")
	pty.Want("integrations setup github")
	pty.WantPrompt()

	// List shows the connection.
	pty.SendLine("integrations setup github --list")
	pty.Want("GitHub accounts:")
	pty.Want("testghuser")
	pty.WantPrompt()

	// Setup again: authorize discovers same installation, upserts idempotently.
	pty.SendLine("integrations setup github")
	out = pty.WantREMatch(`Authorize your GitHub account`)
	out = pty.WantREMatch(`/r/[0-9a-f]{32}`)
	state = extractRedirectKey(t, out)

	loc = simulateGitHubCallback(t, state, 0)
	if loc == "" {
		t.Fatal("expected redirect to install page")
	}
	pty.Want("Connected:")
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

	// Re-setup: authorize discovers installation again.
	pty.SendLine("integrations setup github")
	out = pty.WantREMatch(`/r/[0-9a-f]{32}`)
	state = extractRedirectKey(t, out)

	simulateGitHubCallback(t, state, 0)
	pty.Want("Connected:")
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

// TestIntegrationsSetupGitHubNoInstallations tests the flow when a user has
// no installations: OAuth authorize → no installations found → redirect to
// install page → poll detects new installation → connected.
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

	pty, _, _, _ := registerForExeDev(t)

	pty.SendLine("integrations setup github")
	out := pty.WantREMatch(`Authorize your GitHub account`)
	out = pty.WantREMatch(`/r/[0-9a-f]{32}`)
	state := extractRedirectKey(t, out)

	// OAuth callback with our custom code — returns empty installations.
	loc := simulateGitHubOAuthCallback(t, code, state, 0)

	// Server should detect no installations and redirect browser to install page.
	pty.Want("No GitHub App installations found")
	pty.Want("Install the app in your browser")

	// The browser should be redirected to the install page.
	if loc == "" {
		t.Fatal("expected redirect to install page")
	}
	if !regexp.MustCompile(`installations/new`).MatchString(loc) {
		t.Fatalf("expected redirect to installations/new, got: %s", loc)
	}

	// Simulate the user installing the app: add installation for this token.
	// The polling loop should pick this up.
	time.Sleep(1 * time.Second) // let the poller do one empty poll
	Env.servers.GitHubMock.AddInstallationForToken(token, 99999, "new-org")

	// SSH session should detect the new installation via polling.
	pty.Want("Connected:")
	pty.Want("new-org")
	pty.WantPrompt()

	// Verify it's listed.
	pty.SendLine("integrations setup github --list")
	pty.Want("new-org")
	pty.WantPrompt()

	// Clean up.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected:")
	pty.WantPrompt()
}

// TestIntegrationsSetupGitHubOrg tests the flow for a user who is part of an
// org that already has the app installed — both personal and org installations
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

	pty, _, _, _ := registerForExeDev(t)

	pty.SendLine("integrations setup github")
	out := pty.WantREMatch(`/r/[0-9a-f]{32}`)
	state := extractRedirectKey(t, out)

	// OAuth callback discovers both installations.
	loc := simulateGitHubOAuthCallback(t, code, state, 0)
	if loc == "" {
		t.Fatal("expected redirect to install page")
	}

	// Both should be synced.
	pty.Want("Connected:")
	// Check for test-org first since "testghuser" appears in both lines
	// ("testghuser" and "test-org (via testghuser)").
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

// TestIntegrationsSetupGitHubWrongAccount tests the retry flow when a user
// picks the wrong GitHub account from the browser's account chooser.
// The first OAuth attempt returns 401 (wrong account), then the user retries
// and picks the correct account.
func TestIntegrationsSetupGitHubWrongAccount(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// The first OAuth code will produce a token that returns 401 on /user.
	const badCode = "wrong-acct"
	const badToken = "ghu_" + badCode
	Env.servers.GitHubMock.SetUserForToken(badToken, "") // empty = 401

	pty, _, _, _ := registerForExeDev(t)

	pty.SendLine("integrations setup github")
	out := pty.WantREMatch(`Authorize your GitHub account`)
	out = pty.WantREMatch(`/r/[0-9a-f]{32}`)
	state1 := extractRedirectKey(t, out)

	// Simulate OAuth callback with the bad token — server gets 401 on /user.
	status, _ := doGitHubOAuthCallback(t, badCode, state1, 0)
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 from callback with bad token, got %d", status)
	}

	// SSH session should show retry message and a new URL.
	pty.Want("Authorization failed")
	pty.Want("wrong GitHub account")
	out = pty.WantREMatch(`Try again`)
	out = pty.WantREMatch(`/r/[0-9a-f]{32}`)
	state2 := extractRedirectKey(t, out)

	if state1 == state2 {
		t.Fatal("retry should generate a new state token")
	}

	// Second attempt: use the default code which returns testghuser.
	loc := simulateGitHubCallback(t, state2, 0)
	if loc == "" {
		t.Fatal("expected redirect to install page after connecting")
	}

	pty.Want("Connected:")
	pty.Want("testghuser")
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

	pty, _, _, _ := registerForExeDev(t)

	// Error: add github without a GitHub account connected.
	pty.SendLine("integrations add github --name=ghtest --repository=testghuser/empty")
	pty.Want("no GitHub account connected")
	pty.WantPrompt()

	// Connect a GitHub account first (authorize flow).
	pty.SendLine("integrations setup github")
	out := pty.WantREMatch(`/r/[0-9a-f]{32}`)
	state := extractRedirectKey(t, out)
	simulateGitHubCallback(t, state, 0)
	pty.Want("Connected:")
	pty.Want("testghuser")
	pty.WantPrompt()

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
