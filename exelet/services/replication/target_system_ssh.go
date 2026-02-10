package replication

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// SystemSSHTarget implements Target using the system SSH binary.
// This allows Tailscale SSH, ProxyCommand, and other system SSH
// configuration to work transparently.
type SystemSSHTarget struct {
	config *TargetConfig
}

// NewSystemSSHTarget creates a new SystemSSHTarget.
func NewSystemSSHTarget(cfg *TargetConfig) *SystemSSHTarget {
	return &SystemSSHTarget{config: cfg}
}

func (t *SystemSSHTarget) Type() string {
	return "ssh"
}

// sshArgs returns the base ssh command arguments for connecting to the target.
func (t *SystemSSHTarget) sshArgs() []string {
	args := []string{
		"-T",
		"-o", "BatchMode=yes",
	}
	if t.config.Port != "" {
		args = append(args, "-p", t.config.Port)
	}
	if t.config.SSHKeyPath != "" {
		args = append(args, "-i", t.config.SSHKeyPath)
	}
	if t.config.KnownHostsPath != "" {
		args = append(args, "-o", "UserKnownHostsFile="+t.config.KnownHostsPath)
	} else {
		args = append(args, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null")
	}
	args = append(args, fmt.Sprintf("%s@%s", t.config.User, t.config.Host))
	return args
}

// runCommand executes a command on the remote host via the system ssh binary.
func (t *SystemSSHTarget) runCommand(ctx context.Context, command string) ([]byte, error) {
	args := append(t.sshArgs(), command)
	cmd := exec.CommandContext(ctx, t.config.SSHCommand, args...)
	return cmd.CombinedOutput()
}

func (t *SystemSSHTarget) remoteDataset(volumeID string) string {
	return fmt.Sprintf("%s/%s", t.config.Pool, volumeID)
}

func (t *SystemSSHTarget) ListSnapshots(ctx context.Context, volumeID string) ([]string, error) {
	dataset := t.remoteDataset(volumeID)
	command := fmt.Sprintf("zfs list -t snapshot -H -o name -s creation %s", dataset)

	output, err := t.runCommand(ctx, command)
	if err != nil {
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
		parts := strings.SplitN(line, "@", 2)
		if len(parts) == 2 {
			snapshots = append(snapshots, parts[1])
		}
	}
	return snapshots, nil
}

func (t *SystemSSHTarget) ListSnapshotsWithMetadata(ctx context.Context, volumeID string) ([]SnapshotMetadata, error) {
	dataset := t.remoteDataset(volumeID)
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
		parts := strings.SplitN(fields[0], "@", 2)
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

func (t *SystemSSHTarget) Send(ctx context.Context, opts SendOptions) error {
	estimatedSize := estimateSendSize(ctx, opts.Dataset, opts.SnapshotName, opts.BaseSnapshot)

	// Build local zfs send command
	var sendArgs []string
	if opts.BaseSnapshot != "" {
		sendArgs = []string{"send", "-i", fmt.Sprintf("%s@%s", opts.Dataset, opts.BaseSnapshot), fmt.Sprintf("%s@%s", opts.Dataset, opts.SnapshotName)}
	} else {
		sendArgs = []string{"send", fmt.Sprintf("%s@%s", opts.Dataset, opts.SnapshotName)}
	}
	sendCmd := exec.CommandContext(ctx, "zfs", sendArgs...)

	// Build remote ssh zfs recv command
	remoteDataset := t.remoteDataset(opts.VolumeID)
	recvCommand := fmt.Sprintf("zfs recv -F %s", remoteDataset)
	sshArgs := append(t.sshArgs(), recvCommand)
	sshCmd := exec.CommandContext(ctx, t.config.SSHCommand, sshArgs...)

	bandwidthLimit := opts.BandwidthLimit
	if bandwidthLimit == "" {
		bandwidthLimit = t.config.BandwidthLimit
	}

	var stderrBuf strings.Builder
	sshCmd.Stderr = &stderrBuf

	var sendStderrBuf strings.Builder
	sendCmd.Stderr = &sendStderrBuf

	sendStdout, err := sendCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create send stdout pipe: %w", err)
	}

	// copyDone signals when the background copy goroutine has finished
	// draining the pipe. We must wait for this before calling cmd.Wait()
	// because Wait closes the stdout pipe — if the goroutine hasn't
	// finished reading, data is lost and zfs recv gets a truncated stream.
	copyDone := make(chan struct{})
	copyUsed := false

	if bandwidthLimit != "" {
		// Pipeline: zfs send | pv -L rate | ssh zfs recv
		pvCmd := exec.CommandContext(ctx, "pv", "-q", "-L", bandwidthLimit)
		pvCmd.Stdin = sendStdout

		pvStdout, err := pvCmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("failed to create pv stdout pipe: %w", err)
		}

		if opts.OnProgress != nil && estimatedSize > 0 {
			copyUsed = true
			sshStdin, err := sshCmd.StdinPipe()
			if err != nil {
				return fmt.Errorf("failed to create ssh stdin pipe: %w", err)
			}
			counter := newCountingWriter(sshStdin, estimatedSize, opts.OnProgress)
			go func() {
				defer close(copyDone)
				buf := make([]byte, 256*1024)
				for {
					n, readErr := pvStdout.Read(buf)
					if n > 0 {
						if _, writeErr := counter.Write(buf[:n]); writeErr != nil {
							break
						}
					}
					if readErr != nil {
						break
					}
				}
				counter.Close()
			}()
		} else {
			sshCmd.Stdin = pvStdout
		}

		if err := sendCmd.Start(); err != nil {
			return fmt.Errorf("failed to start zfs send: %w", err)
		}
		if err := pvCmd.Start(); err != nil {
			sendCmd.Process.Kill()
			return fmt.Errorf("failed to start pv: %w", err)
		}
		if err := sshCmd.Start(); err != nil {
			sendCmd.Process.Kill()
			pvCmd.Process.Kill()
			return fmt.Errorf("failed to start ssh recv: %w", err)
		}

		// Wait for copy goroutine to drain the pipe before Wait() closes it
		if copyUsed {
			<-copyDone
		}
		sendErr := sendCmd.Wait()
		pvErr := pvCmd.Wait()
		sshErr := sshCmd.Wait()

		if sshErr != nil {
			return fmt.Errorf("remote zfs recv failed: %w (recv stderr: %s) (send stderr: %s)", sshErr, stderrBuf.String(), sendStderrBuf.String())
		}
		if pvErr != nil {
			return fmt.Errorf("pv failed: %w", pvErr)
		}
		if sendErr != nil {
			return fmt.Errorf("zfs send failed: %w (stderr: %s)", sendErr, sendStderrBuf.String())
		}
	} else {
		// Simple pipeline: zfs send | ssh zfs recv
		if opts.OnProgress != nil && estimatedSize > 0 {
			copyUsed = true
			sshStdin, err := sshCmd.StdinPipe()
			if err != nil {
				return fmt.Errorf("failed to create ssh stdin pipe: %w", err)
			}
			counter := newCountingWriter(sshStdin, estimatedSize, opts.OnProgress)
			go func() {
				defer close(copyDone)
				buf := make([]byte, 256*1024)
				for {
					n, readErr := sendStdout.Read(buf)
					if n > 0 {
						if _, writeErr := counter.Write(buf[:n]); writeErr != nil {
							break
						}
					}
					if readErr != nil {
						break
					}
				}
				counter.Close()
			}()
		} else {
			sshCmd.Stdin = sendStdout
		}

		if err := sendCmd.Start(); err != nil {
			return fmt.Errorf("failed to start zfs send: %w", err)
		}
		if err := sshCmd.Start(); err != nil {
			sendCmd.Process.Kill()
			return fmt.Errorf("failed to start ssh recv: %w", err)
		}

		// Wait for copy goroutine to drain the pipe before Wait() closes it
		if copyUsed {
			<-copyDone
		}
		sendErr := sendCmd.Wait()
		sshErr := sshCmd.Wait()

		if sshErr != nil {
			return fmt.Errorf("remote zfs recv failed: %w (recv stderr: %s) (send stderr: %s)", sshErr, stderrBuf.String(), sendStderrBuf.String())
		}
		if sendErr != nil {
			return fmt.Errorf("zfs send failed: %w (stderr: %s)", sendErr, sendStderrBuf.String())
		}
	}

	return nil
}

func (t *SystemSSHTarget) Receive(ctx context.Context, opts ReceiveOptions) error {
	// Build remote zfs send command
	remoteDataset := t.remoteDataset(opts.VolumeID)
	snapshotPath := fmt.Sprintf("%s@%s", remoteDataset, opts.SnapshotName)
	sendCommand := fmt.Sprintf("zfs send %s", snapshotPath)

	sshArgs := append(t.sshArgs(), sendCommand)
	sshCmd := exec.CommandContext(ctx, t.config.SSHCommand, sshArgs...)

	// Build local zfs recv command
	recvArgs := []string{"recv"}
	if opts.Force {
		recvArgs = append(recvArgs, "-F")
	}
	recvArgs = append(recvArgs, opts.Dataset)
	recvCmd := exec.CommandContext(ctx, "zfs", recvArgs...)

	// Pipeline: ssh zfs send | zfs recv
	sshStdout, err := sshCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create ssh stdout pipe: %w", err)
	}
	recvCmd.Stdin = sshStdout

	var sshStderr, recvStderr strings.Builder
	sshCmd.Stderr = &sshStderr
	recvCmd.Stderr = &recvStderr

	if err := sshCmd.Start(); err != nil {
		return fmt.Errorf("failed to start ssh send: %w", err)
	}
	if err := recvCmd.Start(); err != nil {
		sshCmd.Process.Kill()
		return fmt.Errorf("failed to start local zfs recv: %w", err)
	}

	sshErr := sshCmd.Wait()
	recvErr := recvCmd.Wait()

	recvErrMsg := strings.TrimSpace(recvStderr.String())
	sshErrMsg := strings.TrimSpace(sshStderr.String())

	if recvErr != nil && recvErrMsg != "" {
		return fmt.Errorf("local zfs recv failed: %s", recvErrMsg)
	}
	if sshErr != nil && recvErr != nil && strings.Contains(sshErr.Error(), "141") {
		if recvErrMsg != "" {
			return fmt.Errorf("local zfs recv failed: %s", recvErrMsg)
		}
		return fmt.Errorf("local zfs recv failed: %w", recvErr)
	}
	if sshErr != nil {
		if sshErrMsg != "" {
			return fmt.Errorf("remote zfs send failed (%s): %s", sendCommand, sshErrMsg)
		}
		return fmt.Errorf("remote zfs send failed (%s): %w", sendCommand, sshErr)
	}
	if recvErr != nil {
		if recvErrMsg != "" {
			return fmt.Errorf("local zfs recv failed: %s", recvErrMsg)
		}
		return fmt.Errorf("local zfs recv failed: %w", recvErr)
	}

	return nil
}

func (t *SystemSSHTarget) Delete(ctx context.Context, volumeID, snapshotName string) error {
	remoteDataset := t.remoteDataset(volumeID)
	snapshotPath := fmt.Sprintf("%s@%s", remoteDataset, snapshotName)
	command := fmt.Sprintf("zfs destroy %s", snapshotPath)

	output, err := t.runCommand(ctx, command)
	if err != nil {
		return fmt.Errorf("failed to destroy snapshot %s: %s", snapshotPath, string(output))
	}
	return nil
}

func (t *SystemSSHTarget) ListVolumes(ctx context.Context) ([]string, error) {
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
		volumeID := strings.TrimPrefix(line, t.config.Pool+"/")
		if volumeID != "" {
			volumes = append(volumes, volumeID)
		}
	}

	sort.Strings(volumes)
	return volumes, nil
}

func (t *SystemSSHTarget) DeleteVolume(ctx context.Context, volumeID string) error {
	remoteDataset := t.remoteDataset(volumeID)
	command := fmt.Sprintf("zfs destroy -r %s", remoteDataset)

	output, err := t.runCommand(ctx, command)
	if err != nil {
		return fmt.Errorf("failed to destroy dataset %s: %s", remoteDataset, string(output))
	}
	return nil
}

func (t *SystemSSHTarget) ListAllReplicationSnapshots(ctx context.Context) ([]VolumeSnapshot, error) {
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
		fullName := fields[0]
		atIdx := strings.LastIndex(fullName, "@")
		if atIdx < 0 {
			continue
		}
		datasetPath := fullName[:atIdx]
		snapshotName := fullName[atIdx+1:]
		if !strings.HasPrefix(snapshotName, SnapshotPrefix) {
			continue
		}
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

func (t *SystemSSHTarget) Close() error {
	return nil
}
