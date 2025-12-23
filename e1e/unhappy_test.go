// This file tests "unhappy path" scenarios.

package e1e

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

func TestRequiresSSHKey(t *testing.T) {
	t.Parallel()
	// CI intermittently is missing a newline in this test.
	// Failures look like golden file diffs like:
	//   -Press Enter to close this connection.
	//   -USER@localhost: Permission denied (publickey,keyboard-interactive).
	//   +Press Enter to close this connection.USER@localhost: Permission denied (publickey,keyboard-interactive).
	// I don't know why this happens, and it's not great...but it's not worth fighting over now.
	// Suppress golden output for this test.
	noGolden(t)

	pty := makePty(t, "ssh localhost [no keys]")

	sshCmd := exec.CommandContext(Env.context(t), "ssh",
		"-p", fmt.Sprint(Env.piperd.Port),
		"-o", "StrictHostKeyChecking=no",
		"-o", "PubkeyAuthentication=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-F", "/dev/null", // don't use any config file -> no ssh keys
		"localhost",
	)
	sshCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=") // disable SSH agent

	pty.attachAndStart(sshCmd)

	pty.want("SSH keys are required to access exe.dev")
	pty.want("Press Enter to close this connection.")
	pty.sendLine("")
	// Confirm we have the correct auth methods advertised.
	pty.want("Permission denied (publickey,keyboard-interactive).")
	pty.wantEOF()
}

func TestExeDevRejectsSCP(t *testing.T) {
	t.Parallel()

	// The exact error varies depending on the local scp program.
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.disconnect()

	pty = makePty(t, "scp localhost")

	sshCmd := exec.CommandContext(Env.context(t), "scp",
		"-P", fmt.Sprint(Env.piperd.Port),
		"-o", "IdentityFile="+keyFile,
		"-o", "IdentityAgent=none",
		"-o", "StrictHostKeyChecking=no",
		"-o", "PreferredAuthentications=publickey",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "PubkeyAuthentication=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "ChallengeResponseAuthentication=no",
		"-o", "IdentitiesOnly=yes",
		"-F", "/dev/null",
		"unhappy_test.go",
		"localhost:foo.txt",
	)
	sshCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=") // disable SSH agent

	pty.attachAndStart(sshCmd)

	pty.reject("subsystem request failed")
	pty.wantRe(`scp/sftp is not supported on the exe.dev server.|command not found: "scp -t`)
	pty.wantEOF()
}
