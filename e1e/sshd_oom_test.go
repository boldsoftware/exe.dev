// This file contains tests for sshd OOM protection.

package e1e

import (
	"strings"
	"testing"
)

// TestSshdOOMProtection verifies that the sshd process inside VMs
// is protected from the OOM killer by having oom_score_adj set to -1000.
func TestSshdOOMProtection(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	pty.disconnect()

	waitForSSH(t, boxName, keyFile)

	// Find the sshd process PID and check its oom_score_adj.
	// The main sshd process (not sshd-session children) should have -1000.
	// exe-init starts sshd at /exe.dev/bin/sshd, and sets oom_score_adj to -1000.
	//
	// Use pgrep to find sshd processes. The parent sshd process is PID 1's child.
	out, err := boxSSHCommand(t, boxName, keyFile, "pgrep", "-x", "sshd").CombinedOutput()
	if err != nil {
		// Debug: list all processes to understand what's running
		debug, _ := boxSSHCommand(t, boxName, keyFile, "ps", "aux").CombinedOutput()
		t.Fatalf("failed to find sshd PID: %v\noutput: %s\nps aux:\n%s", err, out, debug)
	}

	// pgrep returns one PID per line. Take the first one (the parent sshd).
	pids := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(pids) == 0 || pids[0] == "" {
		t.Fatalf("no sshd process found")
	}

	// Find the parent sshd (the one with PPID 1, started by exe-init).
	// Check oom_score_adj for each sshd until we find the one that's -1000.
	var foundOOMProtected bool
	for _, pid := range pids {
		pid = strings.TrimSpace(pid)
		if pid == "" {
			continue
		}
		out, err = boxSSHCommand(t, boxName, keyFile, "cat", "/proc/"+pid+"/oom_score_adj").CombinedOutput()
		if err != nil {
			continue
		}
		oomScoreAdj := strings.TrimSpace(string(out))
		if oomScoreAdj == "-1000" {
			foundOOMProtected = true
			break
		}
	}

	if !foundOOMProtected {
		// Get detailed info for debugging
		debug, _ := boxSSHCommand(t, boxName, keyFile,
			"bash", "-c", "for pid in $(pgrep -x sshd); do echo \"PID $pid: oom_score_adj=$(cat /proc/$pid/oom_score_adj 2>/dev/null || echo N/A)\"; done").CombinedOutput()
		t.Errorf("no sshd process has oom_score_adj=-1000\nsshd processes:\n%s", debug)
	}

	// Cleanup
	cleanupBox(t, keyFile, boxName)
}
