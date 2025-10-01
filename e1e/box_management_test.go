// This file contains tests for box management functionality.

package e1e

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

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
