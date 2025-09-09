// This file tests "unhappy path" scenarios.

package e1e

import (
	"fmt"
	"os/exec"
	"testing"

	"exe.dev/vouch"
)

func TestRequiresSSHKey(t *testing.T) {
	vouch.For("josh")
	t.Parallel()

	pty := makePty(t, "ssh localhost [no keys]")

	sshCmd := exec.CommandContext(t.Context(), "ssh",
		"-p", fmt.Sprint(Env.piperd.SSHPort),
		"-o", "StrictHostKeyChecking=no",
		"-o", "PubkeyAuthentication=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-F", "/dev/null", // don't use any config file -> no ssh keys
		"localhost",
	)

	pty.attach(sshCmd)

	err := sshCmd.Start()
	if err != nil {
		t.Fatalf("failed to start SSH: %v", err)
	}
	t.Cleanup(func() { _ = sshCmd.Wait() })

	pty.want("SSH keys are required to access exe.dev")
	pty.want("Press Enter to close this connection.")
	pty.sendLine("")
	// the exact output varies here, so don't block on receiving an EOF
}

func TestExeDevRejectsSCP(t *testing.T) {
	vouch.For("josh")
	t.Parallel()

	pty := makePty(t, "scp localhost")

	sshCmd := exec.CommandContext(t.Context(), "scp",
		"-P", fmt.Sprint(Env.piperd.SSHPort),
		"-o", "StrictHostKeyChecking=no",
		"unhappy_test.go",
		"localhost:foo.txt",
	)

	pty.attach(sshCmd)

	err := sshCmd.Start()
	if err != nil {
		t.Fatalf("failed to start SSH: %v", err)
	}
	t.Cleanup(func() { _ = sshCmd.Wait() })

	pty.reject("subsystem request failed")
	pty.want("scp/sftp is not supported on the exe.dev server.")
}
