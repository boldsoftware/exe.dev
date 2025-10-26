// This file contains tests for box management functionality.

package e1e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"exe.dev/vouch"
)

func TestSSHWorks(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Create a box.
	boxName := newBox(t, pty)
	pty.disconnect()

	// SSH to it.
	pty = sshToBox(t, boxName, keyFile)
	pty.reject("Permission denied") // fail fast on common known failure mode
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want("exedev")
	pty.want("\n") // exedev is also in the prompt! require a newline after it.
	pty.wantPrompt()
	pty.disconnect()

	pty = sshToExeDev(t, keyFile)
	// They've created a box, so we should have stopped hinting at them about it.
	pty.reject("create your first box")
	pty.wantPrompt()
	pty.disconnect()

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

	// Cleanup
	pty = sshToExeDev(t, keyFile)
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
	pty.sendLine(fmt.Sprintf("new --name=%s --prompt=%q --prompt-model=predictable", boxName, prompt))
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

func TestDockerWorks(t *testing.T) {
	vouch.For("philip")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Create a box with default systemd command.
	boxName := newBox(t, pty)
	pty.disconnect()

	// Wait for SSH to be responsive (systemd may take time to initialize).
	var err error
	for range 150 {
		err = boxSSHCommand(t, boxName, keyFile, "true").Run()
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("box ssh did not come up, last error: %v", err)
	}

	// Wait for docker to be available. Docker uses socket activation and starts on first use,
	// but we need to give systemd a bit more time after SSH is ready.
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
	out, err := boxSSHCommand(t, boxName, keyFile, "sudo", "docker", "run", "--rm", "alpine:latest", "echo", "hello").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to run docker command: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "hello") {
		t.Fatalf("expected 'hello' in docker output, got: %s", out)
	}

	// Cleanup
	pty = sshToExeDev(t, keyFile)
	pty.deleteBox(boxName)
	pty.disconnect()
}
