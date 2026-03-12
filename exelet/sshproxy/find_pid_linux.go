//go:build linux

package sshproxy

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// findListeningPID finds the PID of a process listening on the given TCP port.
// It reads /proc/net/tcp to find socket inodes, then scans /proc/*/fd/ to find which process owns one.
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
// This is used to detect and clean up duplicate socat processes.
func findAllListeningPIDs(port int) ([]int, error) {
	inodes, err := findAllListeningInodes(port)
	if err != nil {
		return nil, err
	}
	if len(inodes) == 0 {
		return nil, nil
	}

	var pids []int
	seen := make(map[int]bool)
	for _, inode := range inodes {
		pid, err := findProcessByInode(inode)
		if err != nil || pid == 0 {
			continue
		}
		if !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// findAllListeningInodes reads /proc/net/tcp and /proc/net/tcp6 to find all sockets listening on the given port.
func findAllListeningInodes(port int) ([]uint64, error) {
	var allInodes []uint64
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		inodes, err := findListeningInodesInFile(path, port)
		if err != nil {
			continue // File might not exist (e.g., IPv6 disabled)
		}
		allInodes = append(allInodes, inodes...)
	}
	return allInodes, nil
}

// findListeningInodesInFile parses a /proc/net/tcp or /proc/net/tcp6 file to find all listening sockets on a port.
// Format: sl local_address rem_address st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode ...
// State 0A = LISTEN
func findListeningInodesInFile(path string, port int) ([]uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Port in hex, uppercase
	portHex := fmt.Sprintf("%04X", port)

	var inodes []uint64
	scanner := bufio.NewScanner(file)
	scanner.Scan() // Skip header line

	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		// local_address is field 1, format: IP:PORT (both in hex)
		localAddr := fields[1]
		parts := strings.Split(localAddr, ":")
		if len(parts) != 2 {
			continue
		}
		localPort := parts[1]

		// State is field 3, 0A = LISTEN
		state := fields[3]

		// Check if this is a listening socket on our port
		if localPort == portHex && state == "0A" {
			// Inode is field 9
			inode, err := strconv.ParseUint(fields[9], 10, 64)
			if err != nil {
				continue
			}
			inodes = append(inodes, inode)
		}
	}

	return inodes, scanner.Err()
}

// findProcessByInode scans /proc/*/fd/* to find which process owns the given socket inode.
func findProcessByInode(targetInode uint64) (int, error) {
	procDir, err := os.Open("/proc")
	if err != nil {
		return 0, err
	}
	defer procDir.Close()

	entries, err := procDir.Readdirnames(-1)
	if err != nil {
		return 0, err
	}

	socketLink := fmt.Sprintf("socket:[%d]", targetInode)

	for _, entry := range entries {
		// Check if this is a numeric directory (PID)
		pid, err := strconv.Atoi(entry)
		if err != nil {
			continue
		}

		// Scan fd directory
		fdPath := fmt.Sprintf("/proc/%d/fd", pid)
		fdDir, err := os.Open(fdPath)
		if err != nil {
			continue // Permission denied or process exited
		}

		fds, err := fdDir.Readdirnames(-1)
		fdDir.Close()
		if err != nil {
			continue
		}

		for _, fd := range fds {
			linkPath := filepath.Join(fdPath, fd)
			link, err := os.Readlink(linkPath)
			if err != nil {
				continue
			}
			if link == socketLink {
				return pid, nil
			}
		}
	}

	return 0, nil
}
