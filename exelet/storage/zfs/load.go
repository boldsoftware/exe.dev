package zfs

import (
	"context"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *ZFS) Load(ctx context.Context, id string) (*api.InstanceFilesystem, error) {
	if err := s.ensureFSExists(id); err != nil {
		return nil, err
	}

	diskPath, err := s.getDSDiskPath(id)
	if err != nil {
		return nil, err
	}

	return &api.InstanceFilesystem{
		ID:   id,
		Path: diskPath,
	}, nil
}
