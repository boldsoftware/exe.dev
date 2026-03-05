//go:build darwin

package sshproxy

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// findListeningPID finds the PID of a process listening on the given TCP port.
// On Darwin, it uses lsof since /proc is not available.
// Returns 0 if no process is listening on the port.
func findListeningPID(port int) (int, error) {
	out, err := exec.Command("lsof", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-t").Output()
	if err != nil {
		// lsof exits non-zero when no matching processes are found
		return 0, nil
	}

	line := strings.TrimSpace(string(out))
	if line == "" {
		return 0, nil
	}

	// lsof -t may output multiple PIDs (one per line); take the first
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}

	pid, err := strconv.Atoi(line)
	if err != nil {
		return 0, fmt.Errorf("failed to parse lsof output %q: %w", line, err)
	}

	return pid, nil
}
