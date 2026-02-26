package exelets

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
)

// TestMetadata tests that metadata operations work with exeprox.
func TestMetadata(t *testing.T) {
	t.Parallel()

	pty, _, keyFile, email := register(t)
	boxName := makeBox(t, pty, keyFile, email)
	pty.Disconnect()
	defer deleteBox(t, boxName, keyFile)

	t.Run("shelley_install", func(t *testing.T) {
		pty, _ := testinfra.MakeTestPTY(t, "", "ssh localhost", true)
		cmd, err := serverEnv.SSHToExeDev(t.Context(), pty.PTY(), keyFile)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Wait() })

		out, err := serverEnv.BoxSSHCommand(t.Context(), boxName, keyFile, "/usr/local/bin/shelley", "--version").CombinedOutput()
		if err == nil {
			t.Logf("shelley initial version %s", strings.TrimSpace(string(out)))
		}

		pty.SendLine("shelley install " + boxName)
		pty.Want("Installed shelley")
		pty.WantPrompt()

		for {
			out, err := serverEnv.BoxSSHCommand(t.Context(), boxName, keyFile, "sudo", "systemctl", "is-active", "shelley.service").CombinedOutput()
			if err == nil && bytes.Contains(out, []byte("active")) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	})

	t.Run("metadata_service", func(t *testing.T) {
		out, err := serverEnv.BoxSSHCommand(t.Context(), boxName, keyFile, "curl --max-time 10 -s http://169.254.169.254/ | jq -r .source_ip").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get IP: %v\n%s", err, out)
		}
		vmIP := strings.TrimSpace(string(out))
		if vmIP == "" || vmIP == "null" {
			t.Fatalf("metadata service not responding: got %q for source_ip", vmIP)
		}

		pty, _ := testinfra.MakeTestPTY(t, "", "ssh localhost", true)
		sshCmd, err := serverEnv.SSHWithUserName(t.Context(), pty.PTY(), boxName, keyFile)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sshCmd.Wait() })
		defer pty.Disconnect()

		pty.SetPromptRE(regexp.QuoteMeta(boxName) + ".*" + regexp.QuoteMeta("$"))

		pty.WantPrompt()
		pty.DisableBracketedPaste()
		pty.WantPrompt()

		// Test metadata service returns source_ip.
		pty.SendLine("curl --max-time 10 -s http://169.254.169.254/ | jq -r .source_ip")
		pty.WantPrompt()

		// Test metadata service returns JSON with instance information.
		pty.SendLine("curl --max-time 10 -s http://169.254.169.254/ | jq -M .")
		pty.Want(`"name":`)
		pty.Want(`"source_ip":`)
		pty.WantPrompt()

		// Verify the name matches our box.
		pty.SendLine("curl --max-time 10 -s http://169.254.169.254/ | jq -r .name")
		pty.Want(boxName)
		pty.WantPrompt()

		// Test LLM gateway ready endpoint through metadata service.
		pty.SendLine("curl --max-time 10 -s -o /dev/null -w '%{http_code}\\n' http://169.254.169.254/gateway/llm/ready")
		pty.WantRE("200")
		pty.WantPrompt()

		// Test that unknown paths return 404.
		pty.SendLine("curl --max-time 10 -s -o /dev/null -w '%{http_code}\\n' http://169.254.169.254/does-not-exist")
		pty.Want("404")
		pty.WantPrompt()
	})
}
