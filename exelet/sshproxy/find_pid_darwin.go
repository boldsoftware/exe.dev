//go:build darwin

package sshproxy

import (
	"os/exec"
	"strconv"
	"strings"
)

// findListeningPID finds the PID of a process listening on the given TCP port.
// On Darwin, it uses lsof since /proc is not available.
// Returns 0 if no process is listening on the port.
func findListeningPID(port int) (int, error) {
	pids, err := findAllListeningPIDs(port)
	if err != nil {
		return 0, err
	}
	if len(pids) == 0 {
		return 0, nil
	}
	return pids[0], nil
}

// findAllListeningPIDs finds all PIDs of processes listening on the given TCP port.
func findAllListeningPIDs(port int) ([]int, error) {
	out, err := exec.Command("lsof", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-t").Output()
	if err != nil {
		// lsof exits non-zero when no matching processes are found
		return nil, nil
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var pids []int
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}
