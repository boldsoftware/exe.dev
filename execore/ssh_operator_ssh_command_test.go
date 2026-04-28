package execore

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExeletHostFromCtrhost(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"tcp://exelet-01:9080", "exelet-01"},
		{"tcp://exelet-01.tail-scale.ts.net:9080", "exelet-01.tail-scale.ts.net"},
		{"exelet-01:9080", "exelet-01"},
		{"exelet-01", "exelet-01"},
		{"tcp://[::1]:9080", "[::1]"},
		{"", ""},
	}
	for _, c := range cases {
		if got := exeletHostFromCtrhost(c.in); got != c.want {
			t.Errorf("exeletHostFromCtrhost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestBuildOperatorSSHCommand_ContainsParts checks the literal printed form of
// the operator-ssh command (run from the exelet host).
func TestBuildOperatorSSHCommand_ContainsParts(t *testing.T) {
	t.Parallel()
	cmd := buildOperatorSSHCommand("/data/exelet/runtime/abc/opssh.sock", 2222)
	for _, want := range []string{
		"ssh -i /tmp/opssh-key",
		"-o StrictHostKeyChecking=no",
		`echo CONNECT 2222`,
		`sudo socat -t30 - UNIX-CONNECT:/data/exelet/runtime/abc/opssh.sock`,
		`read _`,
		"root@vsock",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("printed command missing %q\ncmd: %s", want, cmd)
		}
	}
}

// TestBuildOperatorSSHCommand_QuotingRoundTrip simulates what happens when an
// operator pastes the printed command into their shell on the exelet host.
// We shim `ssh` and `sudo` in PATH to capture argv without running anything,
// then verify the ProxyCommand is parseable by /bin/sh.
func TestBuildOperatorSSHCommand_QuotingRoundTrip(t *testing.T) {
	t.Parallel()
	cmd := buildOperatorSSHCommand("/data/exelet/runtime/abc/opssh.sock", 2222)

	shimDir := t.TempDir()
	sshLog := filepath.Join(shimDir, "ssh-argv")
	dumpScript := func(path string) string {
		return "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\0' \"$a\" >> " + path + "; done\n"
	}
	for _, name := range []string{"ssh", "sudo"} {
		if err := os.WriteFile(filepath.Join(shimDir, name), []byte(dumpScript(sshLog)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Run the entire printed command via /bin/sh -c, with our shims taking
	// precedence on PATH.
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Env = append(os.Environ(), "PATH="+shimDir+":/usr/bin:/bin")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("run printed cmd: %v\nout: %s", err, out)
	}

	sshArgs := readNullArgs(t, sshLog)
	// Find ProxyCommand= entry.
	var proxyCmd string
	for i, a := range sshArgs {
		if a == "-o" && i+1 < len(sshArgs) && strings.HasPrefix(sshArgs[i+1], "ProxyCommand=") {
			proxyCmd = strings.TrimPrefix(sshArgs[i+1], "ProxyCommand=")
		}
	}
	if proxyCmd == "" {
		t.Fatalf("no ProxyCommand in ssh argv: %q", sshArgs)
	}
	if !strings.HasPrefix(proxyCmd, `sh -c "`) {
		t.Fatalf("unexpected ProxyCommand: %q", proxyCmd)
	}

	// ssh forks /bin/sh -c <ProxyCommand>. Reproduce that and verify it parses.
	os.Remove(sshLog)
	c2 := exec.Command("/bin/sh", "-c", proxyCmd)
	c2.Env = append(os.Environ(), "PATH="+shimDir+":/usr/bin:/bin")
	if out, err := c2.CombinedOutput(); err != nil {
		t.Fatalf("run ProxyCommand: %v\nout: %s", err, out)
	}

	// The sudo shim should have captured the socat invocation.
	sudoArgs := readNullArgs(t, sshLog)
	joined := strings.Join(sudoArgs, " ")
	for _, w := range []string{
		"socat",
		"UNIX-CONNECT:/data/exelet/runtime/abc/opssh.sock",
	} {
		if !strings.Contains(joined, w) {
			t.Errorf("sudo argv missing %q\nargv: %q", w, sudoArgs)
		}
	}
}

func readNullArgs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
}
