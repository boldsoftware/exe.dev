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
// the operator-ssh command.
func TestBuildOperatorSSHCommand_ContainsParts(t *testing.T) {
	t.Parallel()
	cmd := buildOperatorSSHCommand("exelet-99", "/data/exelet/runtime/abc/opssh.sock", 2222)
	for _, want := range []string{
		"ssh -o StrictHostKeyChecking=no",
		"-o 'ProxyCommand=bash -c \"",
		`coproc P { ssh exelet-99`,
		`sudo socat -t30 - UNIX-CONNECT:/data/exelet/runtime/abc/opssh.sock`,
		`echo CONNECT 2222`,
		`>&\${P[1]}`,
		`<&\${P[0]}`,
		"root@vsock",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("printed command missing %q\ncmd: %s", want, cmd)
		}
	}
}

// TestBuildOperatorSSHCommand_QuotingRoundTrip simulates what happens when an
// operator pastes the printed command into their shell and ssh forks the
// ProxyCommand: we verify that a *real* /bin/sh can parse the command line
// down to the eventual `bash -c <script>` invocation, and that the script
// matches what we expect.
//
// To avoid actually running ssh/socat, we shim `ssh` in PATH. The shim records
// argv it received; the FIRST argv we capture is the argv ssh would have been
// invoked with by the operator (i.e. the printed command's `ssh ... root@vsock`
// form). We then look at the -o values that shim received to extract the
// ProxyCommand value, and execute that via /bin/sh -c with `bash` shimmed to
// capture *its* argv.
func TestBuildOperatorSSHCommand_QuotingRoundTrip(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	cmd := buildOperatorSSHCommand("exelet-99", "/data/exelet/runtime/abc/opssh.sock", 2222)

	shimDir := t.TempDir()
	sshLog := filepath.Join(shimDir, "ssh-argv")
	bashLog := filepath.Join(shimDir, "bash-argv")
	dumpScript := func(path string) string {
		return "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\0' \"$a\" >> " + path + "; done\n"
	}
	if err := os.WriteFile(filepath.Join(shimDir, "ssh"), []byte(dumpScript(sshLog)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shimDir, "bash"), []byte(dumpScript(bashLog)), 0o755); err != nil {
		t.Fatal(err)
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
	if !strings.HasPrefix(proxyCmd, `bash -c "`) {
		t.Fatalf("unexpected ProxyCommand: %q", proxyCmd)
	}

	// ssh forks /bin/sh -c <ProxyCommand>. Reproduce that.
	os.Remove(bashLog)
	c2 := exec.Command("/bin/sh", "-c", proxyCmd)
	c2.Env = append(os.Environ(), "PATH="+shimDir+":/usr/bin:/bin")
	if out, err := c2.CombinedOutput(); err != nil {
		t.Fatalf("run ProxyCommand: %v\nout: %s", err, out)
	}
	bashArgs := readNullArgs(t, bashLog)
	if len(bashArgs) != 2 || bashArgs[0] != "-c" {
		t.Fatalf("shimmed bash got args %q; want [-c <script>]", bashArgs)
	}
	script := bashArgs[1]
	for _, w := range []string{
		`coproc P { ssh exelet-99 "sudo socat -t30 - UNIX-CONNECT:/data/exelet/runtime/abc/opssh.sock"; }`,
		`echo CONNECT 2222 >&${P[1]}`,
		`IFS= read -r _ <&${P[0]}`,
		`cat <&${P[0]} & exec cat >&${P[1]}`,
	} {
		if !strings.Contains(script, w) {
			t.Errorf("final bash script missing %q\nscript: %s", w, script)
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
