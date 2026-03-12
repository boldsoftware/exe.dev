package sshproxy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// socatSSHProxy represents a persistent SSH proxy using socat.
type socatSSHProxy struct {
	instanceID  string
	port        int
	targetIP    string
	targetPort  int
	pid         int
	instanceDir string
	bindIP      string // IP address to bind to (empty means all interfaces)
	log         *slog.Logger
}

// proxyMetadata is the JSON structure persisted to disk
type proxyMetadata struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	TargetIP  string    `json:"target_ip"`
	StartedAt time.Time `json:"started_at"`
}

// newSocatSSHProxy creates a new SSH proxy instance.
// bindIP specifies the IP address to bind to; empty string means all interfaces.
func newSocatSSHProxy(instanceID string, port int, targetIP, instanceDir, bindIP string, log *slog.Logger) *socatSSHProxy {
	return &socatSSHProxy{
		instanceID:  instanceID,
		port:        port,
		targetIP:    targetIP,
		targetPort:  22, // Always SSH port
		instanceDir: instanceDir,
		bindIP:      bindIP,
		log:         log,
	}
}

// start spawns a detached socat process for SSH forwarding.
// If a process is already listening on the port, it adopts that process instead of spawning a duplicate.
func (p *socatSSHProxy) start() error {
	// Check if socat is available
	if _, err := exec.LookPath("socat"); err != nil {
		return fmt.Errorf("socat not found in PATH: %w", err)
	}

	// Check if there's already a process listening on this port.
	// This prevents duplicate socat processes when exelet restarts and the old socat is still running.
	if existingPID, err := findListeningPID(p.port); err == nil && existingPID > 0 {
		p.pid = existingPID
		p.log.Info("adopted existing proxy process", "instance", p.instanceID, "port", p.port, "pid", existingPID)
		if err := p.saveToDisk(); err != nil {
			return fmt.Errorf("failed to save proxy metadata after adopting: %w", err)
		}
		return nil
	}

	// Build socat command.
	// Note: we intentionally omit reuseaddr. Without it, a second socat
	// cannot bind to the same port (EADDRINUSE), which lets us detect and
	// adopt the existing process instead of silently spawning a duplicate.
	var listenAddr string
	if p.bindIP != "" {
		listenAddr = fmt.Sprintf("TCP-LISTEN:%d,fork,bind=%s", p.port, p.bindIP)
	} else {
		listenAddr = fmt.Sprintf("TCP-LISTEN:%d,fork", p.port)
	}
	targetAddr := fmt.Sprintf("TCP:%s:%d,connect-timeout=3", p.targetIP, p.targetPort)

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

	p.pid = cmd.Process.Pid

	// Verify socat actually bound to the port. Without reuseaddr, a
	// duplicate will fail with EADDRINUSE and exit immediately.
	const (
		pollInterval = 20 * time.Millisecond
		pollTimeout  = 500 * time.Millisecond
	)
	deadline := time.Now().Add(pollTimeout)
	listening := false
	for time.Now().Before(deadline) {
		if pid, err := findListeningPID(p.port); err == nil && pid > 0 {
			listening = true
			break
		}
		time.Sleep(pollInterval)
	}
	if !listening {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return fmt.Errorf("socat failed to listen on port %d within %v", p.port, pollTimeout)
	}

	// Check if a different process is the actual listener (i.e. our socat
	// exited due to EADDRINUSE but a pre-existing socat is still running).
	// Adopt the existing process instead of reporting failure.
	if listeningPID, err := findListeningPID(p.port); err == nil && listeningPID > 0 && listeningPID != p.pid {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		p.pid = listeningPID
		p.log.Info("adopted existing proxy process (bind conflict)", "instance", p.instanceID, "port", p.port, "pid", listeningPID)
		if err := p.saveToDisk(); err != nil {
			return fmt.Errorf("failed to save proxy metadata after adopting: %w", err)
		}
		return nil
	}

	p.log.Info("ssh proxy started", "instance", p.instanceID, "port", p.port, "target", targetAddr, "pid", p.pid)

	// Release the process so it runs independently
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("failed to release socat process: %w", err)
	}

	// Persist metadata to disk
	if err := p.saveToDisk(); err != nil {
		// Try to kill the process we just started
		p.killProcess()
		return fmt.Errorf("failed to save proxy metadata: %w", err)
	}

	return nil
}

// Stop kills the socat process and removes metadata
func (p *socatSSHProxy) stop() error {
	if p.pid == 0 {
		return fmt.Errorf("no PID to stop")
	}

	if err := p.killProcess(); err != nil {
		p.log.Warn("failed to kill socat process", "pid", p.pid, "error", err)
	}

	// Remove metadata file
	metadataPath := filepath.Join(p.instanceDir, "process-sshproxy.json")
	if err := os.Remove(metadataPath); err != nil && !os.IsNotExist(err) {
		p.log.Warn("failed to remove proxy metadata", "path", metadataPath, "error", err)
	}

	p.log.Info("ssh proxy stopped", "instance", p.instanceID, "port", p.port, "pid", p.pid)
	p.pid = 0

	return nil
}

// killProcess attempts to kill the process gracefully, then forcefully
func (p *socatSSHProxy) killProcess() error {
	process, err := os.FindProcess(p.pid)
	if err != nil {
		return fmt.Errorf("failed to find process %d: %w", p.pid, err)
	}

	// Try SIGTERM first
	if err := process.Signal(syscall.SIGTERM); err != nil {
		// Process might already be dead
		if err.Error() == "os: process already finished" {
			return nil
		}
		// Try SIGKILL
		if killErr := process.Signal(syscall.SIGKILL); killErr != nil {
			return fmt.Errorf("failed to kill process %d: %w", p.pid, killErr)
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
		p.log.Warn("timeout waiting for socat process to exit", "pid", p.pid)
		return nil
	}
}

// IsRunning checks if the socat process is still alive
func (p *socatSSHProxy) isRunning() bool {
	if p.pid == 0 {
		return false
	}

	// Send signal 0 to check if process exists
	process, err := os.FindProcess(p.pid)
	if err != nil {
		return false
	}

	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// SaveToDisk persists proxy metadata to instance directory
func (p *socatSSHProxy) saveToDisk() error {
	metadata := proxyMetadata{
		PID:       p.pid,
		Port:      p.port,
		TargetIP:  p.targetIP,
		StartedAt: time.Now(),
	}

	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	metadataPath := filepath.Join(p.instanceDir, "process-sshproxy.json")
	if err := os.WriteFile(metadataPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	return nil
}

// LoadFromDisk loads proxy metadata from instance directory
func (p *socatSSHProxy) loadFromDisk() error {
	metadataPath := filepath.Join(p.instanceDir, "process-sshproxy.json")

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

	p.pid = metadata.PID
	p.port = metadata.Port
	p.targetIP = metadata.TargetIP

	return nil
}

// getPort returns the port number as a string (for compatibility with API)
func (p *socatSSHProxy) getPort() string {
	return strconv.Itoa(p.port)
}
