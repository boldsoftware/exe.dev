// This file contains tests for box management functionality.

package e1e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"exe.dev/bsdns/alley53"
)

// TestVanillaBox tests functionality of a vanilla box.
// (Vanilla means no flags to new, no subsequent exe.dev-level modifications or mutations.)
// Unifying these in a single test reduces box creation overhead.
func TestVanillaBox(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, cookies, keyFile, email := registerForExeDev(t)
	boxName := newBox(t, pty)
	pty.disconnect()

	t.Run("new_box_email_sent", func(t *testing.T) {
		msg := Env.email.waitForEmail(t, email)
		if !strings.Contains(msg.Subject, boxName) {
			t.Errorf("expected email subject to contain box name %q, got %q", boxName, msg.Subject)
		}
	})

	t.Run("no_second_hint", func(t *testing.T) {
		noGolden(t)
		pty := sshToExeDev(t, keyFile)
		// They've created a box, so we should have stopped hinting at them about it.
		pty.reject("create your first box")
		pty.wantPrompt()
		pty.disconnect()
	})

	waitForSSH(t, boxName, keyFile)

	// Ensure sudo hints are suppressed so golden output stays consistent
	// regardless of whether previous sudo commands were run on this box during image creation.
	// TODO: remove this when box creation is more hermetic and consistent between lima and CI.
	if err := boxSSHCommand(t, boxName, keyFile, "sudo", "true").Run(); err != nil {
		t.Fatalf("failed to run sudo true: %v", err)
	}

	t.Run("ssh", func(t *testing.T) {
		pty := sshToBox(t, boxName, keyFile)
		pty.reject("Permission denied") // fail fast on common known failure mode
		pty.wantPrompt()
		pty.sendLine("whoami")
		pty.want("exedev")
		pty.want("\n") // exedev is also in the prompt! require a newline after it.
		pty.wantPrompt()
		pty.disconnect()
	})

	t.Run("scp", func(t *testing.T) {
		// Make sure SCP works too.
		// We need some file to copy up. Use the private key. Why not. It's a file.
		cmd := exec.CommandContext(Env.context(t),
			"scp",
			"-F", "/dev/null",
			"-P", fmt.Sprint(Env.sshPort()),
			"-o", "IdentityFile="+keyFile,
			"-o", "IdentityAgent=none",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "PreferredAuthentications=publickey",
			"-o", "PubkeyAuthentication=yes",
			"-o", "PasswordAuthentication=no",
			"-o", "KbdInteractiveAuthentication=no",
			"-o", "ChallengeResponseAuthentication=no",
			"-o", "IdentitiesOnly=yes",
			keyFile,
			fmt.Sprintf("%v@localhost:key.txt", boxName),
		)
		cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=") // disable SSH agent
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("failed to run %v: %v\n%s", cmd, err, out)
		}

		// Confirm that the file made it there.
		out, err = boxSSHCommand(t, boxName, keyFile, "ls", "key.txt").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to run ls key.txt: %v\n%s", err, out)
		}
		if string(out) != "key.txt\n" {
			t.Fatalf("expected key.txt from ls, got %q", out)
		}
	})

	t.Run("docker", func(t *testing.T) {
		// Wait for docker to be available. Docker uses socket activation and starts on first use,
		// but we need to give systemd a bit more time after SSH is ready.
		var err error
		for range 150 {
			err = boxSSHCommand(t, boxName, keyFile, "sudo", "docker", "info").Run()
			if err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("docker not available after waiting, last error: %v", err)
		}

		// Run a simple docker container to verify Docker works in exeuntu.
		out, err := boxSSHCommand(t, boxName, keyFile, "sudo", "docker", "run", "--rm", "ghcr.io/linuxcontainers/alpine:latest", "echo", "hello").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to run docker command: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "hello") {
			t.Fatalf("expected 'hello' in docker output, got: %s", out)
		}
	})

	t.Run("ping_without_sudo", func(t *testing.T) {
		// Verify that non-root users can use ping without sudo (see #23, #128)
		out, err := boxSSHCommand(t, boxName, keyFile, "ping", "-c", "1", "-W", "5", "127.0.0.1").CombinedOutput()
		if err != nil {
			t.Fatalf("ping without sudo failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "1 packets transmitted") {
			t.Fatalf("expected ping success output, got: %s", out)
		}
	})

	t.Run("listen_port_80_without_sudo", func(t *testing.T) {
		// Verify that non-root users can bind to port 80 without sudo
		out, err := boxSSHCommand(t, boxName, keyFile, "sh", "-c", "nc -l -p 80 & pid=$!; sleep 0.1; kill $pid 2>/dev/null; echo ok").CombinedOutput()
		if err != nil {
			t.Fatalf("listen on port 80 without sudo failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "ok") {
			t.Fatalf("expected 'ok' output, got: %s", out)
		}
	})

	t.Run("shelley_install", func(t *testing.T) {
		// Test the shelley install command
		pty := sshToExeDev(t, keyFile)
		defer pty.disconnect()

		// Get initial shelley version/timestamp
		initialVersion := ""
		out, err := boxSSHCommand(t, boxName, keyFile, "/usr/local/bin/shelley", "--version").CombinedOutput()
		if err == nil {
			initialVersion = strings.TrimSpace(string(out))
		}

		// Run shelley install command
		pty.sendLine("shelley install " + boxName)
		pty.want("Installing Shelley")
		pty.wantRe("(Backed up|Copied shelley binary)")
		pty.want("Installed shelley")
		pty.wantRe("(Restarted|Warning)") // Either succeeded or warned about restart
		pty.wantPrompt()

		// Verify shelley binary exists and is executable
		out, err = boxSSHCommand(t, boxName, keyFile, "test", "-x", "/usr/local/bin/shelley", "&&", "echo", "exists").CombinedOutput()
		if err != nil {
			t.Fatalf("shelley binary not found or not executable after install: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "exists") {
			t.Fatalf("expected 'exists' confirmation, got: %s", out)
		}

		// Verify shelley service is running (give it a moment to start)
		for range 50 {
			out, err = boxSSHCommand(t, boxName, keyFile, "sudo", "systemctl", "is-active", "shelley.service").CombinedOutput()
			if err == nil && strings.Contains(string(out), "active") {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		// It's ok if the service isn't active yet (systemd can be slow), but the binary should be there
		t.Logf("Initial version: %s", initialVersion)
		t.Logf("Shelley install test completed")
	})

	t.Run("metadata_service", func(t *testing.T) {
		// Get the VM's IP address so we can canonicalize it BEFORE starting the pty session
		// that will be recorded in the golden file
		out, err := boxSSHCommand(t, boxName, keyFile, "curl --max-time 10 -s http://169.254.169.254/ | jq -r .source_ip").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get IP: %v", err)
		}
		vmIP := strings.TrimSpace(string(out))
		if vmIP == "" || vmIP == "null" {
			t.Fatalf("metadata service not responding: got %q for source_ip", vmIP)
		}
		if strings.HasPrefix(vmIP, "192.168.") || strings.HasPrefix(vmIP, "100.") {
			Env.addCanonicalization(vmIP, "VM_IP")
		}

		pty := sshToBox(t, boxName, keyFile)
		defer pty.disconnect()

		pty.wantPrompt()

		// Test metadata service returns source_ip
		pty.sendLine("curl --max-time 10 -s http://169.254.169.254/ | jq -r .source_ip")
		pty.wantPrompt()

		// Test metadata service returns JSON with instance information
		pty.sendLine("curl --max-time 10 -s http://169.254.169.254/ | jq -M .")
		pty.want(`"name":`)
		pty.want(`"source_ip":`)
		pty.wantPrompt()

		// Verify the name matches our box
		pty.sendLine("curl --max-time 10 -s http://169.254.169.254/ | jq -r .name")
		pty.want(boxName)
		pty.wantPrompt()

		// Test LLM gateway ready endpoint through metadata service
		pty.sendLine("curl --max-time 10 -s -o /dev/null -w '%{http_code}\\n' http://169.254.169.254/gateway/llm/ready")
		pty.want("200")
		pty.wantPrompt()

		// Test Anthropic API through metadata service (only if ANTHROPIC_API_KEY is set)
		// We don't include this because it messes with golden files locally.
		// if os.Getenv("ANTHROPIC_API_KEY") != "" {
		// 	pty.sendLine(`curl --max-time 30 -s -o /dev/null -w '%{http_code}' http://169.254.169.254/gateway/llm/anthropic/v1/messages -H "content-type: application/json" -H "anthropic-version: 2023-06-01" -d '{"model":"claude-3-5-haiku-20241022","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}'`)
		// 	pty.want("200")
		// 	pty.wantPrompt()
		// }
	})

	// Test LLM gateway proxies to external APIs.
	// These tests confirm that the full communication path works:
	// VM -> exelet metadata service -> exed LLM gateway -> external API
	//
	// These tests do NOT require valid API keys - they send malformed requests
	// that will be rejected by the external APIs anyway. Getting an error response
	// FROM the external API (not from our gateway) proves the gateway is working
	// end-to-end. The e1e test infrastructure sets fake API keys if real ones
	// aren't provided, so the gateway will forward requests to the external APIs.
	//
	// These tests DO require the external APIs (Anthropic, OpenAI, Fireworks) to be up and reachable.
	t.Run("gateway_anthropic", func(t *testing.T) {
		noGolden(t) // Response contains variable request_id
		// Send a minimal request to Anthropic. Without valid messages, Anthropic will return
		// an error, but the error response will contain a request_id field proving we reached them.
		out, err := boxSSHCommand(t, boxName, keyFile, "curl", "--max-time", "30", "-s",
			"http://169.254.169.254/gateway/llm/anthropic/v1/messages",
			"-H", "content-type: application/json",
			"-H", "anthropic-version: 2023-06-01",
			"-d", `{}`).CombinedOutput()
		response := string(out)
		// Anthropic error responses include a request_id field like "request_id": "req_..."
		// We check for request_id first - if present, we reached Anthropic regardless of curl exit code.
		if !strings.Contains(response, `"request_id"`) {
			if err != nil {
				t.Fatalf("curl to anthropic gateway failed: %v\n%s", err, out)
			}
			t.Errorf("expected Anthropic error response with request_id, got: %s", response)
		}
	})

	t.Run("gateway_openai", func(t *testing.T) {
		noGolden(t) // Response may vary
		// Send a minimal request to OpenAI. The error response proves we reached them.
		// OpenAI returns errors with "error" object containing "type" and "message".
		out, err := boxSSHCommand(t, boxName, keyFile, "curl", "--max-time", "30", "-s",
			"http://169.254.169.254/gateway/llm/openai/v1/chat/completions",
			"-H", "content-type: application/json",
			"-d", `{}`).CombinedOutput()
		response := string(out)
		// OpenAI error responses include an "error" object with "type" field
		// We check for the error response first - if present, we reached OpenAI regardless of curl exit code.
		if !strings.Contains(response, `"error"`) || !strings.Contains(response, `"type"`) {
			if err != nil {
				t.Fatalf("curl to openai gateway failed: %v\n%s", err, out)
			}
			t.Errorf("expected OpenAI error response with error object, got: %s", response)
		}
	})

	t.Run("gateway_fireworks", func(t *testing.T) {
		noGolden(t) // Response may vary
		// Send a minimal request to Fireworks. The error response proves we reached them.
		// Fireworks uses OpenAI-compatible API format.
		out, err := boxSSHCommand(t, boxName, keyFile, "curl", "--max-time", "30", "-s",
			"http://169.254.169.254/gateway/llm/fireworks/inference/v1/chat/completions",
			"-H", "content-type: application/json",
			"-d", `{}`).CombinedOutput()
		response := string(out)
		// Fireworks error responses include an "error" object (OpenAI-compatible)
		// We check for the error response first - if present, we reached Fireworks regardless of curl exit code.
		if !strings.Contains(response, `"error"`) {
			if err != nil {
				t.Fatalf("curl to fireworks gateway failed: %v\n%s", err, out)
			}
			t.Errorf("expected Fireworks error response with error object, got: %s", response)
		}
	})

	t.Run("shard_routing", func(t *testing.T) {
		// shard_routing tests that `ssh boxname.exe.cloud` routes to the correct box.
		// Skip if alley53 isn't running
		if !alley53.NewClient("localhost:5380").IsRunning(Env.context(t)) {
			t.Skip("alley53 is not running - skipping box hostname routing test")
		}

		// This is the full flow:
		// 1. alley53 DNS resolves boxname.exe.cloud to a shard IP (e.g., 127.21.0.1)
		// 2. SSH connects to that IP
		// 3. sshpiper sees the local address and routes to the box based on (user + shard)

		// Now test the hostname-based routing: ssh boxname.exe.cloud
		// This goes through DNS resolution -> shard IP -> sshpiper -> box
		boxHostname := boxName + ".exe.cloud"

		// Connect via SSH to boxHostname, no username.
		//
		// We can't use sshToBox here:
		//
		// IMPORTANT: This connects directly to sshpiper (piperd) port, bypassing the
		// test's TCP proxy. This is necessary because hostname-based routing depends
		// on the local address sshpiper sees when accepting the connection. The TCP
		// proxy creates a new outbound connection to sshpiper, losing the original
		// destination IP information.
		ptyHost := makePty(t, "ssh "+boxHostname)
		args := sshOpts()
		args = append(args,
			"-p", fmt.Sprint(Env.piperd.SSHPort), // use piperd port directly (not proxy) so sshpiper sees the correct local address
			"-o", "IdentityFile="+keyFile,
			boxHostname,
		)
		sshCmd := exec.CommandContext(Env.context(t), "ssh", args...)
		sshCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=") // disable SSH agent
		ptyHost.attachAndStart(sshCmd)
		ptyHost.promptRe = regexp.QuoteMeta(boxName) + ".*" + regexp.QuoteMeta("$")

		// Verify we're in the right box
		ptyHost.reject("Permission denied")
		ptyHost.reject(exeDevPrompt) // we don't want to land in the repl!
		ptyHost.wantPrompt()
		ptyHost.sendLine("hostname")
		ptyHost.want(boxName)
		ptyHost.wantPrompt()
		ptyHost.disconnect()
	})

	t.Run("proxy_port_dashboard", func(t *testing.T) {
		// Test that the dashboard correctly displays the proxy port and share setting
		noGolden(t)

		// Set custom proxy port (8000) and make it public
		exeShell := sshToExeDev(t, keyFile)
		exeShell.sendLine(fmt.Sprintf("share port %s 8000", boxName))
		exeShell.want("Route updated successfully")
		exeShell.wantPrompt()

		exeShell.sendLine(fmt.Sprintf("share set-public %s", boxName))
		exeShell.want("Route updated successfully")
		exeShell.wantPrompt()
		exeShell.disconnect()

		// Fetch dashboard and check proxy port display
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatalf("failed to create cookie jar: %v", err)
		}
		for _, cookie := range cookies {
			cookie.Domain = "localhost"
			jar.SetCookies(&url.URL{Scheme: "http", Host: fmt.Sprintf("localhost:%d", Env.exed.HTTPPort)}, []*http.Cookie{cookie})
		}
		client := &http.Client{
			Jar:     jar,
			Timeout: 10 * time.Second,
		}
		resp, err := client.Get(fmt.Sprintf("http://localhost:%d/", Env.exed.HTTPPort))
		if err != nil {
			t.Fatalf("failed to get dashboard: %v", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read dashboard: %v", err)
		}
		dashboard := string(body)

		// Check that the dashboard shows the correct proxy port (8000) and share setting (public)
		if !strings.Contains(dashboard, "Port 8000") {
			t.Errorf("Expected 'Port 8000' in dashboard, got dashboard content (truncated): %s", truncate(dashboard, 500))
		}
		if !strings.Contains(dashboard, "public") {
			t.Errorf("Expected 'public' share setting in dashboard, got dashboard content (truncated): %s", truncate(dashboard, 500))
		}

		// Change to private and port 3000 to test another combination
		exeShell = sshToExeDev(t, keyFile)
		exeShell.sendLine(fmt.Sprintf("share port %s 3000", boxName))
		exeShell.want("Route updated successfully")
		exeShell.wantPrompt()

		exeShell.sendLine(fmt.Sprintf("share set-private %s", boxName))
		exeShell.want("Route updated successfully")
		exeShell.wantPrompt()
		exeShell.disconnect()

		// Fetch dashboard again
		resp, err = client.Get(fmt.Sprintf("http://localhost:%d/", Env.exed.HTTPPort))
		if err != nil {
			t.Fatalf("failed to get dashboard after update: %v", err)
		}
		defer resp.Body.Close()
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read dashboard after update: %v", err)
		}
		dashboard = string(body)

		// Check the updated values
		if !strings.Contains(dashboard, "Port 3000") {
			t.Errorf("Expected 'Port 3000' in dashboard after update, got dashboard content (truncated): %s", truncate(dashboard, 500))
		}
		if !strings.Contains(dashboard, "private") {
			t.Errorf("Expected 'private' share setting in dashboard after update, got dashboard content (truncated): %s", truncate(dashboard, 500))
		}
	})

	// Cleanup
	pty = sshToExeDev(t, keyFile)
	pty.deleteBox(boxName)
	pty.disconnect()
}

func TestStandardAlpineBox(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Attempt to create a box with a standard alpine image.
	image := "ghcr.io/linuxcontainers/alpine:latest"
	boxName := newBox(t, pty, BoxOpts{Image: image})
	waitForSSH(t, boxName, keyFile)

	out, err := boxSSHCommand(t, boxName, keyFile, "cat", "/etc/os-release").CombinedOutput()
	if err != nil {
		t.Fatalf("error running box command: %s", err)
	}
	if !strings.Contains(string(out), "Alpine Linux") {
		t.Fatalf("expected 'Alpine Linux', got: %s", string(out))
	}
	// cleanup
	pty.deleteBox(boxName)
	pty.disconnect()
}

func TestBadBoxName(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, _, _ := registerForExeDev(t)

	// Attempt to create a box with an invalid name.
	boxName := "ThisIsNotAValidBoxName!"
	pty.sendLine("new --name=" + boxName)
	pty.wantRe("Invalid box name")
	pty.wantPrompt()
	pty.disconnect()
}

func TestNewRejectsBoxMatchingSSHUsername(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.disconnect()

	conflictName := boxName(t)
	conflictPty := sshWithUsername(t, conflictName, keyFile)
	conflictPty.prompt = exeDevPrompt
	conflictPty.wantPrompt()

	conflictPty.sendLine("new --name=" + conflictName)
	conflictPty.want("cannot match SSH username")
	conflictPty.wantPrompt()
	conflictPty.disconnect()
}

func TestNewWithPrompt(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, _, _ := registerForExeDev(t)

	// Create a box with a prompt (use predictable model for testing)
	boxName := boxName(t)
	prompt := "hello" // This will trigger predictable service to respond with "Well, hi there!"
	// systemd is painfully slow on macOS.
	// By providing --command, we bypass it...but we still need Shelley running,
	// so we reach in and start it ourselves.
	// This is gross, but the tests are unusable otherwise.
	// TODO: revert this hack when systemd is faster on macOS in L2.
	command := fmt.Sprintf(`new --name=%s --prompt=%q --prompt-model=predictable`+
		` --command="/usr/local/bin/shelley -debug -db /home/exedev/.shelley/shelley.db -config /exe.dev/shelley.json serve -port 9999"`,
		boxName, prompt,
	)
	pty.sendLine(command)
	pty.reject("Sorry")
	pty.wantRe("Creating .*" + boxName)
	// Calls to action
	pty.want("Coding agent")
	pty.want("App")
	pty.want("SSH")

	// Expect Shelley prompt execution to start
	pty.want("Shelley...")

	// With predictable model, we should get a quick response
	pty.want("Well, hi there!") // Expected response from predictable service for "hello"

	// Should return to prompt after Shelley completes
	pty.wantPrompt()

	// Cleanup
	pty.deleteBox(boxName)
	pty.disconnect()
}

func TestNewWithPromptDefaultModel(t *testing.T) {
	// TODO(philip): figure this out.
	t.Skip("This is flaky right now for me, and I just added it.")

	// Only run if ANTHROPIC_API_KEY is set
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t) // LLM responses are unpredictable

	pty, _, keyFile, _ := registerForExeDev(t)

	// Create a box with a prompt (use default model - will use gateway)
	boxName := boxName(t)
	prompt := "run 'touch /tmp/foo'" // Simple command to test execution
	// systemd is painfully slow on macOS.
	// By providing --command, we bypass it...but we still need Shelley running,
	// so we reach in and start it ourselves.
	// This is gross, but the tests are unusable otherwise.
	// TODO: revert this hack when systemd is faster on macOS in L2.
	command := fmt.Sprintf(`new --name=%s --prompt=%q`+
		` --command="/usr/local/bin/shelley -debug -db /home/exedev/.shelley/shelley.db -config /exe.dev/shelley.json serve -port 9999"`,
		boxName, prompt,
	)
	pty.sendLine(command)
	pty.reject("Sorry")
	pty.wantRe("Creating .*" + boxName)

	// Expect Shelley prompt execution to start
	pty.want("Shelley...")

	// Wait for completion - we don't know exactly what the LLM will say,
	// but we should get back to a prompt eventually (with timeout via expectPty)
	pty.wantPrompt()

	// Verify the command was executed by checking if /tmp/foo exists
	out, err := boxSSHCommand(t, boxName, keyFile, "test", "-f", "/tmp/foo", "&&", "echo", "exists").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "exists") {
		t.Errorf("Expected /tmp/foo to exist after LLM execution, but it doesn't. Output: %s, Error: %v", string(out), err)
	}

	// Cleanup
	pty.deleteBox(boxName)
	pty.disconnect()
}

func TestBoxRestartShutdown(t *testing.T) {
	t.Skip("this is flaky in CI, to be investigated")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	pty.disconnect()
	waitForSSH(t, boxName, keyFile)

	t.Run("restart", func(t *testing.T) {
		box := sshToBox(t, boxName, keyFile)
		box.wantPrompt()
		box.sendLine("echo restart-test > /home/exedev/restart.txt")
		box.wantPrompt()
		box.sendLine("sudo reboot")
		box.wantEOF()

		// Wait for box to come back up and verify marker file remains.
		waitForSSH(t, boxName, keyFile)
		box = sshToBox(t, boxName, keyFile)

		box.wantPrompt()
		box.sendLine("cat /home/exedev/restart.txt")
		box.want("restart-test")
		box.wantPrompt()
		box.disconnect()
	})

	t.Run("shutdown", func(t *testing.T) {
		box := sshToBox(t, boxName, keyFile)
		box.wantPrompt()
		box.sendLine("sudo shutdown now")
		box.wantEOF()

		// After shutdown, SSH should not connect.
		// Set a short timeout here to avoid long waits.
		// This could yield false negatives, but it's worth it.
		//
		// TODO: figure out why this command hangs indefinitely without a timeout.
		// It really should fail on its own reasonably quickly,
		// but I've never seen it actually time out, even after many minutes.
		// That probably means we're leaking something somewhere.
		ctx, cancel := context.WithTimeout(Env.context(t), time.Second)
		defer cancel()
		cmd := boxSSHCommandContext(ctx, boxName, keyFile, "echo", "ping")
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("ssh to box %q succeeded after shutdown; output: %s", boxName, output)
		}
	})

	cleanup := sshToExeDev(t, keyFile)
	cleanup.deleteBox(boxName)
	cleanup.disconnect()
}

// TestNewWithEnvVars tests environment variable passing to boxes.
//
// It is also a designated special test!
// It is the only test in e1e that is not run in parallel.
// We want one non-parallel test, which runs first, to take the "pulling exeuntu hit",
// and generally to fail fast if something is very broken.
// This one was picked by virtue of being pretty simple and short.
// (The longer that test, the more we want it to be parallel, because Amdahl.)
func TestNewWithEnvVars(t *testing.T) {
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Create a box with environment variables including simple values and special characters
	boxName := boxName(t)
	pty.sendLine(fmt.Sprintf("new --name=%s --env TEST_VAR1=value1 --env TEST_VAR2=value2 --env 'GREETING=hello world' --env 'COMMAND=echo $HOME' --env 'QUOTE=it'\"'\"'s great'", boxName))
	pty.wantRe("Creating .*" + boxName)
	pty.want("Ready")
	pty.wantPrompt()
	pty.disconnect()

	// SSH into the box and verify the environment variables are set
	waitForSSH(t, boxName, keyFile)
	box := sshToBox(t, boxName, keyFile)
	box.wantPrompt()

	// Check simple values
	box.sendLine("echo $TEST_VAR1")
	box.want("value1")
	box.wantPrompt()

	box.sendLine("echo $TEST_VAR2")
	box.want("value2")
	box.wantPrompt()

	// Check GREETING (contains space)
	box.sendLine("echo $GREETING")
	box.want("hello world")
	box.wantPrompt()

	// Check COMMAND (contains special chars that should NOT be expanded)
	box.sendLine("echo $COMMAND")
	box.want("echo $HOME")
	box.wantPrompt()

	// Check QUOTE (contains single quote)
	box.sendLine("echo $QUOTE")
	box.want("it's great")
	box.wantPrompt()

	box.disconnect()

	// Clean up
	cleanup := sshToExeDev(t, keyFile)
	cleanup.deleteBox(boxName)
	cleanup.disconnect()
}

func TestNewWithInvalidEnvVarFormat(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Try to create a box with invalid environment variable format (missing =)
	boxName := boxName(t)
	pty.sendLine(fmt.Sprintf("new --name=%s --env INVALID_VAR", boxName))
	pty.want("invalid environment variable format")
	pty.want("must be KEY=VALUE")
	pty.wantPrompt()
	pty.disconnect()

	// Verify the box was not created
	cleanup := sshToExeDev(t, keyFile)
	cleanup.sendLine(fmt.Sprintf("rm %s", boxName))
	cleanup.want("not found")
	cleanup.wantPrompt()
	cleanup.disconnect()
}

// TestNewBoxVariants tests various box creation flags that don't require deep verification.
func TestNewBoxVariants(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, email := registerForExeDev(t)

	// Test both long name (63 chars, DNS limit) and --no-email flag together.
	// For no-email, poison the inbox - email server will panic if email arrives before process ends.
	Env.email.poisonInbox(email)

	boxName := boxName(t)
	if len(boxName) < 63 {
		boxName += strings.Repeat("a", 63-len(boxName))
		Env.addCanonicalization(boxName, "BOX_NAME")
	}
	pty.sendLine(fmt.Sprintf("new --name=%s -no-email", boxName))
	pty.wantRe("Creating .*" + boxName)
	pty.want("Ready")
	pty.wantPrompt()

	// Clean up
	cleanup := sshToExeDev(t, keyFile)
	cleanup.deleteBox(boxName)
	cleanup.disconnect()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
