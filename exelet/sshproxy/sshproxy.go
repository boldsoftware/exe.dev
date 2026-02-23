package sshproxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SSHProxy represents a persistent SSH proxy using socat
type SSHProxy struct {
	InstanceID  string
	Port        int
	TargetIP    string
	TargetPort  int
	PID         int
	InstanceDir string
	BindIP      string // IP address to bind to (empty means all interfaces)
	log         *slog.Logger
}

// proxyMetadata is the JSON structure persisted to disk
type proxyMetadata struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	TargetIP  string    `json:"target_ip"`
	StartedAt time.Time `json:"started_at"`
}

// NewSSHProxy creates a new SSH proxy instance.
// bindIP specifies the IP address to bind to; empty string means all interfaces.
func NewSSHProxy(instanceID string, port int, targetIP, instanceDir, bindIP string, log *slog.Logger) *SSHProxy {
	return &SSHProxy{
		InstanceID:  instanceID,
		Port:        port,
		TargetIP:    targetIP,
		TargetPort:  22, // Always SSH port
		InstanceDir: instanceDir,
		BindIP:      bindIP,
		log:         log,
	}
}

// Start spawns a detached socat process for SSH forwarding.
// If a process is already listening on the port, it adopts that process instead of spawning a duplicate.
func (p *SSHProxy) Start() error {
	// Check if socat is available
	if _, err := exec.LookPath("socat"); err != nil {
		return fmt.Errorf("socat not found in PATH: %w", err)
	}

	// Check if there's already a process listening on this port.
	// This prevents duplicate socat processes when exelet restarts and the old socat is still running.
	if existingPID, err := findListeningPID(p.Port); err == nil && existingPID > 0 {
		p.PID = existingPID
		p.log.Info("adopted existing proxy process", "instance", p.InstanceID, "port", p.Port, "pid", existingPID)
		if err := p.SaveToDisk(); err != nil {
			return fmt.Errorf("failed to save proxy metadata after adopting: %w", err)
		}
		return nil
	}

	// Build socat command
	var listenAddr string
	if p.BindIP != "" {
		listenAddr = fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr,bind=%s", p.Port, p.BindIP)
	} else {
		listenAddr = fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", p.Port)
	}
	targetAddr := fmt.Sprintf("TCP:%s:%d,connect-timeout=3", p.TargetIP, p.TargetPort)

	cmd := exec.Command("socat", listenAddr, targetAddr)

	// Detach from parent process
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true, // Create new process group
	}

	// Redirect all I/O to prevent blocking
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	// Start the process
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start socat: %w", err)
	}

	p.PID = cmd.Process.Pid

	// Verify socat actually bound to the port. Without this check, socat
	// can fail to bind (e.g. EADDRINUSE from ephemeral port collision) and
	// exit immediately, while we incorrectly report success.
	const (
		pollInterval = 20 * time.Millisecond
		pollTimeout  = 500 * time.Millisecond
	)
	deadline := time.Now().Add(pollTimeout)
	listening := false
	for time.Now().Before(deadline) {
		if pid, err := findListeningPID(p.Port); err == nil && pid > 0 {
			listening = true
			break
		}
		time.Sleep(pollInterval)
	}
	if !listening {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return fmt.Errorf("socat failed to listen on port %d within %v", p.Port, pollTimeout)
	}

	p.log.Info("ssh proxy started", "instance", p.InstanceID, "port", p.Port, "target", targetAddr, "pid", p.PID)

	// Release the process so it runs independently
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("failed to release socat process: %w", err)
	}

	// Persist metadata to disk
	if err := p.SaveToDisk(); err != nil {
		// Try to kill the process we just started
		p.killProcess()
		return fmt.Errorf("failed to save proxy metadata: %w", err)
	}

	return nil
}

// Stop kills the socat process and removes metadata
func (p *SSHProxy) Stop() error {
	if p.PID == 0 {
		return fmt.Errorf("no PID to stop")
	}

	if err := p.killProcess(); err != nil {
		p.log.Warn("failed to kill socat process", "pid", p.PID, "error", err)
	}

	// Remove metadata file
	metadataPath := filepath.Join(p.InstanceDir, "process-sshproxy.json")
	if err := os.Remove(metadataPath); err != nil && !os.IsNotExist(err) {
		p.log.Warn("failed to remove proxy metadata", "path", metadataPath, "error", err)
	}

	p.log.Info("ssh proxy stopped", "instance", p.InstanceID, "port", p.Port, "pid", p.PID)
	p.PID = 0

	return nil
}

// killProcess attempts to kill the process gracefully, then forcefully
func (p *SSHProxy) killProcess() error {
	process, err := os.FindProcess(p.PID)
	if err != nil {
		return fmt.Errorf("failed to find process %d: %w", p.PID, err)
	}

	// Try SIGTERM first
	if err := process.Signal(syscall.SIGTERM); err != nil {
		// Process might already be dead
		if err.Error() == "os: process already finished" {
			return nil
		}
		// Try SIGKILL
		if killErr := process.Signal(syscall.SIGKILL); killErr != nil {
			return fmt.Errorf("failed to kill process %d: %w", p.PID, killErr)
		}
	}

	// Wait for the process to be reaped to prevent zombie processes
	// Use a goroutine with timeout to avoid blocking forever
	done := make(chan error, 1)
	go func() {
		_, waitErr := process.Wait()
		done <- waitErr
	}()

	// Wait up to 5 seconds for the process to exit
	select {
	case waitErr := <-done:
		if waitErr != nil {
			// Process might have already been reaped by init
			return nil
		}
		return nil
	case <-time.After(5 * time.Second):
		// Timeout - process didn't exit, but we tried
		p.log.Warn("timeout waiting for socat process to exit", "pid", p.PID)
		return nil
	}
}

// IsRunning checks if the socat process is still alive
func (p *SSHProxy) IsRunning() bool {
	if p.PID == 0 {
		return false
	}

	// Send signal 0 to check if process exists
	process, err := os.FindProcess(p.PID)
	if err != nil {
		return false
	}

	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// SaveToDisk persists proxy metadata to instance directory
func (p *SSHProxy) SaveToDisk() error {
	metadata := proxyMetadata{
		PID:       p.PID,
		Port:      p.Port,
		TargetIP:  p.TargetIP,
		StartedAt: time.Now(),
	}

	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	metadataPath := filepath.Join(p.InstanceDir, "process-sshproxy.json")
	if err := os.WriteFile(metadataPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	return nil
}

// LoadFromDisk loads proxy metadata from instance directory
func (p *SSHProxy) LoadFromDisk() error {
	metadataPath := filepath.Join(p.InstanceDir, "process-sshproxy.json")

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("proxy metadata not found")
		}
		return fmt.Errorf("failed to read metadata: %w", err)
	}

	var metadata proxyMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	p.PID = metadata.PID
	p.Port = metadata.Port
	p.TargetIP = metadata.TargetIP

	return nil
}

// GetPort returns the port number as a string (for compatibility with API)
func (p *SSHProxy) GetPort() string {
	return strconv.Itoa(p.Port)
}

// findListeningPID finds the PID of a process listening on the given TCP port.
// It reads /proc/net/tcp to find the socket inode, then scans /proc/*/fd/ to find which process owns it.
// Returns 0 if no process is listening on the port.
func findListeningPID(port int) (int, error) {
	// Find the socket inode for the listening port
	inode, err := findListeningInode(port)
	if err != nil {
		return 0, err
	}
	if inode == 0 {
		return 0, nil // No process listening on this port
	}

	// Find which process owns this inode
	pid, err := findProcessByInode(inode)
	if err != nil {
		return 0, err
	}

	return pid, nil
}

// findListeningInode reads /proc/net/tcp and /proc/net/tcp6 to find a socket listening on the given port.
// Returns the inode number of the socket, or 0 if not found.
func findListeningInode(port int) (uint64, error) {
	// Try both IPv4 and IPv6
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		inode, err := findListeningInodeInFile(path, port)
		if err != nil {
			continue // File might not exist (e.g., IPv6 disabled)
		}
		if inode > 0 {
			return inode, nil
		}
	}
	return 0, nil
}

// findListeningInodeInFile parses a /proc/net/tcp or /proc/net/tcp6 file to find a listening socket.
// Format: sl local_address rem_address st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode ...
// State 0A = LISTEN
func findListeningInodeInFile(path string, port int) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	// Port in hex, uppercase
	portHex := fmt.Sprintf("%04X", port)

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
			return inode, nil
		}
	}

	return 0, scanner.Err()
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
