package e1e

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"testing"
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

// simulateGitHubCallback sends an HTTP GET to exed's /github/callback endpoint,
// simulating the browser redirect after GitHub App authorization or installation.
// If installationID is 0, no installation_id parameter is sent (authorize-only flow).
// Returns the redirect Location if the callback responded with a redirect, or empty string.
func simulateGitHubCallback(t *testing.T, state string, installationID int) string {
	t.Helper()
	callbackURL := fmt.Sprintf("http://localhost:%d/github/callback?code=mock-code&state=%s",
		Env.servers.Exed.HTTPPort, url.QueryEscape(state))
	if installationID != 0 {
		callbackURL += fmt.Sprintf("&installation_id=%d", installationID)
	}
	resp, err := noRedirectClient(nil).Get(callbackURL)
	if err != nil {
		t.Fatalf("callback request failed: %v", err)
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return ""
	case http.StatusFound:
		loc := resp.Header.Get("Location")
		if loc == "" {
			t.Fatal("redirect with no Location header")
		}
		return loc
	default:
		t.Fatalf("callback returned %d", resp.StatusCode)
		return ""
	}
}

// TestIntegrationsSetupGitHub tests the GitHub App setup flow:
// authorize → discover installations → connect.
// Then: duplicate detected → all connected → browser auto-redirected to install.
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
	// Callback blocks until SSH session processes and responds.
	loc := simulateGitHubCallback(t, state, 0)
	if loc != "" {
		t.Fatalf("expected no redirect on first setup, got %s", loc)
	}

	// Server discovers installation 12345 via API and connects it.
	pty.Want("Connected: testghuser")
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
	if loc != "" {
		t.Fatalf("expected no redirect on idempotent setup, got %s", loc)
	}
	pty.Want("Connected: testghuser")
	pty.WantPrompt()

	// Delete all connections.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected GitHub")
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

	pty.Want("Connected: testghuser")
	pty.WantPrompt()

	// List shows empty after deletion.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected GitHub")
	pty.WantPrompt()

	pty.SendLine("integrations setup github --list")
	pty.Want("No GitHub accounts connected")
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
	pty.Want("Connected: testghuser")
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
	pty.Want("Disconnected GitHub")
	pty.WantPrompt()
}
