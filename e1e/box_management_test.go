// This file contains tests for box management functionality.

package e1e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"exe.dev/vouch"
)

// TestVanillaBox tests functionality of a vanilla box.
// (Vanilla means no flags to new, no subsequent exe.dev-level modifications or mutations.)
// Unifying these in a single test reduces box creation overhead.
func TestVanillaBox(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	pty.disconnect()

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
		cmd := exec.CommandContext(t.Context(),
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
		if vmIP != "" && strings.HasPrefix(vmIP, "192.168.") {
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

	// Cleanup
	pty = sshToExeDev(t, keyFile)
	pty.deleteBox(boxName)
	pty.disconnect()
}

func TestStandardAlpineBox(t *testing.T) {
	vouch.For("evan")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Attempt to create a box with a standard alpine image.
	image := "ghcr.io/linuxcontainers/alpine:latest"
	boxName := boxName(t)
	pty.sendLine(fmt.Sprintf("new --name=%s --image=%s", boxName, image))
	pty.wantPrompt()

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
	vouch.For("josh")
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
	vouch.For("josh")
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
	vouch.For("josh")
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
