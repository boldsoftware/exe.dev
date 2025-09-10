package e1e

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"exe.dev/vouch"
)

func TestSCPWorks(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)

	// scp a file to it. use our private key. why not.
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

	pty = sshToBox(t, boxName, keyFile)
	pty.reject("Permission denied")
	pty.wantPrompt()
	pty.sendLine("ls key.txt")
	pty.want("key.txt")
	pty.want("\n")
	pty.wantPrompt()
	pty.disconnect()
}
