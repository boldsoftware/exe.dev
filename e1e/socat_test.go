package e1e

import (
	"bytes"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
	"exe.dev/exelet/sshproxy"
	"exe.dev/tslog"
)

// TestSocat is a temporary test that tests that we can open
// some ssh connections with an exelet that does not use exepipe,
// restart the exelet to use exepipe, and see that the listening
// socat processes are killed but the copying ones are not.
// This can be removed after we deploy exelets that use exepipe
// by default.
func TestSocat(t *testing.T) {
	// This test can't be parallel.
	reserveVMs(t, 1)
	noGolden(t)
	e1eTestsOnlyRunOnce(t)

	if *flagUseExepipe {
		t.Skip("this test only works if the default is socat")
	}

	existingSocats := findSocatProcesses(t, false)

	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty, testinfra.BoxOpts{NoEmail: true})
	pty.Disconnect()
	defer cleanupBox(t, keyFile, boxName)

	waitForSSH(t, boxName, keyFile)

	cmd := boxSSHCommand(t, boxName, keyFile, "/bin/bash")
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatalf("ssh to VM failed: %v", err)
	}

	if _, err := stdinPipe.Write([]byte("echo $SHELL\n")); err != nil {
		t.Fatalf("stdin pipe write failed: %v", err)
	}
	var buf [1024]byte
	n, err := outPipe.Read(buf[:])
	if err != nil {
		t.Fatalf("stdin pipe read failed: %v", err)
	}
	if !bytes.Contains(buf[:n], []byte("bash")) {
		t.Fatalf("stdin pipe read %q, does not contain %q", buf[:n], "bash")
	}

	socats := findSocatProcesses(t, false)
	socats = removeExistingSocats(socats, existingSocats)

	if len(socats) == 0 {
		t.Fatal("could not find any existing socat processes with active ssh")
	}

	// A better test would restart exelet,
	// but this is good enough for temporary work.
	sshproxy.StopSocatListeners(t.Context(), tslog.Slogger(t))

	// Make sure the existing connection is undisturbed.
	if _, err := stdinPipe.Write([]byte("echo $SHELL\n")); err != nil {
		t.Fatalf("stdin pipe write failed: %v", err)
	}
	n, err = outPipe.Read(buf[:])
	if err != nil {
		t.Fatalf("stdin pipe read failed: %v", err)
	}
	if !bytes.Contains(buf[:n], []byte("bash")) {
		t.Fatalf("stdin pipe read %q, does not contain %q", buf[:n], "bash")
	}
}

var processRE = regexp.MustCompile(`"socat",pid=([0-9]+),`)

// findSocatProcesses returns the PIDs of the currently running
// socat processes. If listening returns listening processes,
// otherwise established connections.
func findSocatProcesses(t *testing.T, listening bool) []int {
	args := []string{
		"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		Env.servers.Exelets[0].RemoteHost,
		"sudo",
		"ss",
		"-tnp",
	}
	if listening {
		args = append(args, "-l")
	}
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh of ss failed: %v\n%s", err, out)
	}

	var ret []int
	first := true
	for line := range bytes.Lines(out) {
		if first {
			// Skip the headers
			first = false
			continue
		}
		if !bytes.Contains(line, []byte("socat")) {
			continue
		}
		fields := strings.Fields(string(line))
		if len(fields) != 6 {
			t.Errorf("could not parse ss line: %q", line)
			continue
		}
		isListening := fields[0] == "LISTEN"
		if listening != isListening {
			continue
		}
		matches := processRE.FindStringSubmatch(fields[5])
		if len(matches) == 0 {
			t.Logf("no match for %q", fields[5])
			continue
		}
		pidStr := matches[1]
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			t.Errorf("could not parse pid %q: %v", pidStr, err)
			continue
		}
		ret = append(ret, pid)
	}
	return ret
}

// removeExistingSocats removes the previously existing socat processes.
func removeExistingSocats(socats, existingSocats []int) []int {
	ret := make([]int, 0, len(socats))
	for _, pid := range socats {
		if !slices.Contains(existingSocats, pid) {
			ret = append(ret, pid)
		}
	}
	return ret
}
