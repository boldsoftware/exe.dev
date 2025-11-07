package storage

import (
	"context"

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
	Clone(ctx context.Context, srcID string, destID string) error
	// Expand resizes the specified instance FS to the desired size (must be larger than current)
	Expand(ctx context.Context, id string, size uint64) error
	// Shrink resizes the specified instance fs to the minimum
	Shrink(ctx context.Context, id string) error
	// Load ensures the instance fs is loaded and ready
	Load(ctx context.Context, id string) (*api.Filesystem, error)
	// Mount mounts the specified instance fs
	Mount(ctx context.Context, id string) (*api.FilesystemMountConfig, error)
	// Unmount unmounts the specified instance fs
	Unmount(ctx context.Context, id string) error
	// DeleteInstanceFS removes an instance fs
	Delete(ctx context.Context, id string) error
}
