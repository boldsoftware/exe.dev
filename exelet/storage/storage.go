package storage

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"exe.dev/exelet/storage/raw"
	"exe.dev/exelet/storage/zfs"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// StorageManager is the interface to implement for storage managers
type StorageManager interface {
	// Type returns the type of manager
	Type() string
	// CreateInstanceFS creates a new instance fs
	Create(ctx context.Context, id string, cfg *api.InstanceFilesystemConfig) (*api.InstanceFilesystem, error)
	// Load ensures the instance fs is loaded and ready
	Load(ctx context.Context, id string) (*api.InstanceFilesystem, error)
	// Mount mounts the specified instance fs
	Mount(ctx context.Context, id string) (*api.InstanceMountConfig, error)
	// Unmount unmounts the specified instance fs
	Unmount(ctx context.Context, id string) error
	// DeleteInstanceFS removes an instance fs
	Delete(ctx context.Context, id string) error
}

// NewStorageManager returns a new storage manager of the specified type
func NewStorageManager(addr string, log *slog.Logger) (StorageManager, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	switch strings.TrimSpace(strings.ToLower(u.Scheme)) {
	case "zfs":
		return zfs.NewZFS(addr, log)
	case "raw":
		return raw.NewRaw(addr, log)
	}

	return nil, fmt.Errorf("unsupported secret store type %q", u.Scheme)
}
