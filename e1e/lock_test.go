package e1e

import (
	"strings"
	"testing"
)

func TestLock(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	defer pty.Disconnect()

	box := newBox(t, pty)
	waitForSSH(t, box, keyFile)

	t.Run("LockUsage", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("lock")
		repl.Want("usage")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("UnlockUsage", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("unlock")
		repl.Want("usage")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("UnlockNotLocked", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("unlock " + box)
		repl.Want("not locked")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("LockVM", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("lock " + box + " important-data")
		repl.Want("Locked")
		repl.Want("important-data")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("LockAlreadyLocked", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("lock " + box)
		repl.Want("already locked")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("RmWhileLocked", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("rm " + box)
		repl.Want("locked")
		repl.Want("unlock")
		repl.Reject("success")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("UnlockFromExecRejected", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "unlock", box)
		if err == nil {
			t.Fatalf("expected unlock via exec to fail, got: %s", out)
		}
		if !strings.Contains(string(out), "interactive repl") {
			t.Fatalf("expected error about interactive repl, got: %s", out)
		}
	})

	t.Run("UnlockVM", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("unlock " + box)
		repl.Want("Unlocked")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("RmAfterUnlock", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("rm " + box)
		repl.Want("Deleting")
		repl.Want("success")
		repl.WantPrompt()
		repl.Disconnect()
	})
}
