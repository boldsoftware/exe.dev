package storage

import (
	"context"
	"io"

	api "exe.dev/pkg/api/exe/storage/v1"
)

// StorageManager is the interface to implement for storage managers
type StorageManager interface {
	// Type returns the type of manager
	Type() string
	// Get returns the specified filesystem
	Get(ctx context.Context, id string) (*api.Filesystem, error)
	// Create creates a new instance fs
	Create(ctx context.Context, id string, cfg *api.FilesystemConfig) (*api.Filesystem, error)
	// Clone clones a source instance FS to the target
	Clone(ctx context.Context, srcID, destID string) error
	// Expand resizes the specified instance FS to the desired size (must be larger than current).
	// If resizeFilesystem is true, runs fsck and resize2fs to expand the filesystem to match.
	// Set resizeFilesystem=false when the filesystem is mounted inside a running VM (resize must be done from inside the VM).
	Expand(ctx context.Context, id string, size uint64, resizeFilesystem bool) error
	// Shrink resizes the specified instance fs to the minimum
	Shrink(ctx context.Context, id string) error
	// Load ensures the instance fs is loaded and ready
	Load(ctx context.Context, id string) (*api.Filesystem, error)
	// Mount mounts the specified instance fs
	Mount(ctx context.Context, id string) (*api.FilesystemMountConfig, error)
	// Unmount unmounts the specified instance fs
	Unmount(ctx context.Context, id string) error
	// Rename renames a filesystem from oldID to newID
	Rename(ctx context.Context, oldID, newID string) error
	// Fsck runs filesystem check on the specified filesystem
	Fsck(ctx context.Context, id string) error
	// Delete removes an instance fs
	Delete(ctx context.Context, id string) error

	// Migration methods

	// GetDatasetName returns the full dataset name for an ID (e.g., "tank/instance-id")
	GetDatasetName(id string) string
	// GetOrigin returns the origin (parent snapshot) of a dataset, or empty string if none
	GetOrigin(id string) string
	// CreateMigrationSnapshot creates a snapshot for migration and returns its name and a cleanup function
	CreateMigrationSnapshot(ctx context.Context, id string) (snapName string, cleanup func(), err error)
	// SendSnapshot streams ZFS snapshot data. If incremental is true, sends only delta from baseSnap.
	SendSnapshot(ctx context.Context, snapName string, incremental bool, baseSnap string) (io.ReadCloser, error)
	// ReceiveSnapshot receives a ZFS stream and creates/updates a dataset
	ReceiveSnapshot(ctx context.Context, id string, reader io.Reader) error
	// GetEncryptionKey returns the encryption key for an encrypted dataset, or nil if not encrypted
	GetEncryptionKey(id string) ([]byte, error)
	// SetEncryptionKey stores an encryption key for a dataset
	SetEncryptionKey(id string, key []byte) error
	// SnapshotExists checks if a ZFS snapshot exists
	SnapshotExists(snapName string) bool
	// CreateSnapshot creates a ZFS snapshot with the given full name (e.g., "tank/sha256:...@instance-id")
	CreateSnapshot(ctx context.Context, snapName string) error
}
