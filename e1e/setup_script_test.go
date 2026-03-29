package e1e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestSetupScriptViaFlag tests the --setup-script flag on the new command.
func TestSetupScriptViaFlag(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	boxName := boxName(t)
	// Use the interactive REPL so shlex can handle the quoting.
	pty.SendLine(fmt.Sprintf(`new --name=%s --setup-script="#!/bin/sh\ntouch /tmp/setup-flag-ran"`, boxName))
	pty.WantRE("Creating .*" + boxName)
	pty.Want("Ready")
	pty.WantPrompt()

	// Verify the setup script was written to /exe.dev/setup
	waitForSSH(t, boxName, keyFile)
	box := sshToBox(t, boxName, keyFile)

	// The file should exist (or have existed and been run by exe-setup.service).
	box.SendLine("cat /exe.dev/setup || echo SETUP_GONE")
	box.WantRE("setup-flag-ran|SETUP_GONE")
	box.WantPrompt()

	// Also verify the script is executable
	box.SendLine("test -x /exe.dev/setup && echo EXECUTABLE || echo SETUP_GONE")
	box.WantRE("EXECUTABLE|SETUP_GONE")
	box.WantPrompt()

	box.Disconnect()
	cleanupBox(t, keyFile, boxName)
}

// TestSetupScriptViaDefault tests setting a default setup script and having
// it automatically applied to new VMs.
func TestSetupScriptViaDefault(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Set a default setup script via stdin (the piped approach)
	script := "#!/bin/sh\ntouch /tmp/setup-default-ran"
	out, err := Env.servers.RunExeDevSSHCommandWithStdin(
		Env.context(t), keyFile, []byte(script),
		"defaults", "write", "dev.exe", "new.setup-script",
	)
	if err != nil {
		t.Fatalf("defaults write via stdin failed: %v\n%s", err, out)
	}

	// Verify it was stored
	out, err = Env.servers.RunExeDevSSHCommand(
		Env.context(t), keyFile,
		"defaults", "read", "dev.exe", "new.setup-script",
	)
	if err != nil {
		t.Fatalf("defaults read failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "#!/bin/sh") {
		t.Fatalf("expected script in output, got: %s", out)
	}

	// Create a VM without --setup-script; it should use the default
	boxName := boxName(t)
	pty.SendLine(fmt.Sprintf("new --name=%s", boxName))
	pty.WantRE("Creating .*" + boxName)
	pty.Want("Ready")
	pty.WantPrompt()

	// Verify the setup script was written
	waitForSSH(t, boxName, keyFile)
	box := sshToBox(t, boxName, keyFile)

	box.SendLine("cat /exe.dev/setup || echo SETUP_GONE")
	box.WantRE("setup-default-ran|SETUP_GONE")
	box.WantPrompt()

	box.Disconnect()

	// Clean up the default
	pty.SendLine("defaults delete dev.exe new.setup-script")
	pty.WantPrompt()

	pty.SendLine("defaults read dev.exe new.setup-script")
	pty.Want("(not set)")
	pty.WantPrompt()

	cleanupBox(t, keyFile, boxName)
}

// TestSetupScriptDefaultViaStdin tests piping a default setup script via stdin:
// cat setup.sh | ssh exe.dev defaults write dev.exe new.setup-script
func TestSetupScriptDefaultViaStdin(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	boxName := boxName(t)
	script := "#!/bin/sh\ntouch /tmp/setup-pipe-ran"

	// Pipe the script via stdin to defaults write
	out, err := Env.servers.RunExeDevSSHCommandWithStdin(
		Env.context(t), keyFile, []byte(script),
		"defaults", "write", "dev.exe", "new.setup-script",
	)
	if err != nil {
		t.Fatalf("defaults write via stdin failed: %v\n%s", err, out)
	}

	// Create a VM to verify the default was applied
	pty.SendLine(fmt.Sprintf("new --name=%s", boxName))
	pty.WantRE("Creating .*" + boxName)
	pty.Want("Ready")
	pty.WantPrompt()

	// Verify the setup script was written
	waitForSSH(t, boxName, keyFile)
	box := sshToBox(t, boxName, keyFile)

	box.SendLine("cat /exe.dev/setup || echo SETUP_GONE")
	box.WantRE("setup-pipe-ran|SETUP_GONE")
	box.WantPrompt()

	box.Disconnect()

	// Clean up the default
	pty.SendLine("defaults delete dev.exe new.setup-script")
	pty.WantPrompt()

	cleanupBox(t, keyFile, boxName)
}

// TestSetupScriptFlagOverridesDefault tests that --setup-script on the command
// line overrides the user default.
func TestSetupScriptFlagOverridesDefault(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Set a default via stdin
	out, err := Env.servers.RunExeDevSSHCommandWithStdin(
		Env.context(t), keyFile, []byte("#!/bin/sh\necho default > /tmp/which-script"),
		"defaults", "write", "dev.exe", "new.setup-script",
	)
	if err != nil {
		t.Fatalf("defaults write failed: %v\n%s", err, out)
	}

	// Create with explicit override via the flag (overrides the default)
	boxName := boxName(t)
	pty.SendLine(fmt.Sprintf(`new --name=%s --setup-script="#!/bin/sh\necho override"`, boxName))
	pty.WantRE("Creating .*" + boxName)
	pty.Want("Ready")
	pty.WantPrompt()

	waitForSSH(t, boxName, keyFile)
	box := sshToBox(t, boxName, keyFile)

	// The file should contain "override", not "default"
	box.SendLine("cat /exe.dev/setup || echo SETUP_GONE")
	box.WantRE("override|SETUP_GONE")
	box.WantPrompt()

	box.Disconnect()

	// Clean up
	pty.SendLine("defaults delete dev.exe new.setup-script")
	pty.WantPrompt()

	cleanupBox(t, keyFile, boxName)
}

// TestSetupScriptDefaultWriteReadDelete tests the defaults write/read/delete
// cycle for new.setup-script, both via interactive REPL and via SSH exec with stdin.
func TestSetupScriptDefaultWriteReadDelete(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Write via stdin
	script := "#!/bin/sh\necho hello from setup"
	out, err := Env.servers.RunExeDevSSHCommandWithStdin(
		Env.context(t), keyFile, []byte(script),
		"defaults", "write", "dev.exe", "new.setup-script",
	)
	if err != nil {
		t.Fatalf("defaults write via stdin failed: %v\n%s", err, out)
	}

	// Read via interactive REPL
	pty.SendLine("defaults read dev.exe new.setup-script")
	pty.Want("#!/bin/sh")
	pty.WantPrompt()

	// Read all defaults to verify it shows up
	pty.SendLine("defaults read dev.exe")
	pty.Want("new.setup-script:")
	pty.WantPrompt()

	// Delete
	pty.SendLine("defaults delete dev.exe new.setup-script")
	pty.WantPrompt()

	// Verify deleted
	pty.SendLine("defaults read dev.exe new.setup-script")
	pty.Want("(not set)")
	pty.WantPrompt()

	pty.Disconnect()
}

// TestSetupScriptDefaultWriteWithArg tests writing a setup script default
// as a positional argument (inline, for short scripts).
func TestSetupScriptDefaultWriteWithArg(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	// Write a short script as a positional argument (quoted in the REPL)
	pty.SendLine(fmt.Sprintf("defaults write dev.exe new.setup-script %q", "#!/bin/sh\necho inline"))
	pty.WantPrompt()

	pty.SendLine("defaults read dev.exe new.setup-script")
	pty.Want("#!/bin/sh")
	pty.WantPrompt()

	// Clean up
	pty.SendLine("defaults delete dev.exe new.setup-script")
	pty.WantPrompt()

	pty.Disconnect()
}

// TestSetupScriptExecutesEndToEnd verifies the full flow: the setup script
// is written, executed by exe-setup.service, produces a side effect, and
// the script file is deleted afterward.
func TestSetupScriptExecutesEndToEnd(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Set the setup script as a user default via stdin so real newlines are preserved.
	script := "#!/bin/sh\necho setup-e2e-ok > /tmp/setup-marker\n"
	out, err := Env.servers.RunExeDevSSHCommandWithStdin(
		Env.context(t), keyFile, []byte(script),
		"defaults", "write", "dev.exe", "new.setup-script",
	)
	if err != nil {
		t.Fatalf("defaults write failed: %v\n%s", err, out)
	}

	boxName := boxName(t)
	pty.SendLine(fmt.Sprintf("new --name=%s", boxName))
	pty.WantRE("Creating .*" + boxName)
	pty.Want("Ready")
	pty.WantPrompt()

	waitForSSH(t, boxName, keyFile)

	// The systemd service may not have run yet. Retry until the marker appears.
	var found bool
	for range 30 {
		cmd := boxSSHCommand(t, boxName, keyFile, "cat", "/tmp/setup-marker")
		cmdOut, cmdErr := cmd.CombinedOutput()
		if cmdErr == nil && strings.Contains(string(cmdOut), "setup-e2e-ok") {
			found = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !found {
		// Dump debug info
		cmd := boxSSHCommand(t, boxName, keyFile, "sudo", "systemctl", "status", "exe-setup.service")
		debugOut, _ := cmd.CombinedOutput()
		t.Logf("exe-setup.service status:\n%s", debugOut)
		cmd = boxSSHCommand(t, boxName, keyFile, "sudo", "journalctl", "-u", "exe-setup.service", "--no-pager")
		debugOut, _ = cmd.CombinedOutput()
		t.Logf("exe-setup.service journal:\n%s", debugOut)
		t.Fatal("setup script did not execute: /tmp/setup-marker not found after 15s")
	}

	// Verify the setup script file was deleted (ExecStartPost runs after ExecStart,
	// so there's a small window after the marker appears but before the file is removed).
	var deleted bool
	for range 30 {
		cmd := boxSSHCommand(t, boxName, keyFile, "test", "-f", "/exe.dev/setup")
		if err := cmd.Run(); err != nil {
			deleted = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !deleted {
		t.Error("/exe.dev/setup still exists after execution; expected it to be deleted")
	}

	// Clean up default
	pty.SendLine("defaults delete dev.exe new.setup-script")
	pty.WantPrompt()

	cleanupBox(t, keyFile, boxName)
}
