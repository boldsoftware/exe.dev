package sshpool

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"mvdan.cc/sh/v3/syntax"
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
	mu          sync.Mutex
	baseDir     string
	bypassUntil map[string]time.Time
}

// shellQuoteCommand quotes multiple arguments and joins them with spaces.
// If args cannot be safely quoted, it returns an empty string.
func shellQuoteCommand(args []string) string {
	if len(args) == 0 {
		return ""
	}

	quoted := make([]string, len(args))
	for i, arg := range args {
		quotedArg, err := syntax.Quote(arg, syntax.LangBash)
		if err != nil {
			// Command contains a NUL byte or something else seriously sketchy.
			// Play it safe by returning "", and log for debugging purposes
			slog.Warn("failed to quote command argument", "args", args, "arg", arg, "error", err)
			return ""
		}
		quoted[i] = quotedArg
	}
	return strings.Join(quoted, " ")
}

// New creates a new SSH connection pool
func New() *Pool {
	// Create a unique temporary directory for control sockets
	baseDir, err := os.MkdirTemp("", "exe-ssh-control-")
	if err != nil {
		panic(fmt.Errorf("failed to create SSH control temp dir: %w", err))
	}

	pool := &Pool{
		connections: make(map[string]*Connection),
		baseDir:     baseDir,
		bypassUntil: make(map[string]time.Time),
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
				slog.Info("[SSH-POOL] Closing stale connection", "host", host)
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

	// If we've recently decided to bypass ControlMaster for this host, bail out early
	if p.shouldBypass(host) {
		return nil, fmt.Errorf("bypassing ControlMaster for %s", host)
	}

	p.mu.Lock()
	conn, exists := p.connections[host]
	p.mu.Unlock()

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

	// Ensure the base directory exists (in case it was deleted or we're in a new environment)
	if err := os.MkdirAll(p.baseDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create SSH control directory: %w", err)
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
		"-o", "ConnectTimeout=5",
		"-N", // No command, just establish connection
		host,
	)

	// Start the master connection in the background
	if err := masterCmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start SSH master connection to %s: %w", host, err)
	}

	// Wait for the control socket to be created, but respect the caller's context
	// so short-lived operations (e.g., 2s timeouts in tests) don't block here.
	socketCreated := false
	startWait := time.Now()
	// Default max wait is 5s, but shrink to the caller's deadline if sooner
	maxWait := 5 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if remain := time.Until(dl); remain > 0 && remain < maxWait {
			// Leave a small margin so the caller has time to run the actual command
			// after the connection is ready.
			const margin = 250 * time.Millisecond
			if remain > margin {
				maxWait = remain - margin
			} else {
				maxWait = remain
			}
		}
	}

WaitForSocket:
	for time.Since(startWait) < maxWait {
		// Allow early exit if the caller cancels
		select {
		case <-ctx.Done():
			break WaitForSocket
		default:
		}

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
		// Avoid repeatedly attempting ControlMaster on environments where it doesn't work
		p.bypassHostLocked(host)
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
	slog.Info("[SSH-POOL] Established new SSH connection", "host", host)

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
		slog.Info("[SSH-POOL] Connection appears dead", "host", conn.host, "error", err)
		return false
	}

	return strings.TrimSpace(string(output)) == "alive"
}

func (p *Pool) shouldBypass(host string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.bypassUntil[host]
	if ok {
		if time.Now().Before(until) {
			return true
		}
		delete(p.bypassUntil, host) // clean up expired entry
	}
	return false
}

func (p *Pool) bypassHost(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bypassHostLocked(host)
}

func (p *Pool) bypassHostLocked(host string) {
	p.bypassUntil[host] = time.Now().Add(30 * time.Minute)
}

// ExecCommand executes a command through a pooled SSH connection
func (p *Pool) ExecCommand(ctx context.Context, host string, args ...string) *exec.Cmd {
	// Ensure we have a bounded context to avoid very long DNS waits on invalid hosts.
	// If caller did not provide a deadline, use a sensible default.
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		// Best-effort cancel when command is created; the caller runs the command.
		// We cannot defer cancel here because the command may outlive this function.
		// Intentionally not deferring: the command will stop on context expiration.
		_ = cancel
	}
	// Normalize host and check bypass cache
	normHost := strings.TrimPrefix(host, "ssh://")
	shouldBypass := p.shouldBypass(normHost)
	if shouldBypass {
		return p.execDirectSSH(ctx, host, args...)
	}

	// Get or create connection
	conn, err := p.getConnection(ctx, host)
	if err != nil {
		slog.Info("[SSH-POOL] Failed to get connection", "host", host, "error", err)
		// Fall back to direct SSH and remember to bypass for a while
		p.bypassHost(normHost)
		return p.execDirectSSH(ctx, host, args...)
	}

	// Build the SSH arguments
	sshArgs := []string{
		"-o", fmt.Sprintf("ControlPath=%s", conn.controlPath),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=5",
		"-o", "LogLevel=ERROR",
		host,
	}

	// Properly quote and combine all command arguments into a single string
	// SSH expects the entire remote command as a single argument
	if len(args) > 0 {
		// Use proper shell quoting to handle spaces and special characters
		command := shellQuoteCommand(args)
		sshArgs = append(sshArgs, command)
	}

	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	return cmd
}

// execDirectSSH falls back to a direct SSH connection without pooling
func (p *Pool) execDirectSSH(ctx context.Context, host string, args ...string) *exec.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		_ = cancel
	}
	host = strings.TrimPrefix(host, "ssh://")

	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=5",
		"-o", "LogLevel=ERROR",
		host,
	}

	// Properly quote and combine all command arguments, same as ExecCommand
	if len(args) > 0 {
		command := shellQuoteCommand(args)
		sshArgs = append(sshArgs, command)
	}

	return exec.CommandContext(ctx, "ssh", sshArgs...)
}

// Close closes all connections in the pool
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for host, conn := range p.connections {
		slog.Info("[SSH-POOL] Closing connection", "host", host)
		conn.cancel()
		os.Remove(conn.controlPath)
	}

	p.connections = make(map[string]*Connection)
	os.RemoveAll(p.baseDir)
}

// SCP transfers files to remoteDest on host, preserving their permissions
// remoteDest is the destination directory, localPaths are the source files or directories
// If a localPath is a directory, it will be copied recursively
func (p *Pool) SCP(ctx context.Context, host string, remoteDest string, localPaths ...string) error {
	if len(localPaths) == 0 {
		return nil
	}

	// Parse SSH host if needed
	sshHost := strings.TrimPrefix(host, "ssh://")

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
