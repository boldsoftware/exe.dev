package raw

import (
	"context"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// Mount mounts the specified instance fs
func (s *Raw) Mount(ctx context.Context, id string) (*api.InstanceMountConfig, error) {
	mountpoint, err := s.mountInstanceFS(id)
	if err != nil {
		return nil, err
	}

	return &api.InstanceMountConfig{
		ID:   id,
		Path: mountpoint,
	}, nil
}
