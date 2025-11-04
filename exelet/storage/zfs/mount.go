package zfs

import (
	"context"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *ZFS) Mount(ctx context.Context, id string) (*api.InstanceMountConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mountpoint, err := s.mountInstanceFS(id)
	if err != nil {
		return nil, err
	}

	return &api.InstanceMountConfig{
		ID:   id,
		Path: mountpoint,
	}, nil
}
