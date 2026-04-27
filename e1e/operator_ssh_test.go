// Tests that cloud-hypervisor exposes a hybrid-vsock unix socket on the
// exelet host, and that exe-init has started a Go SSH server on AF_VSOCK
// inside the guest. The operator reaches the in-guest sshd by speaking the
// CH hybrid-vsock handshake ("CONNECT <port>\n" / "OK ...\n") and then a
// normal SSH connection over the resulting stream.

package e1e

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
	"golang.org/x/crypto/ssh"
)

func TestOperatorSSHOverVsock(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)

	if len(Env.servers.Exelets) == 0 {
		t.Fatal("no exelets in test environment")
	}
	exelet := Env.servers.Exelets[0]
	if exelet.RemoteHost != "" {
		t.Skip("operator-ssh test requires a local exelet (needs direct filesystem access)")
	}

	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	pty.Disconnect()
	defer cleanupBox(t, keyFile, boxName)

	waitForSSH(t, boxName, keyFile)

	ctx := Env.context(t)
	exeletClient := exelet.Client()
	instanceID := instanceIDByName(t, ctx, exeletClient, boxName)

	socketPath := testinfra.OperatorSSHSocketPath(exelet.DataDir, instanceID)
	t.Logf("operator-ssh hybrid-vsock socket: %s", socketPath)

	// On failure, dump a few guest-side diagnostics via the user ssh path so
	// future regressions are easier to triage.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, cmd := range [][]string{
			{"ls", "-la", "/dev/vsock"},
			{"sudo", "ss", "-lnpA", "vsock"},
			{"sudo", "pgrep", "-fa", "exe-init"},
		} {
			if b, err := boxSSHCommand(t, boxName, keyFile, cmd...).CombinedOutput(); err == nil {
				t.Logf("guest %v:\n%s", cmd, b)
			}
		}
	})

	// sun_path limit; keep an assertion so regressions trip here rather than
	// at cloud-hypervisor bind time.
	if len(socketPath) > 107 {
		t.Fatalf("operator-ssh socket path is too long (%d > 107): %q", len(socketPath), socketPath)
	}

	keyBytes, err := Env.servers.Exed.BoxClientSSHKey(ctx, boxName)
	if err != nil {
		t.Fatalf("BoxClientSSHKey: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		t.Fatalf("parse client key: %v", err)
	}

	client, err := testinfra.OperatorSSHClient(ctx, exelet, instanceID, signer, 60*time.Second)
	if err != nil {
		t.Fatalf("could not connect to operator ssh: %v", err)
	}
	defer client.Close()

	t.Run("whoami", func(t *testing.T) {
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer sess.Close()
		out, err := sess.CombinedOutput("whoami")
		if err != nil {
			t.Fatalf("whoami: %v (out=%q)", err, out)
		}
		if strings.TrimSpace(string(out)) != "root" {
			t.Fatalf("whoami = %q, want root", strings.TrimSpace(string(out)))
		}
	})

	t.Run("nonzero_exit", func(t *testing.T) {
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer sess.Close()
		err = sess.Run("exit 17")
		ee, ok := err.(*ssh.ExitError)
		if !ok {
			t.Fatalf("want *ssh.ExitError, got %T: %v", err, err)
		}
		if ee.ExitStatus() != 17 {
			t.Fatalf("exit = %d, want 17", ee.ExitStatus())
		}
	})

	t.Run("pty_echo", func(t *testing.T) {
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer sess.Close()
		if err := sess.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
			t.Fatalf("RequestPty: %v", err)
		}
		out, err := sess.CombinedOutput("echo hi")
		if err != nil {
			t.Fatalf("echo under pty: %v (out=%q)", err, out)
		}
		if !bytes.Contains(out, []byte("hi")) {
			t.Fatalf("pty echo output = %q, want to contain \"hi\"", out)
		}
	})

	t.Run("large_stdout", func(t *testing.T) {
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer sess.Close()
		const n = 512 * 1024
		out, err := sess.Output(fmt.Sprintf("yes x | head -c %d", n))
		if err != nil {
			t.Fatalf("large stdout: %v", err)
		}
		if len(out) != n {
			t.Fatalf("got %d bytes, want %d", len(out), n)
		}
	})

	t.Run("bad_auth", func(t *testing.T) {
		_, badPriv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("gen key: %v", err)
		}
		badSigner, err := ssh.NewSignerFromKey(badPriv)
		if err != nil {
			t.Fatalf("signer: %v", err)
		}
		conn, cleanup, err := testinfra.DialOperatorSSH(ctx, exelet, instanceID)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer cleanup()
		cfg := &ssh.ClientConfig{
			User:            "root",
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(badSigner)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         10 * time.Second,
		}
		if _, _, _, err := ssh.NewClientConn(conn, "vsock", cfg); err == nil {
			t.Fatal("expected auth failure, got success")
		}
	})
}
