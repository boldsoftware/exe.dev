//go:build linux

package zfs

import (
	"context"
	"fmt"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/mistifyio/go-zfs/v3"

	api "exe.dev/pkg/api/exe/storage/v1"
)

func (s *ZFS) Get(ctx context.Context, id string) (*api.Filesystem, error) {
	dsName := s.getDSName(id)
	ds, err := zfs.GetDataset(dsName)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, api.ErrNotFound
		}
		return nil, err
	}

	diskPath, err := s.getDSDiskPath(id)
	if err != nil {
		return nil, err
	}

	volSize, err := ds.GetProperty("volsize")
	if err != nil {
		return nil, fmt.Errorf("error getting volsize: %w", err)
	}

	size, err := humanize.ParseBytes(volSize)
	if err != nil {
		return nil, fmt.Errorf("error parsing volsize: %w", err)
	}

	return &api.Filesystem{
		ID:   id,
		Path: diskPath,
		Size: size,
	}, nil
}
