package sshpool

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Connection represents a persistent SSH connection with multiplexing
type Connection struct {
	host        string
	controlPath string
	mu          sync.Mutex
	lastUsed    time.Time
	ctx         context.Context
	cancel      context.CancelFunc
}

// Pool manages persistent SSH connections
type Pool struct {
	connections map[string]*Connection
	mu          sync.RWMutex
	baseDir     string
}

// New creates a new SSH connection pool
func New() *Pool {
	// Create a temporary directory for control sockets
	baseDir := "/tmp/exe-ssh-control"
	os.MkdirAll(baseDir, 0o700)

	pool := &Pool{
		connections: make(map[string]*Connection),
		baseDir:     baseDir,
	}

	// Start a goroutine to periodically clean up stale connections
	go pool.cleanupStaleConnections()

	return pool
}

// cleanupStaleConnections periodically removes connections that haven't been used
func (p *Pool) cleanupStaleConnections() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		p.mu.Lock()
		now := time.Now()
		for host, conn := range p.connections {
			conn.mu.Lock()
			if now.Sub(conn.lastUsed) > 10*time.Minute {
				log.Printf("[SSH-POOL] Closing stale connection to %s", host)
				conn.cancel()
				os.Remove(conn.controlPath)
				delete(p.connections, host)
			}
			conn.mu.Unlock()
		}
		p.mu.Unlock()
	}
}

// getConnection returns an existing connection or creates a new one
func (p *Pool) getConnection(ctx context.Context, host string) (*Connection, error) {
	// Normalize the host string
	host = strings.TrimPrefix(host, "ssh://")

	p.mu.RLock()
	conn, exists := p.connections[host]
	p.mu.RUnlock()

	if exists {
		// Check if the connection is still alive
		if p.isConnectionAlive(conn) {
			conn.mu.Lock()
			conn.lastUsed = time.Now()
			conn.mu.Unlock()
			return conn, nil
		}

		// Connection is dead, remove it
		p.mu.Lock()
		delete(p.connections, host)
		p.mu.Unlock()
		os.Remove(conn.controlPath)
	}

	// Create a new connection
	return p.createConnection(ctx, host)
}

// createConnection establishes a new SSH master connection
func (p *Pool) createConnection(ctx context.Context, host string) (*Connection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check again if someone else created it while we were waiting for the lock
	if conn, exists := p.connections[host]; exists {
		if p.isConnectionAlive(conn) {
			return conn, nil
		}
	}

	// Create control socket path
	controlPath := fmt.Sprintf("%s/%s.sock", p.baseDir, strings.ReplaceAll(host, ":", "_"))

	// Create a cancellable context for this connection
	connCtx, cancel := context.WithCancel(context.Background())

	// Start SSH master connection with ControlMaster
	masterCmd := exec.CommandContext(connCtx, "ssh",
		"-o", "ControlMaster=yes",
		"-o", fmt.Sprintf("ControlPath=%s", controlPath),
		"-o", "ControlPersist=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-o", "ConnectTimeout=10",
		"-N", // No command, just establish connection
		host,
	)

	// Start the master connection in the background
	if err := masterCmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start SSH master connection to %s: %w", host, err)
	}

	// Wait a moment for the control socket to be created
	socketCreated := false
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(controlPath); err == nil {
			socketCreated = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !socketCreated {
		// The connection might have failed, check if the process is still running
		if masterCmd.ProcessState != nil && masterCmd.ProcessState.Exited() {
			cancel()
			return nil, fmt.Errorf("SSH master connection to %s failed to start", host)
		}
	}

	// Verify the control socket exists
	if _, err := os.Stat(controlPath); err != nil {
		cancel()
		masterCmd.Process.Kill()
		return nil, fmt.Errorf("SSH control socket not created for %s: %w", host, err)
	}

	conn := &Connection{
		host:        host,
		controlPath: controlPath,
		lastUsed:    time.Now(),
		ctx:         connCtx,
		cancel:      cancel,
	}

	p.connections[host] = conn
	log.Printf("[SSH-POOL] Established new SSH connection to %s", host)

	return conn, nil
}

// isConnectionAlive checks if an SSH connection is still working
func (p *Pool) isConnectionAlive(conn *Connection) bool {
	// Check if control socket exists
	if _, err := os.Stat(conn.controlPath); err != nil {
		return false
	}

	// Try to run a simple command through the connection
	checkCmd := exec.Command("ssh",
		"-o", fmt.Sprintf("ControlPath=%s", conn.controlPath),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		conn.host,
		"echo", "alive",
	)

	output, err := checkCmd.CombinedOutput()
	if err != nil {
		log.Printf("[SSH-POOL] Connection to %s appears dead: %v", conn.host, err)
		return false
	}

	return strings.TrimSpace(string(output)) == "alive"
}

// ExecCommand executes a command through a pooled SSH connection
func (p *Pool) ExecCommand(ctx context.Context, host string, args ...string) *exec.Cmd {
	// Get or create connection
	conn, err := p.getConnection(ctx, host)
	if err != nil {
		log.Printf("[SSH-POOL] Failed to get connection to %s: %v", host, err)
		// Fall back to direct SSH
		return p.execDirectSSH(ctx, host, args...)
	}

	// Build the command
	sshArgs := []string{
		"-o", fmt.Sprintf("ControlPath=%s", conn.controlPath),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		host,
	}
	sshArgs = append(sshArgs, args...)

	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	return cmd
}

// execDirectSSH falls back to a direct SSH connection without pooling
func (p *Pool) execDirectSSH(ctx context.Context, host string, args ...string) *exec.Cmd {
	host = strings.TrimPrefix(host, "ssh://")

	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		host,
	}
	sshArgs = append(sshArgs, args...)

	return exec.CommandContext(ctx, "ssh", sshArgs...)
}

// Close closes all connections in the pool
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for host, conn := range p.connections {
		log.Printf("[SSH-POOL] Closing connection to %s", host)
		conn.cancel()
		os.Remove(conn.controlPath)
	}

	p.connections = make(map[string]*Connection)
	os.RemoveAll(p.baseDir)
}

// StreamCommand executes a command with streaming output through a pooled connection
func (p *Pool) StreamCommand(ctx context.Context, host string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	cmd := p.ExecCommand(ctx, host, args...)

	if stdin != nil {
		cmd.Stdin = stdin
	}
	if stdout != nil {
		cmd.Stdout = stdout
	}
	if stderr != nil {
		cmd.Stderr = stderr
	}

	return cmd.Run()
}

// CheckHostConnection verifies that we can connect to a host
func (p *Pool) CheckHostConnection(ctx context.Context, host string) error {
	_, err := p.getConnection(ctx, host)
	if err != nil {
		return err
	}

	// Run a simple test command
	testCmd := p.ExecCommand(ctx, host, "echo", "test")
	output, err := testCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to execute test command on %s: %w", host, err)
	}

	if strings.TrimSpace(string(output)) != "test" {
		return fmt.Errorf("unexpected output from test command on %s: %s", host, output)
	}

	return nil
}

// SCP transfers files to remoteDest on host, preserving their permissions
// remoteDest is the destination directory, localPaths are the source files or directories
// If a localPath is a directory, it will be copied recursively
func (p *Pool) SCP(ctx context.Context, host string, remoteDest string, localPaths ...string) error {
	if len(localPaths) == 0 {
		return nil
	}

	// Parse SSH host if needed
	sshHost := host
	if strings.HasPrefix(sshHost, "ssh://") {
		sshHost = strings.TrimPrefix(sshHost, "ssh://")
	}

	// Get or create the connection to ensure control socket exists
	conn, err := p.getConnection(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to get SSH connection: %w", err)
	}

	// Update last used time
	conn.mu.Lock()
	conn.lastUsed = time.Now()
	controlPath := conn.controlPath
	conn.mu.Unlock()

	// Build scp command with all files at once
	// scp preserves permissions by default with -p flag
	scpArgs := []string{
		"-rp", // Recursive and preserve modification times, access times, and modes
		"-o", "ControlPath=" + controlPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
	}
	scpArgs = append(scpArgs, localPaths...)
	scpArgs = append(scpArgs, fmt.Sprintf("%s:%s", sshHost, remoteDest))

	scpCmd := exec.CommandContext(ctx, "scp", scpArgs...)
	if output, err := scpCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to scp files to %s: %w: %s", remoteDest, err, output)
	}

	return nil
}
