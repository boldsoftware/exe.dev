package exelets

import (
	"context"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
)

func TestTwoExelets(t *testing.T) {
	// Don't use t.Context here, the exelets should be around
	// for other tests.
	if err := ensureExeletCount(context.Background(), 2); err != nil {
		t.Fatal(err)
	}

	exeletAddrs := []string{
		exelets[0].Address,
		exelets[1].Address,
	}

	// Don't use t.Context here, the restarted exed should be
	// around for other tests.
	if err := serverEnv.Exed.Restart(context.Background(), exeletAddrs, exeletTestRunIDs[0]); err != nil {
		t.Fatal(err)
	}

	pty, _, err := testinfra.MakePTY("", "ssh localhost", true)
	if err != nil {
		t.Fatal(err)
	}
	email := t.Name() + "@example.com"
	_, keyFile, sshCmd, err := serverEnv.RegisterForExeDevWithEmail(t.Context(), pty, email, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sshCmd.Wait() })

	boxName, err := serverEnv.NewBox(t.Name(), exeletTestRunIDs[0], pty)
	if err != nil {
		t.Fatal(err)
	}

	if err := pty.Disconnect(); err != nil {
		t.Error(err)
	}

	if msg, err := serverEnv.Email.WaitForEmail(email); err != nil {
		t.Error(err)
	} else if !strings.Contains(msg.Subject, boxName) {
		t.Errorf("got email subject %q, expected it to contain box name %q", msg.Subject, boxName)
	}

	if err := serverEnv.WaitForBoxSSHServer(t.Context(), boxName, keyFile); err != nil {
		t.Fatal(err)
	}

	cmd := serverEnv.BoxSSHCommand(t.Context(), boxName, keyFile, "true")
	cmd.Stdout = t.Output()
	cmd.Stderr = t.Output()
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to run true: %v", err)
	}

	pty, _, err = testinfra.MakePTY("", "ssh localhost", true)
	if err != nil {
		t.Fatal(err)
	}
	sshCmd2, err := serverEnv.SSHToExeDev(t.Context(), pty, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sshCmd2.Wait() })

	if err := pty.SendLine("rm " + boxName); err != nil {
		t.Fatal(err)
	}
	if err := pty.Want("Deleting"); err != nil {
		t.Fatal(err)
	}
	pty.Reject("internal error")
	if err := pty.Want("success"); err != nil {
		t.Fatal(err)
	}
	if err := pty.WantPrompt(); err != nil {
		t.Fatal(err)
	}
	if err := pty.Disconnect(); err != nil {
		t.Error(err)
	}
}
