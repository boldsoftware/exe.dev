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
	re := regexp.MustCompile(`state=([0-9a-f]+)`)
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
// setup → verify duplicate error → delete → verify delete-again error → re-setup → delete.
func TestIntegrationsSetupGitHub(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	// First setup: should print install URL and wait.
	pty.SendLine("integrations setup github")
	out := pty.WantREMatch(`state=[0-9a-f]+`)
	state := extractState(t, out)

	// Simulate the GitHub callback.
	simulateGitHubCallback(t, state, 12345)

	// SSH session should unblock and print success.
	pty.Want("Connected GitHub account: testghuser")
	pty.WantPrompt()

	// Setup again: should error because already connected.
	pty.SendLine("integrations setup github")
	pty.Want("already connected as testghuser")
	pty.WantPrompt()

	// Delete the connection.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected GitHub account: testghuser")
	pty.WantPrompt()

	// Delete again: should error because no account connected.
	pty.SendLine("integrations setup github -d")
	pty.Want("no GitHub account connected")
	pty.WantPrompt()

	// Re-setup: should succeed again.
	pty.SendLine("integrations setup github")
	out = pty.WantREMatch(`state=[0-9a-f]+`)
	state = extractState(t, out)

	simulateGitHubCallback(t, state, 67890)

	pty.Want("Connected GitHub account: testghuser")
	pty.WantPrompt()

	// Final cleanup: delete.
	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected GitHub account: testghuser")
	pty.WantPrompt()
}
