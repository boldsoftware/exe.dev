//go:build linux

package zfs

import (
	"context"

	api "exe.dev/pkg/api/exe/storage/v1"
)

func (s *ZFS) Mount(ctx context.Context, id string) (*api.FilesystemMountConfig, error) {
	unlock := s.lockVolume(id)
	defer unlock()

	mountpoint, err := s.mountInstanceFS(id)
	if err != nil {
		return nil, err
	}

	return &api.FilesystemMountConfig{
		ID:   id,
		Path: mountpoint,
	}, nil
}
