//go:build linux

package zfs

import (
	"context"

	api "exe.dev/pkg/api/exe/storage/v1"
)

func (s *ZFS) Load(ctx context.Context, id string) (*api.Filesystem, error) {
	if err := s.ensureFSExists(id); err != nil {
		return nil, err
	}

	diskPath, err := s.getDSDiskPath(id)
	if err != nil {
		return nil, err
	}

	return &api.Filesystem{
		ID:   id,
		Path: diskPath,
	}, nil
}
