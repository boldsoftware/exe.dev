package e1e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestIntegrationsGitHubProxy tests the GitHub integration proxy with API paths.
// This verifies that /api/v3/* and /api/graphql requests are properly routed
// to api.github.com, enabling the gh CLI to work via GH_HOST.
func TestIntegrationsGitHubProxy(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, cookies, keyFile, _ := registerForExeDev(t)
	bn := boxName(t)

	// Connect GitHub and create a GitHub integration.
	pty.SendLine("integrations setup github")
	pty.Want("Continue setup in your browser")
	pty.WantPrompt()

	connectGitHubViaWebDefault(t, cookies)

	pty.SendLine("integrations add github --name=ghproxy --repository=testghuser/empty")
	pty.Want("Added integration ghproxy")
	pty.WantPrompt()

	// Create a VM and attach the integration.
	pty.SendLine(fmt.Sprintf("new --name=%s", bn))
	pty.WantRE("Creating .*" + bn)
	pty.Want("Ready")
	pty.WantPrompt()

	waitForSSH(t, bn, keyFile)

	pty.SendLine(fmt.Sprintf("integrations attach ghproxy vm:%s", bn))
	pty.Want("Attached ghproxy to vm:" + bn)
	pty.WantPrompt()

	// Helper to run a command inside the VM and get output.
	curlRetry := func(t *testing.T, curlArgs, want string) string {
		t.Helper()
		cmd := fmt.Sprintf(`curl --max-time 10 -s %s`, curlArgs)
		var response string
		deadline := time.Now().Add(30 * time.Second)
		for {
			out, _ := boxSSHShell(t, bn, keyFile, cmd).CombinedOutput()
			response = string(out)
			if strings.Contains(response, want) {
				return response
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %q in response:\n%s", want, response)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	const pathFilterMsg = "path does not match any configured repository"

	t.Run("api_v3_passes_filter", func(t *testing.T) {
		// The /api/v3/ path should be routed to api.github.com.
		// With the mock installation token, GitHub will return some response
		// (likely 401 since the mock token isn't real), but NOT our 403 path filter error.
		response := curlRetry(t, "-w '\n%{http_code}' http://ghproxy.int.exe.cloud/api/v3/repos/testghuser/empty", "\n")
		if strings.Contains(response, pathFilterMsg) {
			t.Errorf("API path was blocked by path filter: %s", response)
		}
	})

	t.Run("graphql_passes_filter", func(t *testing.T) {
		// The /api/graphql path should be routed to api.github.com.
		response := curlRetry(t, "-X POST -d '{}' -w '\n%{http_code}' http://ghproxy.int.exe.cloud/api/graphql", "\n")
		if strings.Contains(response, pathFilterMsg) {
			t.Errorf("GraphQL path was blocked by path filter: %s", response)
		}
	})

	t.Run("git_path_still_works", func(t *testing.T) {
		// Git paths should still pass through.
		response := curlRetry(t, "-w '\n%{http_code}' http://ghproxy.int.exe.cloud/testghuser/empty.git/info/refs?service=git-upload-pack", "\n")
		if strings.Contains(response, pathFilterMsg) {
			t.Errorf("git path was blocked by path filter: %s", response)
		}
	})

	t.Run("non_api_non_git_blocked", func(t *testing.T) {
		// Non-API, non-git paths should still be blocked.
		curlRetry(t, "-w '\n%{http_code}' http://ghproxy.int.exe.cloud/random/path", pathFilterMsg)
	})

	// Clean up.
	pty.SendLine("integrations remove ghproxy")
	pty.Want("Removed integration ghproxy")
	pty.WantPrompt()

	pty.SendLine("integrations setup github -d")
	pty.Want("Disconnected:")
	pty.WantPrompt()

	cleanupBox(t, keyFile, bn)
}
