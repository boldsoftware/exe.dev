package e1e

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"testing"
)

// extractState extracts the state parameter from a URL string in PTY output.
func extractState(t *testing.T, output string) string {
	t.Helper()
	re := regexp.MustCompile(`state=([0-9a-f]{32})`)
	matches := re.FindStringSubmatch(output)
	if len(matches) < 2 {
		t.Fatalf("could not extract state from output: %s", output)
	}
	return matches[1]
}

// simulateGitHubCallback sends an HTTP GET to exed's /github/callback endpoint,
// simulating the browser redirect after GitHub App installation.
func simulateGitHubCallback(t *testing.T, state string, installationID int) {
	t.Helper()
	callbackURL := fmt.Sprintf("http://localhost:%d/github/callback?code=mock-code&installation_id=%d&state=%s",
		Env.servers.Exed.HTTPPort, installationID, url.QueryEscape(state))
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("callback request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback returned %d", resp.StatusCode)
	}
}

// TestIntegrationsSetupGitHub tests the GitHub App installation flow:
// setup → duplicate install detected → delete → verify delete-again error → re-setup → delete.
func TestIntegrationsSetupGitHub(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	// First setup: should print install URL and wait.
	pty.SendLine("integrations setup github")
	out := pty.WantREMatch(`state=[0-9a-f]{32}`)
	state := extractState(t, out)

	// Simulate the GitHub callback.
	simulateGitHubCallback(t, state, 12345)

	// SSH session should unblock and print success with target account.
	pty.Want("Connected GitHub account: testghuser")
	pty.WantPrompt()

	// Setup again with same installation: should detect duplicate.
	pty.SendLine("integrations setup github")
	pty.Want("Already connected")
	out = pty.WantREMatch(`state=[0-9a-f]{32}`)
	state = extractState(t, out)
	simulateGitHubCallback(t, state, 12345)
	pty.Want("Already connected: testghuser")
	pty.WantPrompt()

	// Delete all connections.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected GitHub")
	pty.WantPrompt()

	// Delete again: should error because no account connected.
	pty.SendLine("integrations setup github -d")
	pty.Want("no GitHub account connected")
	pty.WantPrompt()

	// Re-setup: should succeed again.
	pty.SendLine("integrations setup github")
	out = pty.WantREMatch(`state=[0-9a-f]{32}`)
	state = extractState(t, out)

	simulateGitHubCallback(t, state, 67890)

	pty.Want("Connected GitHub account: testghuser")
	pty.WantPrompt()

	// Final cleanup: delete.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected GitHub")
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

	// Connect a GitHub account first.
	pty.SendLine("integrations setup github")
	out := pty.WantREMatch(`state=[0-9a-f]{32}`)
	state := extractState(t, out)
	simulateGitHubCallback(t, state, 12345)
	pty.Want("Connected GitHub account: testghuser")
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
