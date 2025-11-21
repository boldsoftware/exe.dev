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

// SSHProxy represents a persistent SSH proxy using socat
type SSHProxy struct {
	InstanceID  string
	Port        int
	TargetIP    string
	TargetPort  int
	PID         int
	InstanceDir string
	log         *slog.Logger
}

// proxyMetadata is the JSON structure persisted to disk
type proxyMetadata struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	TargetIP  string    `json:"target_ip"`
	StartedAt time.Time `json:"started_at"`
}

// NewSSHProxy creates a new SSH proxy instance
func NewSSHProxy(instanceID string, port int, targetIP string, instanceDir string, log *slog.Logger) *SSHProxy {
	return &SSHProxy{
		InstanceID:  instanceID,
		Port:        port,
		TargetIP:    targetIP,
		TargetPort:  22, // Always SSH port
		InstanceDir: instanceDir,
		log:         log,
	}
}

// Start spawns a detached socat process for SSH forwarding
func (p *SSHProxy) Start() error {
	// Check if socat is available
	if _, err := exec.LookPath("socat"); err != nil {
		return fmt.Errorf("socat not found in PATH: %w", err)
	}

	// Build socat command
	listenAddr := fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", p.Port)
	targetAddr := fmt.Sprintf("TCP:%s:%d", p.TargetIP, p.TargetPort)

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
	metadataPath := filepath.Join(p.InstanceDir, "sshproxy.json")
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
	if err != nil {
		return false
	}

	return true
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

	metadataPath := filepath.Join(p.InstanceDir, "sshproxy.json")
	if err := os.WriteFile(metadataPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	return nil
}

// LoadFromDisk loads proxy metadata from instance directory
func (p *SSHProxy) LoadFromDisk() error {
	metadataPath := filepath.Join(p.InstanceDir, "sshproxy.json")

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
