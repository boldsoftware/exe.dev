//go:build linux

package zfs

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// GetDatasetName returns the full dataset name for an ID
func (s *ZFS) GetDatasetName(id string) string {
	return s.getDSName(id)
}

// GetOrigin returns the origin (parent snapshot) of a dataset, or empty string if none
func (s *ZFS) GetOrigin(id string) string {
	dsName := s.getDSName(id)
	return s.getDatasetOrigin(dsName)
}

// CreateMigrationSnapshot creates a snapshot for migration and returns its name and a cleanup function.
// The snapshot is named "{dataset}@migration".
func (s *ZFS) CreateMigrationSnapshot(ctx context.Context, id string) (string, func(), error) {
	unlock := s.lockVolume(id)
	defer unlock()

	dsName := s.getDSName(id)
	snapName := dsName + "@migration"

	// Destroy any existing migration snapshot from a previous failed attempt
	destroyCmd := exec.CommandContext(ctx, "zfs", "destroy", snapName)
	destroyCmd.Run() // Ignore error - snapshot may not exist

	s.log.DebugContext(ctx, "creating migration snapshot", "snapshot", snapName)

	cmd := exec.CommandContext(ctx, "zfs", "snapshot", snapName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("failed to create migration snapshot %s: %w (%s)", snapName, err, string(out))
	}

	cleanup := func() {
		s.log.DebugContext(ctx, "destroying migration snapshot", "snapshot", snapName)
		cmd := exec.Command("zfs", "destroy", snapName)
		if out, err := cmd.CombinedOutput(); err != nil {
			s.log.WarnContext(ctx, "failed to destroy migration snapshot", "snapshot", snapName, "error", err, "output", string(out))
		}
	}

	return snapName, cleanup, nil
}

// SendSnapshot streams ZFS snapshot data.
// If incremental is true, sends only the delta from baseSnap to snapName.
// Returns an io.ReadCloser that streams the zfs send output.
func (s *ZFS) SendSnapshot(ctx context.Context, snapName string, incremental bool, baseSnap string) (io.ReadCloser, error) {
	var args []string

	if incremental && baseSnap != "" {
		// Incremental send from baseSnap to snapName
		args = []string{"send", "-i", baseSnap, snapName}
	} else {
		// Full send of snapshot
		args = []string{"send", snapName}
	}

	s.log.DebugContext(ctx, "starting zfs send", "args", args)

	cmd := exec.CommandContext(ctx, "zfs", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe for zfs send: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe for zfs send: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start zfs send: %w", err)
	}

	// Return a wrapper that waits for the command to complete when closed
	return &zfsSendReader{
		ReadCloser: stdout,
		stderr:     stderr,
		cmd:        cmd,
		log:        s.log,
	}, nil
}

// zfsSendReader wraps the stdout pipe and waits for the command to complete on Close
type zfsSendReader struct {
	io.ReadCloser
	stderr io.ReadCloser
	cmd    *exec.Cmd
	log    interface {
		Debug(msg string, args ...any)
		Warn(msg string, args ...any)
	}
}

func (r *zfsSendReader) Close() error {
	// Close the stdout pipe
	if err := r.ReadCloser.Close(); err != nil {
		r.log.Warn("failed to close zfs send stdout", "error", err)
	}

	// Read stderr (limited to 4KB) before waiting
	stderrBytes, _ := io.ReadAll(io.LimitReader(r.stderr, 4096))
	r.stderr.Close()

	// Wait for the command to complete
	if err := r.cmd.Wait(); err != nil {
		if len(stderrBytes) > 0 {
			return fmt.Errorf("zfs send failed: %w (%s)", err, string(stderrBytes))
		}
		return fmt.Errorf("zfs send failed: %w", err)
	}

	return nil
}

// ReceiveSnapshot receives a ZFS stream and creates/updates a dataset.
// The stream is read from reader and applied to create dataset with the given id.
func (s *ZFS) ReceiveSnapshot(ctx context.Context, id string, reader io.Reader) error {
	unlock := s.lockVolume(id)
	defer unlock()

	dsName := s.getDSName(id)

	s.log.DebugContext(ctx, "starting zfs receive", "dataset", dsName)

	// Use -F to force rollback if needed (for incremental receives)
	cmd := exec.CommandContext(ctx, "zfs", "recv", "-F", dsName)
	cmd.Stdin = reader

	// Capture stderr for error messages
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start the command (don't use CombinedOutput as it doesn't work with streaming stdin)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start zfs recv: %w", err)
	}

	// Read stderr in a goroutine to avoid blocking (limit to 4KB for error messages)
	stderrCh := make(chan []byte, 1)
	go func() {
		limited := io.LimitReader(stderr, 4096)
		data, _ := io.ReadAll(limited)
		stderrCh <- data
		// Drain any remaining stderr to avoid blocking the command
		io.Copy(io.Discard, stderr)
	}()

	// Wait for command to complete
	waitErr := cmd.Wait()

	// Get stderr output
	stderrBytes := <-stderrCh

	if waitErr != nil {
		return fmt.Errorf("zfs recv failed for %s: %w (%s)", dsName, waitErr, string(stderrBytes))
	}

	// Wait for the zvol device to appear
	if err := s.waitForZvol(id); err != nil {
		return fmt.Errorf("timeout waiting for zvol after receive: %w", err)
	}

	s.log.DebugContext(ctx, "zfs receive complete", "dataset", dsName)

	return nil
}

// GetEncryptionKey returns the encryption key for a dataset, or nil if not encrypted
func (s *ZFS) GetEncryptionKey(id string) ([]byte, error) {
	ekPath, err := s.getInstanceEncryptionKeyPath(id)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(ekPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Not encrypted
		}
		return nil, fmt.Errorf("failed to read encryption key: %w", err)
	}

	return data, nil
}

// SetEncryptionKey stores an encryption key for a dataset
func (s *ZFS) SetEncryptionKey(id string, key []byte) error {
	ekPath, err := s.getInstanceEncryptionKeyPath(id)
	if err != nil {
		return err
	}

	// Ensure the directory exists
	if err := os.MkdirAll(filepath.Dir(ekPath), 0o770); err != nil {
		return fmt.Errorf("failed to create encryption key directory: %w", err)
	}

	// Write the key with secure permissions
	if err := os.WriteFile(ekPath, key, 0o600); err != nil {
		return fmt.Errorf("failed to write encryption key: %w", err)
	}

	return nil
}

// SnapshotExists checks if a ZFS snapshot exists
func (s *ZFS) SnapshotExists(snapName string) bool {
	cmd := exec.Command("zfs", "list", "-t", "snapshot", "-H", snapName)
	return cmd.Run() == nil
}

// CreateSnapshot creates a ZFS snapshot with the given full name
func (s *ZFS) CreateSnapshot(ctx context.Context, snapName string) error {
	s.log.DebugContext(ctx, "creating snapshot", "snapshot", snapName)

	cmd := exec.CommandContext(ctx, "zfs", "snapshot", snapName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create snapshot %s: %w (%s)", snapName, err, string(out))
	}

	return nil
}
