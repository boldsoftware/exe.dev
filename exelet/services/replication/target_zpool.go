package replication

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// ZpoolTarget implements Target for local zpool-to-zpool replication using zfs send | zfs recv.
type ZpoolTarget struct {
	config *TargetConfig
}

// NewZpoolTarget creates a new ZpoolTarget.
func NewZpoolTarget(cfg *TargetConfig) *ZpoolTarget {
	return &ZpoolTarget{config: cfg}
}

func (t *ZpoolTarget) Type() string {
	return "zpool"
}

// runCommand executes a command locally and returns output.
func (t *ZpoolTarget) runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// dataset returns the dataset path for a volume on the target pool.
func (t *ZpoolTarget) dataset(volumeID string) string {
	return fmt.Sprintf("%s/%s", t.config.Pool, volumeID)
}

func (t *ZpoolTarget) ListSnapshots(ctx context.Context, volumeID string) ([]string, error) {
	ds := t.dataset(volumeID)
	output, err := t.runCommand(ctx, "zfs", "list", "-t", "snapshot", "-H", "-o", "name", "-s", "creation", ds)
	if err != nil {
		if strings.Contains(string(output), "dataset does not exist") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list snapshots: %s", string(output))
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

func (t *ZpoolTarget) ListSnapshotsWithMetadata(ctx context.Context, volumeID string) ([]SnapshotMetadata, error) {
	ds := t.dataset(volumeID)
	output, err := t.runCommand(ctx, "zfs", "list", "-t", "snapshot", "-H", "-p", "-o", "name,creation,referenced", "-s", "creation", ds)
	if err != nil {
		if strings.Contains(string(output), "dataset does not exist") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list snapshots: %s", string(output))
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

func (t *ZpoolTarget) Send(ctx context.Context, opts SendOptions) error {
	estimatedSize := estimateSendSize(ctx, opts.Dataset, opts.SnapshotName, opts.BaseSnapshot)

	// Build local zfs send command
	var sendArgs []string
	if opts.BaseSnapshot != "" {
		sendArgs = []string{"send", "-i", fmt.Sprintf("%s@%s", opts.Dataset, opts.BaseSnapshot), fmt.Sprintf("%s@%s", opts.Dataset, opts.SnapshotName)}
	} else {
		sendArgs = []string{"send", fmt.Sprintf("%s@%s", opts.Dataset, opts.SnapshotName)}
	}
	sendCmd := exec.CommandContext(ctx, "zfs", sendArgs...)

	// Build local zfs recv command
	targetDataset := t.dataset(opts.VolumeID)
	recvCmd := exec.CommandContext(ctx, "zfs", "recv", "-F", targetDataset)

	bandwidthLimit := opts.BandwidthLimit
	if bandwidthLimit == "" {
		bandwidthLimit = t.config.BandwidthLimit
	}

	sendStdout, err := sendCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create send stdout pipe: %w", err)
	}

	var sendStderr, recvStderr strings.Builder
	sendCmd.Stderr = &sendStderr
	recvCmd.Stderr = &recvStderr

	// copyDone signals when the background copy goroutine has finished
	copyDone := make(chan struct{})
	copyUsed := false

	if bandwidthLimit != "" {
		// Pipeline: zfs send | pv -L rate | zfs recv
		pvCmd := exec.CommandContext(ctx, "pv", "-q", "-L", bandwidthLimit)
		pvCmd.Stdin = sendStdout

		pvStdout, err := pvCmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("failed to create pv stdout pipe: %w", err)
		}

		if opts.OnProgress != nil && estimatedSize > 0 {
			copyUsed = true
			recvStdin, err := recvCmd.StdinPipe()
			if err != nil {
				return fmt.Errorf("failed to create recv stdin pipe: %w", err)
			}
			counter := newCountingWriter(recvStdin, estimatedSize, opts.OnProgress)
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
			recvCmd.Stdin = pvStdout
		}

		if err := sendCmd.Start(); err != nil {
			return fmt.Errorf("failed to start zfs send: %w", err)
		}
		if err := pvCmd.Start(); err != nil {
			sendCmd.Process.Kill()
			return fmt.Errorf("failed to start pv: %w", err)
		}
		if err := recvCmd.Start(); err != nil {
			sendCmd.Process.Kill()
			pvCmd.Process.Kill()
			return fmt.Errorf("failed to start zfs recv: %w", err)
		}

		if copyUsed {
			<-copyDone
		}
		sendErr := sendCmd.Wait()
		pvErr := pvCmd.Wait()
		recvErr := recvCmd.Wait()

		recvErrMsg := strings.TrimSpace(recvStderr.String())
		if recvErr != nil {
			return fmt.Errorf("zfs recv failed: %w (stderr: %s)", recvErr, recvErrMsg)
		}
		if pvErr != nil {
			return fmt.Errorf("pv failed: %w", pvErr)
		}
		if sendErr != nil {
			return fmt.Errorf("zfs send failed: %w (stderr: %s)", sendErr, sendStderr.String())
		}
	} else {
		// Simple pipeline: zfs send | zfs recv
		if opts.OnProgress != nil && estimatedSize > 0 {
			copyUsed = true
			recvStdin, err := recvCmd.StdinPipe()
			if err != nil {
				return fmt.Errorf("failed to create recv stdin pipe: %w", err)
			}
			counter := newCountingWriter(recvStdin, estimatedSize, opts.OnProgress)
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
			recvCmd.Stdin = sendStdout
		}

		if err := sendCmd.Start(); err != nil {
			return fmt.Errorf("failed to start zfs send: %w", err)
		}
		if err := recvCmd.Start(); err != nil {
			sendCmd.Process.Kill()
			return fmt.Errorf("failed to start zfs recv: %w", err)
		}

		if copyUsed {
			<-copyDone
		}
		sendErr := sendCmd.Wait()
		recvErr := recvCmd.Wait()

		recvErrMsg := strings.TrimSpace(recvStderr.String())
		sendErrMsg := strings.TrimSpace(sendStderr.String())

		if recvErr != nil && recvErrMsg != "" {
			return fmt.Errorf("zfs recv failed: %s", recvErrMsg)
		}
		if sendErr != nil && recvErr != nil && strings.Contains(sendErr.Error(), "141") {
			if recvErrMsg != "" {
				return fmt.Errorf("zfs recv failed: %s", recvErrMsg)
			}
			return fmt.Errorf("zfs recv failed: %w", recvErr)
		}
		if sendErr != nil {
			if sendErrMsg != "" {
				return fmt.Errorf("zfs send failed: %s", sendErrMsg)
			}
			return fmt.Errorf("zfs send failed: %w", sendErr)
		}
		if recvErr != nil {
			if recvErrMsg != "" {
				return fmt.Errorf("zfs recv failed: %s", recvErrMsg)
			}
			return fmt.Errorf("zfs recv failed: %w", recvErr)
		}
	}

	return nil
}

func (t *ZpoolTarget) Receive(ctx context.Context, opts ReceiveOptions) error {
	// Build zfs send from backup pool
	sourceDataset := t.dataset(opts.VolumeID)
	snapshotPath := fmt.Sprintf("%s@%s", sourceDataset, opts.SnapshotName)
	sendCmd := exec.CommandContext(ctx, "zfs", "send", snapshotPath)

	// Build local zfs recv command
	recvArgs := []string{"recv"}
	if opts.Force {
		recvArgs = append(recvArgs, "-F")
	}
	recvArgs = append(recvArgs, opts.Dataset)
	recvCmd := exec.CommandContext(ctx, "zfs", recvArgs...)

	// Pipeline: zfs send | zfs recv
	sendStdout, err := sendCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create send stdout pipe: %w", err)
	}
	recvCmd.Stdin = sendStdout

	var sendStderr, recvStderr strings.Builder
	sendCmd.Stderr = &sendStderr
	recvCmd.Stderr = &recvStderr

	if err := sendCmd.Start(); err != nil {
		return fmt.Errorf("failed to start zfs send: %w", err)
	}
	if err := recvCmd.Start(); err != nil {
		sendCmd.Process.Kill()
		return fmt.Errorf("failed to start zfs recv: %w", err)
	}

	sendErr := sendCmd.Wait()
	recvErr := recvCmd.Wait()

	recvErrMsg := strings.TrimSpace(recvStderr.String())
	sendErrMsg := strings.TrimSpace(sendStderr.String())

	if recvErr != nil && recvErrMsg != "" {
		return fmt.Errorf("zfs recv failed: %s", recvErrMsg)
	}
	if sendErr != nil && recvErr != nil && strings.Contains(sendErr.Error(), "141") {
		if recvErrMsg != "" {
			return fmt.Errorf("zfs recv failed: %s", recvErrMsg)
		}
		return fmt.Errorf("zfs recv failed: %w", recvErr)
	}
	if sendErr != nil {
		if sendErrMsg != "" {
			return fmt.Errorf("zfs send failed (%s): %s", snapshotPath, sendErrMsg)
		}
		return fmt.Errorf("zfs send failed (%s): %w", snapshotPath, sendErr)
	}
	if recvErr != nil {
		if recvErrMsg != "" {
			return fmt.Errorf("zfs recv failed: %s", recvErrMsg)
		}
		return fmt.Errorf("zfs recv failed: %w", recvErr)
	}

	return nil
}

func (t *ZpoolTarget) Delete(ctx context.Context, volumeID, snapshotName string) error {
	snapshotPath := fmt.Sprintf("%s@%s", t.dataset(volumeID), snapshotName)
	output, err := t.runCommand(ctx, "zfs", "destroy", snapshotPath)
	if err != nil {
		return fmt.Errorf("failed to destroy snapshot %s: %s", snapshotPath, string(output))
	}
	return nil
}

func (t *ZpoolTarget) DeleteVolume(ctx context.Context, volumeID string) error {
	ds := t.dataset(volumeID)
	output, err := t.runCommand(ctx, "zfs", "destroy", "-r", ds)
	if err != nil {
		return fmt.Errorf("failed to destroy dataset %s: %s", ds, string(output))
	}
	return nil
}

func (t *ZpoolTarget) ListVolumes(ctx context.Context) ([]string, error) {
	output, err := t.runCommand(ctx, "zfs", "list", "-H", "-o", "name", "-r", "-d", "1", t.config.Pool)
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

func (t *ZpoolTarget) ListAllReplicationSnapshots(ctx context.Context) ([]VolumeSnapshot, error) {
	output, err := t.runCommand(ctx, "zfs", "list", "-t", "snapshot", "-H", "-p", "-o", "name,creation,referenced", "-r", t.config.Pool)
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

func (t *ZpoolTarget) Close() error {
	return nil
}
