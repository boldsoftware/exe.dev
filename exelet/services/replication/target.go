package replication

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// SnapshotMetadata holds detailed information about a snapshot
type SnapshotMetadata struct {
	Name      string // Snapshot name (e.g., repl-20240115T143022Z)
	CreatedAt int64  // Unix timestamp of creation
	SizeBytes int64  // Size in bytes (referenced)
}

// VolumeSnapshot combines a volume ID with its snapshot metadata
type VolumeSnapshot struct {
	VolumeID string
	Snapshot SnapshotMetadata
}

// Target represents a replication destination
type Target interface {
	// Type returns the target type ("ssh" or "file")
	Type() string

	// ListSnapshots returns existing snapshots for a volume on the target
	// For SSH: queries remote ZFS for snapshots
	// For file: lists existing backup files
	ListSnapshots(ctx context.Context, volumeID string) ([]string, error)

	// ListSnapshotsWithMetadata returns snapshots with full metadata for a volume
	ListSnapshotsWithMetadata(ctx context.Context, volumeID string) ([]SnapshotMetadata, error)

	// Send transfers a snapshot to the target
	// For SSH: pipes zfs send to remote zfs recv
	// For file: creates a tar.gz backup
	Send(ctx context.Context, opts SendOptions) error

	// Receive restores a snapshot from the target
	// For SSH: pipes remote zfs send to local zfs recv
	// For file: extracts tar.gz backup
	Receive(ctx context.Context, opts ReceiveOptions) error

	// Delete removes a snapshot or backup from the target
	Delete(ctx context.Context, volumeID, snapshotName string) error

	// ListVolumes returns all volume IDs present on the target
	ListVolumes(ctx context.Context) ([]string, error)

	// ListAllReplicationSnapshots lists all replication snapshots across all volumes
	ListAllReplicationSnapshots(ctx context.Context) ([]VolumeSnapshot, error)

	// Close releases any resources held by the target
	Close() error
}

// ProgressFunc is called periodically during send with bytes transferred and total
type ProgressFunc func(bytesTransferred, bytesTotal int64)

// SendOptions configures a send operation
type SendOptions struct {
	VolumeID       string
	Dataset        string       // Full ZFS dataset path (e.g., tank/vm-abc123)
	SnapshotName   string       // Name of snapshot to send (e.g., repl-20240115T143022Z)
	BaseSnapshot   string       // For incremental: name of base snapshot (empty for full)
	BandwidthLimit string       // Rate limit (e.g., "100M", "1G")
	OnProgress     ProgressFunc // Optional callback for progress updates
}

// ReceiveOptions configures a receive operation
type ReceiveOptions struct {
	VolumeID     string
	SnapshotName string // For SSH: snapshot name; for file: backup filename
	Dataset      string // Target ZFS dataset path
	Force        bool   // Overwrite existing dataset
}

// TargetConfig holds parsed configuration for a target
type TargetConfig struct {
	Type           string // "ssh" or "file"
	User           string // SSH user (ssh only)
	Host           string // SSH host (ssh only)
	Port           string // SSH port (ssh only, empty for default)
	Pool           string // Remote ZFS pool (ssh only)
	Path           string // Local path (file only)
	SSHKeyPath     string // Path to SSH private key
	KnownHostsPath string // Path to known_hosts file (ssh only, empty uses ~/.ssh/known_hosts)
	BandwidthLimit string
}

// ParseTarget parses a target URL and returns a configured target
func ParseTarget(targetURL, sshKeyPath, knownHostsPath, bandwidthLimit string) (Target, error) {
	cfg, err := ParseTargetConfig(targetURL)
	if err != nil {
		return nil, err
	}
	cfg.SSHKeyPath = sshKeyPath
	cfg.KnownHostsPath = knownHostsPath
	cfg.BandwidthLimit = bandwidthLimit

	switch cfg.Type {
	case "ssh":
		return NewSSHTarget(cfg)
	case "file":
		return NewFileTarget(cfg)
	default:
		return nil, fmt.Errorf("unsupported target type: %s", cfg.Type)
	}
}

// ParseTargetConfig parses a target URL into configuration
func ParseTargetConfig(targetURL string) (*TargetConfig, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}

	switch u.Scheme {
	case "ssh":
		return parseSSHTarget(u)
	case "file":
		return parseFileTarget(u)
	default:
		return nil, fmt.Errorf("unsupported target scheme: %s (expected ssh:// or file://)", u.Scheme)
	}
}

func parseSSHTarget(u *url.URL) (*TargetConfig, error) {
	if u.Host == "" {
		return nil, fmt.Errorf("SSH target requires host: ssh://user@host/pool")
	}

	user := u.User.Username()
	if user == "" {
		return nil, fmt.Errorf("SSH target requires user: ssh://user@host/pool")
	}

	pool := strings.TrimPrefix(u.Path, "/")
	if pool == "" {
		return nil, fmt.Errorf("SSH target requires pool name: ssh://user@host/pool")
	}

	// Parse host and port separately
	host := u.Hostname()
	port := u.Port()

	return &TargetConfig{
		Type: "ssh",
		User: user,
		Host: host,
		Port: port,
		Pool: pool,
	}, nil
}

func parseFileTarget(u *url.URL) (*TargetConfig, error) {
	path := u.Path
	if path == "" {
		return nil, fmt.Errorf("file target requires path: file:///path/to/backup")
	}

	return &TargetConfig{
		Type: "file",
		Path: path,
	}, nil
}
