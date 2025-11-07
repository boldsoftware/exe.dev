package raw

import (
	"context"

	api "exe.dev/pkg/api/exe/storage/v1"
)

// Mount mounts the specified instance fs
func (s *Raw) Mount(ctx context.Context, id string) (*api.FilesystemMountConfig, error) {
	mountpoint, err := s.mountInstanceFS(id)
	if err != nil {
		return nil, err
	}

	return &api.FilesystemMountConfig{
		ID:   id,
		Path: mountpoint,
	}, nil
}
