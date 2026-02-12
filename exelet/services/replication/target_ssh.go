package replication

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// countingWriter wraps a writer and tracks bytes written
type countingWriter struct {
	w          io.WriteCloser
	written    atomic.Int64
	total      int64
	onProgress ProgressFunc
}

func newCountingWriter(w io.WriteCloser, total int64, onProgress ProgressFunc) *countingWriter {
	return &countingWriter{w: w, total: total, onProgress: onProgress}
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	if n > 0 {
		written := cw.written.Add(int64(n))
		if cw.onProgress != nil {
			cw.onProgress(written, cw.total)
		}
	}
	return n, err
}

func (cw *countingWriter) Close() error {
	return cw.w.Close()
}

func (cw *countingWriter) BytesWritten() int64 {
	return cw.written.Load()
}

// knownHostsCallback returns an ssh.HostKeyCallback using the known_hosts file.
// If path is empty, it uses ~/.ssh/known_hosts.
func knownHostsCallback(path string) (ssh.HostKeyCallback, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		path = filepath.Join(home, ".ssh", "known_hosts")
	}
	callback, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read known_hosts %s: %w", path, err)
	}
	return callback, nil
}

// SSHTarget implements Target for ZFS-to-ZFS replication over SSH
type SSHTarget struct {
	config    *TargetConfig
	sshConfig *ssh.ClientConfig

	// Connection pool - reuse connections when possible
	mu     sync.Mutex
	client *ssh.Client
}

// NewSSHTarget creates a new SSH target
func NewSSHTarget(cfg *TargetConfig) (*SSHTarget, error) {
	authMethods, err := sshAuthMethods(cfg.SSHKeyPath)
	if err != nil {
		return nil, err
	}

	// Use known_hosts for host key verification
	hostKeyCallback, err := knownHostsCallback(cfg.KnownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to setup host key verification: %w", err)
	}

	sshConfig := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         30 * time.Second,
	}

	return &SSHTarget{
		config:    cfg,
		sshConfig: sshConfig,
	}, nil
}

// sshAuthMethods builds SSH auth methods. If keyPath is specified, uses that
// key. Otherwise, tries the SSH agent first, then falls back to default key
// paths (~/.ssh/id_ed25519, ~/.ssh/id_rsa).
func sshAuthMethods(keyPath string) ([]ssh.AuthMethod, error) {
	if keyPath != "" {
		signer, err := loadSSHKey(keyPath)
		if err != nil {
			return nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}

	var methods []ssh.AuthMethod

	// Try SSH agent
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	// Try default key paths
	home, err := os.UserHomeDir()
	if err == nil {
		for _, name := range []string{"id_ed25519", "id_rsa"} {
			p := filepath.Join(home, ".ssh", name)
			signer, err := loadSSHKey(p)
			if err == nil {
				methods = append(methods, ssh.PublicKeys(signer))
				break
			}
		}
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("no SSH authentication available: set --storage-replication-ssh-key, or ensure SSH agent or ~/.ssh/id_ed25519 is available")
	}
	return methods, nil
}

// loadSSHKey reads and parses an SSH private key from disk.
func loadSSHKey(path string) (ssh.Signer, error) {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to expand home directory: %w", err)
		}
		path = filepath.Join(home, path[1:])
	}
	keyData, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read SSH key %s: %w", path, err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH key %s: %w", path, err)
	}
	return signer, nil
}

func (t *SSHTarget) Type() string {
	return "ssh"
}

// getClient returns an SSH client, reusing existing connection if available
func (t *SSHTarget) getClient() (*ssh.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Check if existing client is still valid
	if t.client != nil {
		// Test the connection with a simple request
		_, _, err := t.client.SendRequest("keepalive@openssh.com", true, nil)
		if err == nil {
			return t.client, nil
		}
		// Connection is dead, close it
		t.client.Close()
		t.client = nil
	}

	// Establish new connection
	addr := t.config.Host
	if t.config.Port != "" {
		addr = net.JoinHostPort(t.config.Host, t.config.Port)
	} else {
		addr = net.JoinHostPort(t.config.Host, "22")
	}

	client, err := ssh.Dial("tcp", addr, t.sshConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", addr, err)
	}

	t.client = client
	return client, nil
}

// runCommand executes a command on the remote host and returns output
func (t *SSHTarget) runCommand(ctx context.Context, command string) ([]byte, error) {
	client, err := t.getClient()
	if err != nil {
		return nil, err
	}

	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	// Handle context cancellation
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			session.Signal(ssh.SIGTERM)
			session.Close()
		case <-done:
		}
	}()
	defer close(done)

	output, err := session.CombinedOutput(command)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return output, err
}

// remoteDataset returns the remote dataset path for a volume
func (t *SSHTarget) remoteDataset(volumeID string) string {
	return fmt.Sprintf("%s/%s", t.config.Pool, volumeID)
}

// ListSnapshots returns existing snapshots for a volume on the remote
func (t *SSHTarget) ListSnapshots(ctx context.Context, volumeID string) ([]string, error) {
	dataset := t.remoteDataset(volumeID)
	command := fmt.Sprintf("zfs list -t snapshot -H -o name -s creation %s", dataset)

	output, err := t.runCommand(ctx, command)
	if err != nil {
		// If the dataset doesn't exist, return empty list
		outputStr := string(output)
		if strings.Contains(outputStr, "dataset does not exist") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list snapshots: %s", outputStr)
	}

	var snapshots []string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Extract snapshot name from full path (pool/volume@snapshot -> snapshot)
		parts := strings.SplitN(line, "@", 2)
		if len(parts) == 2 {
			snapshots = append(snapshots, parts[1])
		}
	}

	return snapshots, nil
}

// ListSnapshotsWithMetadata returns snapshots with full metadata for a volume
func (t *SSHTarget) ListSnapshotsWithMetadata(ctx context.Context, volumeID string) ([]SnapshotMetadata, error) {
	dataset := t.remoteDataset(volumeID)
	// -p for parseable (machine-readable) output with timestamps in seconds
	command := fmt.Sprintf("zfs list -t snapshot -H -p -o name,creation,referenced -s creation %s", dataset)

	output, err := t.runCommand(ctx, command)
	if err != nil {
		outputStr := string(output)
		if strings.Contains(outputStr, "dataset does not exist") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list snapshots: %s", outputStr)
	}

	var snapshots []SnapshotMetadata
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		// Extract snapshot name from full path (pool/volume@snapshot -> snapshot)
		fullName := fields[0]
		parts := strings.SplitN(fullName, "@", 2)
		if len(parts) != 2 {
			continue
		}

		var createdAt, sizeBytes int64
		fmt.Sscanf(fields[1], "%d", &createdAt)
		fmt.Sscanf(fields[2], "%d", &sizeBytes)

		snapshots = append(snapshots, SnapshotMetadata{
			Name:      parts[1],
			CreatedAt: createdAt,
			SizeBytes: sizeBytes,
		})
	}

	return snapshots, nil
}

// estimateSendSize estimates the size of a zfs send
// Uses -n (dry run) with -v (verbose) to get the estimated size
func estimateSendSize(ctx context.Context, dataset, snapshotName, baseSnapshot string) int64 {
	snapshot := fmt.Sprintf("%s@%s", dataset, snapshotName)
	var args []string
	if baseSnapshot != "" {
		base := fmt.Sprintf("%s@%s", dataset, baseSnapshot)
		args = []string{"send", "-n", "-v", "-i", base, snapshot}
	} else {
		args = []string{"send", "-n", "-v", snapshot}
	}

	cmd := exec.CommandContext(ctx, "zfs", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0
	}

	// Parse output - zfs send -n -v outputs like:
	// "send from @base to pool/dataset@snap estimated size is 1.23G"
	// or "full send of pool/dataset@snap estimated size is 1.23G"
	// Also may include "total estimated size is 1.23G"
	outputStr := string(output)

	// Look for "estimated size is X" pattern
	if _, sizeStr, ok := strings.Cut(outputStr, "estimated size is "); ok {
		// Take until end of line or whitespace
		if newline := strings.IndexAny(sizeStr, "\n\r"); newline >= 0 {
			sizeStr = sizeStr[:newline]
		}
		sizeStr = strings.TrimSpace(sizeStr)
		return parseHumanSize(sizeStr)
	}

	return 0
}

// parseHumanSize parses sizes like "1.23G", "500M", "1024K", "12345"
func parseHumanSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	multiplier := int64(1)
	lastChar := s[len(s)-1]
	switch lastChar {
	case 'K', 'k':
		multiplier = 1024
		s = s[:len(s)-1]
	case 'M', 'm':
		multiplier = 1024 * 1024
		s = s[:len(s)-1]
	case 'G', 'g':
		multiplier = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	case 'T', 't':
		multiplier = 1024 * 1024 * 1024 * 1024
		s = s[:len(s)-1]
	}

	var value float64
	fmt.Sscanf(s, "%f", &value)
	return int64(value * float64(multiplier))
}

// Send transfers a snapshot to the remote target
func (t *SSHTarget) Send(ctx context.Context, opts SendOptions) error {
	// Estimate send size for progress tracking
	estimatedSize := estimateSendSize(ctx, opts.Dataset, opts.SnapshotName, opts.BaseSnapshot)

	client, err := t.getClient()
	if err != nil {
		return err
	}

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	// Build remote zfs recv command
	remoteDataset := t.remoteDataset(opts.VolumeID)
	recvCommand := fmt.Sprintf("zfs recv -F %s", remoteDataset)

	// Get stdin pipe for the remote command
	remoteStdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get remote stdin: %w", err)
	}

	// Wrap with counting writer for progress tracking
	var output io.WriteCloser = remoteStdin
	var counter *countingWriter
	if opts.OnProgress != nil {
		counter = newCountingWriter(remoteStdin, estimatedSize, opts.OnProgress)
		output = counter
	}

	// Capture stderr for error messages
	var stderrBuf strings.Builder
	session.Stderr = &stderrBuf

	// Start the remote recv command
	if err := session.Start(recvCommand); err != nil {
		return fmt.Errorf("failed to start remote zfs recv: %w", err)
	}

	// Build local zfs send command
	var sendArgs []string
	if opts.BaseSnapshot != "" {
		// Incremental send
		sendArgs = []string{"send", "-i", fmt.Sprintf("%s@%s", opts.Dataset, opts.BaseSnapshot), fmt.Sprintf("%s@%s", opts.Dataset, opts.SnapshotName)}
	} else {
		// Full send
		sendArgs = []string{"send", fmt.Sprintf("%s@%s", opts.Dataset, opts.SnapshotName)}
	}

	sendCmd := exec.CommandContext(ctx, "zfs", sendArgs...)

	// Handle bandwidth limiting with pv if specified
	var pvCmd *exec.Cmd
	bandwidthLimit := opts.BandwidthLimit
	if bandwidthLimit == "" {
		bandwidthLimit = t.config.BandwidthLimit
	}

	if bandwidthLimit != "" {
		// Pipeline: zfs send | pv -L rate | counting writer | remote stdin
		pvCmd = exec.CommandContext(ctx, "pv", "-q", "-L", bandwidthLimit)
		pvStdin, err := sendCmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("failed to create send->pv pipe: %w", err)
		}
		pvCmd.Stdin = pvStdin
		pvCmd.Stdout = output

		if err := sendCmd.Start(); err != nil {
			return fmt.Errorf("failed to start zfs send: %w", err)
		}
		if err := pvCmd.Start(); err != nil {
			sendCmd.Process.Kill()
			return fmt.Errorf("failed to start pv: %w", err)
		}

		// Wait for send and pv to complete
		sendErr := sendCmd.Wait()
		pvErr := pvCmd.Wait()
		output.Close()

		// Wait for remote to complete
		recvErr := session.Wait()

		if recvErr != nil {
			return fmt.Errorf("remote zfs recv failed: %w (stderr: %s)", recvErr, stderrBuf.String())
		}
		if pvErr != nil {
			return fmt.Errorf("pv failed: %w", pvErr)
		}
		if sendErr != nil {
			return fmt.Errorf("zfs send failed: %w", sendErr)
		}
	} else {
		// Simple pipeline: zfs send | counting writer | remote stdin
		sendCmd.Stdout = output
		if err := sendCmd.Start(); err != nil {
			return fmt.Errorf("failed to start zfs send: %w", err)
		}

		sendErr := sendCmd.Wait()
		output.Close()

		// Wait for remote to complete
		recvErr := session.Wait()

		if recvErr != nil {
			return fmt.Errorf("remote zfs recv failed: %w (stderr: %s)", recvErr, stderrBuf.String())
		}
		if sendErr != nil {
			return fmt.Errorf("zfs send failed: %w", sendErr)
		}
	}

	return nil
}

// Receive restores a snapshot from the remote target
func (t *SSHTarget) Receive(ctx context.Context, opts ReceiveOptions) error {
	client, err := t.getClient()
	if err != nil {
		return err
	}

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	// Build remote zfs send command
	remoteDataset := t.remoteDataset(opts.VolumeID)
	snapshotPath := fmt.Sprintf("%s@%s", remoteDataset, opts.SnapshotName)
	sendCommand := fmt.Sprintf("zfs send %s", snapshotPath)

	// Get stdout pipe for the remote command
	remoteStdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get remote stdout: %w", err)
	}

	// Capture remote stderr
	var remoteStderr strings.Builder
	session.Stderr = &remoteStderr

	// Start the remote send command
	if err := session.Start(sendCommand); err != nil {
		return fmt.Errorf("failed to start remote zfs send: %w", err)
	}

	// Build local zfs recv command
	recvArgs := []string{"recv"}
	if opts.Force {
		recvArgs = append(recvArgs, "-F")
	}
	recvArgs = append(recvArgs, opts.Dataset)

	recvCmd := exec.CommandContext(ctx, "zfs", recvArgs...)
	recvCmd.Stdin = remoteStdout

	// Capture local stderr
	var localStderr strings.Builder
	recvCmd.Stderr = &localStderr

	if err := recvCmd.Start(); err != nil {
		session.Close()
		return fmt.Errorf("failed to start local zfs recv: %w", err)
	}

	// Run both concurrently, with proper error handling
	type result struct {
		source string
		err    error
	}
	results := make(chan result, 2)

	go func() {
		err := session.Wait()
		if err != nil {
			// Remote failed - kill local to unblock it
			recvCmd.Process.Kill()
		}
		results <- result{"remote", err}
	}()

	go func() {
		err := recvCmd.Wait()
		if err != nil {
			// Local failed - close session to unblock remote
			session.Close()
		}
		results <- result{"local", err}
	}()

	// Collect both results
	var sendErr, recvErr error
	for range 2 {
		r := <-results
		if r.source == "remote" {
			sendErr = r.err
		} else {
			recvErr = r.err
		}
	}

	// Determine which error to report
	// SIGPIPE (exit code 141) on remote means local closed first - report local error
	localErrMsg := strings.TrimSpace(localStderr.String())
	remoteErrMsg := strings.TrimSpace(remoteStderr.String())

	// If local has an error message, that's likely the root cause
	if recvErr != nil && localErrMsg != "" {
		return fmt.Errorf("local zfs recv failed: %s", localErrMsg)
	}

	// If remote got SIGPIPE and local also failed, report local error
	if sendErr != nil && recvErr != nil && strings.Contains(sendErr.Error(), "141") {
		if localErrMsg != "" {
			return fmt.Errorf("local zfs recv failed: %s", localErrMsg)
		}
		return fmt.Errorf("local zfs recv failed: %w", recvErr)
	}

	// Otherwise check send error first
	if sendErr != nil {
		if remoteErrMsg != "" {
			return fmt.Errorf("remote zfs send failed (%s): %s", sendCommand, remoteErrMsg)
		}
		return fmt.Errorf("remote zfs send failed (%s): %w", sendCommand, sendErr)
	}
	if recvErr != nil {
		if localErrMsg != "" {
			return fmt.Errorf("local zfs recv failed: %s", localErrMsg)
		}
		return fmt.Errorf("local zfs recv failed: %w", recvErr)
	}

	return nil
}

// Delete removes a snapshot from the remote target
func (t *SSHTarget) Delete(ctx context.Context, volumeID, snapshotName string) error {
	remoteDataset := t.remoteDataset(volumeID)
	snapshotPath := fmt.Sprintf("%s@%s", remoteDataset, snapshotName)
	command := fmt.Sprintf("zfs destroy %s", snapshotPath)

	output, err := t.runCommand(ctx, command)
	if err != nil {
		return fmt.Errorf("failed to destroy snapshot %s: %s", snapshotPath, string(output))
	}

	return nil
}

// ListVolumes returns all volume IDs (child datasets) present on the remote
func (t *SSHTarget) ListVolumes(ctx context.Context) ([]string, error) {
	command := fmt.Sprintf("zfs list -H -o name -r -d 1 %s", t.config.Pool)

	output, err := t.runCommand(ctx, command)
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes: %s", string(output))
	}

	var volumes []string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == t.config.Pool {
			continue
		}
		// Extract volume ID from full path (pool/volume -> volume)
		volumeID := strings.TrimPrefix(line, t.config.Pool+"/")
		if volumeID != "" {
			volumes = append(volumes, volumeID)
		}
	}

	sort.Strings(volumes)
	return volumes, nil
}

// DeleteVolume removes an entire volume (dataset) and all its snapshots from the remote
func (t *SSHTarget) DeleteVolume(ctx context.Context, volumeID string) error {
	remoteDataset := t.remoteDataset(volumeID)
	command := fmt.Sprintf("zfs destroy -r %s", remoteDataset)

	output, err := t.runCommand(ctx, command)
	if err != nil {
		return fmt.Errorf("failed to destroy dataset %s: %s", remoteDataset, string(output))
	}

	return nil
}

// ListAllReplicationSnapshots lists all replication snapshots across all volumes on the remote
// Returns snapshots with their volume IDs extracted from the dataset path
func (t *SSHTarget) ListAllReplicationSnapshots(ctx context.Context) ([]VolumeSnapshot, error) {
	// List all snapshots recursively under the pool with metadata
	// -r for recursive, -p for parseable output
	command := fmt.Sprintf("zfs list -t snapshot -H -p -o name,creation,referenced -r %s", t.config.Pool)

	output, err := t.runCommand(ctx, command)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %s", string(output))
	}

	var snapshots []VolumeSnapshot
	poolPrefix := t.config.Pool + "/"

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		// Parse full snapshot path: pool/volumeID@snapshotName
		fullName := fields[0]
		atIdx := strings.LastIndex(fullName, "@")
		if atIdx < 0 {
			continue
		}

		datasetPath := fullName[:atIdx]
		snapshotName := fullName[atIdx+1:]

		// Only include replication snapshots
		if !strings.HasPrefix(snapshotName, SnapshotPrefix) {
			continue
		}

		// Extract volume ID from dataset path (remove pool prefix)
		volumeID := strings.TrimPrefix(datasetPath, poolPrefix)
		if volumeID == "" || volumeID == t.config.Pool {
			continue
		}

		var createdAt, sizeBytes int64
		fmt.Sscanf(fields[1], "%d", &createdAt)
		fmt.Sscanf(fields[2], "%d", &sizeBytes)

		snapshots = append(snapshots, VolumeSnapshot{
			VolumeID: volumeID,
			Snapshot: SnapshotMetadata{
				Name:      snapshotName,
				CreatedAt: createdAt,
				SizeBytes: sizeBytes,
			},
		})
	}

	return snapshots, nil
}

func (t *SSHTarget) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.client != nil {
		err := t.client.Close()
		t.client = nil
		return err
	}
	return nil
}
