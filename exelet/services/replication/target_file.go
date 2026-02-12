package replication

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// progressWriter wraps a writer and reports bytes written via a callback.
type progressWriter struct {
	w          io.Writer
	written    int64
	onProgress ProgressFunc
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	pw.written += int64(n)
	pw.onProgress(pw.written, 0)
	return n, err
}

// FileTarget implements Target for backup to compressed tarballs
type FileTarget struct {
	config *TargetConfig
}

// NewFileTarget creates a new file target
func NewFileTarget(cfg *TargetConfig) (*FileTarget, error) {
	// Ensure backup directory exists
	if err := os.MkdirAll(cfg.Path, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create backup directory %s: %w", cfg.Path, err)
	}

	return &FileTarget{config: cfg}, nil
}

func (t *FileTarget) Type() string {
	return "file"
}

// backupFilename generates the backup filename for a volume and timestamp
func (t *FileTarget) backupFilename(volumeID string, timestamp time.Time) string {
	return fmt.Sprintf("%s-%s.tar.gz", volumeID, timestamp.Format("20060102T150405Z"))
}

// backupPath returns the full path for a backup file
func (t *FileTarget) backupPath(filename string) string {
	return filepath.Join(t.config.Path, filename)
}

// parseBackupFilename extracts volume ID and timestamp from a backup filename
// Returns volumeID, timestamp, success
func (t *FileTarget) parseBackupFilename(filename string) (string, time.Time, bool) {
	// Pattern: <volume-id>-<timestamp>.tar.gz
	// Timestamp format: 20060102T150405Z
	re := regexp.MustCompile(`^(.+)-(\d{8}T\d{6}Z)\.tar\.gz$`)
	matches := re.FindStringSubmatch(filename)
	if len(matches) != 3 {
		return "", time.Time{}, false
	}

	volumeID := matches[1]
	timestamp, err := time.Parse("20060102T150405Z", matches[2])
	if err != nil {
		return "", time.Time{}, false
	}

	return volumeID, timestamp, true
}

// ListSnapshots returns existing backup files for a volume
// Returns filenames sorted by timestamp (oldest first)
func (t *FileTarget) ListSnapshots(ctx context.Context, volumeID string) ([]string, error) {
	entries, err := os.ReadDir(t.config.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read backup directory: %w", err)
	}

	type backupInfo struct {
		filename  string
		timestamp time.Time
	}

	var backups []backupInfo
	prefix := volumeID + "-"
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		vid, ts, ok := t.parseBackupFilename(name)
		if !ok || vid != volumeID {
			continue
		}
		backups = append(backups, backupInfo{filename: name, timestamp: ts})
	}

	// Sort by timestamp (oldest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].timestamp.Before(backups[j].timestamp)
	})

	result := make([]string, len(backups))
	for i, b := range backups {
		result[i] = b.filename
	}

	return result, nil
}

// ListSnapshotsWithMetadata returns snapshots with full metadata for a volume
func (t *FileTarget) ListSnapshotsWithMetadata(ctx context.Context, volumeID string) ([]SnapshotMetadata, error) {
	entries, err := os.ReadDir(t.config.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read backup directory: %w", err)
	}

	type backupInfo struct {
		filename  string
		timestamp time.Time
		size      int64
	}

	var backups []backupInfo
	prefix := volumeID + "-"
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		vid, ts, ok := t.parseBackupFilename(name)
		if !ok || vid != volumeID {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		backups = append(backups, backupInfo{filename: name, timestamp: ts, size: info.Size()})
	}

	// Sort by timestamp (oldest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].timestamp.Before(backups[j].timestamp)
	})

	result := make([]SnapshotMetadata, len(backups))
	for i, b := range backups {
		result[i] = SnapshotMetadata{
			Name:      b.filename,
			CreatedAt: b.timestamp.Unix(),
			SizeBytes: b.size,
		}
	}

	return result, nil
}

// Send creates a compressed zfs send backup of the snapshot
func (t *FileTarget) Send(ctx context.Context, opts SendOptions) error {
	snapshotPath := fmt.Sprintf("%s@%s", opts.Dataset, opts.SnapshotName)

	// Generate backup filename with current timestamp
	timestamp := time.Now().UTC()
	filename := t.backupFilename(opts.VolumeID, timestamp)
	backupPath := t.backupPath(filename)
	tempPath := backupPath + ".tmp"

	// Build zfs send command (-c passes through compressed blocks)
	var sendArgs []string
	if opts.BaseSnapshot != "" {
		sendArgs = []string{"send", "-c", "-i", fmt.Sprintf("%s@%s", opts.Dataset, opts.BaseSnapshot), snapshotPath}
	} else {
		sendArgs = []string{"send", "-c", snapshotPath}
	}
	sendCmd := exec.CommandContext(ctx, "zfs", sendArgs...)

	sendStdout, err := sendCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Create the backup file
	file, err := os.Create(tempPath)
	if err != nil {
		return fmt.Errorf("failed to create backup file: %w", err)
	}
	defer func() {
		file.Close()
		os.Remove(tempPath) // Cleanup on error
	}()

	// Pipe zfs send through gzip to file
	var writer io.Writer = file
	bandwidthLimit := opts.BandwidthLimit
	if bandwidthLimit == "" {
		bandwidthLimit = t.config.BandwidthLimit
	}
	if bandwidthLimit != "" {
		writer = newRateLimitedWriter(writer, bandwidthLimit)
	}

	gzWriter, _ := gzip.NewWriterLevel(writer, gzip.BestSpeed)

	if err := sendCmd.Start(); err != nil {
		return fmt.Errorf("failed to start zfs send: %w", err)
	}

	var dst io.Writer = gzWriter
	if opts.OnProgress != nil {
		dst = &progressWriter{w: gzWriter, onProgress: opts.OnProgress}
	}
	_, copyErr := io.Copy(dst, sendStdout)

	if err := sendCmd.Wait(); err != nil {
		return fmt.Errorf("zfs send failed: %w", err)
	}
	if copyErr != nil {
		return fmt.Errorf("failed to write backup data: %w", copyErr)
	}

	if err := gzWriter.Close(); err != nil {
		return fmt.Errorf("failed to close gzip writer: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close backup file: %w", err)
	}

	// Rename temp file to final name
	if err := os.Rename(tempPath, backupPath); err != nil {
		return fmt.Errorf("failed to finalize backup file: %w", err)
	}

	return nil
}

// Receive restores a backup from a compressed zfs send stream
func (t *FileTarget) Receive(ctx context.Context, opts ReceiveOptions) error {
	backupPath := t.backupPath(opts.SnapshotName)

	// Check if backup exists
	if _, err := os.Stat(backupPath); err != nil {
		return fmt.Errorf("backup file not found: %s", backupPath)
	}

	// Check if target dataset exists
	checkCmd := exec.CommandContext(ctx, "zfs", "list", "-H", opts.Dataset)
	datasetExists := checkCmd.Run() == nil

	if datasetExists && !opts.Force {
		return fmt.Errorf("dataset %s already exists (use force to overwrite)", opts.Dataset)
	}

	// If force and exists, destroy the existing dataset
	if datasetExists && opts.Force {
		destroyCmd := exec.CommandContext(ctx, "zfs", "destroy", "-r", opts.Dataset)
		if output, err := destroyCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to destroy existing dataset: %s", string(output))
		}
	}

	// Open backup file
	file, err := os.Open(backupPath)
	if err != nil {
		return fmt.Errorf("failed to open backup file: %w", err)
	}
	defer file.Close()

	// Create gzip reader
	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzReader.Close()

	// Pipe through zfs receive
	recvCmd := exec.CommandContext(ctx, "zfs", "receive", "-F", opts.Dataset)
	recvCmd.Stdin = gzReader

	if output, err := recvCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("zfs receive failed: %s", string(output))
	}

	return nil
}

// Delete removes a backup file
func (t *FileTarget) Delete(ctx context.Context, volumeID, snapshotName string) error {
	backupPath := t.backupPath(snapshotName)
	if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete backup %s: %w", backupPath, err)
	}
	return nil
}

// ListVolumes returns all volume IDs that have backups
func (t *FileTarget) ListVolumes(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(t.config.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read backup directory: %w", err)
	}

	volumeSet := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		volumeID, _, ok := t.parseBackupFilename(entry.Name())
		if ok {
			volumeSet[volumeID] = struct{}{}
		}
	}

	volumes := make([]string, 0, len(volumeSet))
	for v := range volumeSet {
		volumes = append(volumes, v)
	}
	sort.Strings(volumes)

	return volumes, nil
}

// ListAllReplicationSnapshots lists all replication snapshots across all volumes
func (t *FileTarget) ListAllReplicationSnapshots(ctx context.Context) ([]VolumeSnapshot, error) {
	entries, err := os.ReadDir(t.config.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read backup directory: %w", err)
	}

	var snapshots []VolumeSnapshot
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		volumeID, ts, ok := t.parseBackupFilename(entry.Name())
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		snapshots = append(snapshots, VolumeSnapshot{
			VolumeID: volumeID,
			Snapshot: SnapshotMetadata{
				Name:      entry.Name(),
				CreatedAt: ts.Unix(),
				SizeBytes: info.Size(),
			},
		})
	}

	return snapshots, nil
}

// DeleteVolume removes all backup files for a volume
func (t *FileTarget) DeleteVolume(ctx context.Context, volumeID string) error {
	snapshots, err := t.ListSnapshots(ctx, volumeID)
	if err != nil {
		return err
	}

	for _, snapshot := range snapshots {
		if err := t.Delete(ctx, volumeID, snapshot); err != nil {
			return err
		}
	}

	return nil
}

func (t *FileTarget) Close() error {
	return nil
}

// rateLimitedWriter wraps a writer with rate limiting
type rateLimitedWriter struct {
	w         io.Writer
	rateBytes int64 // bytes per second
}

func newRateLimitedWriter(w io.Writer, limit string) *rateLimitedWriter {
	rate := parseRateLimit(limit)
	return &rateLimitedWriter{w: w, rateBytes: rate}
}

func (r *rateLimitedWriter) Write(p []byte) (n int, err error) {
	if r.rateBytes <= 0 {
		return r.w.Write(p)
	}

	// Simple rate limiting: write in chunks with sleeps
	// This is a basic implementation; production might use token bucket
	// Divide by 10 to get 100ms chunks
	chunkSize := max(int(r.rateBytes/10), 1024)

	written := 0
	for written < len(p) {
		end := min(len(p), written+chunkSize)

		n, err := r.w.Write(p[written:end])
		written += n
		if err != nil {
			return written, err
		}

		// Sleep proportionally
		sleepDuration := time.Duration(float64(n) / float64(r.rateBytes) * float64(time.Second))
		if sleepDuration > 0 {
			time.Sleep(sleepDuration)
		}
	}

	return written, nil
}

// parseRateLimit parses a rate limit string like "100M" or "1G" to bytes per second
func parseRateLimit(limit string) int64 {
	if limit == "" {
		return 0
	}

	limit = strings.ToUpper(strings.TrimSpace(limit))
	multiplier := int64(1)

	if strings.HasSuffix(limit, "G") {
		multiplier = 1024 * 1024 * 1024
		limit = strings.TrimSuffix(limit, "G")
	} else if strings.HasSuffix(limit, "M") {
		multiplier = 1024 * 1024
		limit = strings.TrimSuffix(limit, "M")
	} else if strings.HasSuffix(limit, "K") {
		multiplier = 1024
		limit = strings.TrimSuffix(limit, "K")
	}

	var value int64
	fmt.Sscanf(limit, "%d", &value)
	return value * multiplier
}
