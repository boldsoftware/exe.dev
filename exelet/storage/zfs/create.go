//go:build linux

package zfs

import (
	"context"
	"fmt"
	"os"

	api "exe.dev/pkg/api/exe/storage/v1"
)

// Create creates a new instance filesystem
func (s *ZFS) Create(ctx context.Context, id string, cfg *api.FilesystemConfig) (*api.Filesystem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var err error
	// check if exists
	if _, err := s.Get(ctx, id); err == nil {
		return nil, fmt.Errorf("%w: filesystem %s", api.ErrResourceExists, id)
	}

	// generate encryption key if specified
	if v := cfg.EncryptionKey; v != "" {
		s.log.DebugContext(ctx, "creating encrypted storage", "ds", id)
		// get and store encryption key
		ekPath, err := s.getInstanceEncryptionKeyPath(id)
		if err != nil {
			return nil, err
		}
		ek, err := os.Create(ekPath)
		if err != nil {
			return nil, fmt.Errorf("error storing encryption key: %w", err)
		}
		if _, err := ek.Write([]byte(v)); err != nil {
			return nil, fmt.Errorf("error writing encryption key: %w", err)
		}
		if err := ek.Close(); err != nil {
			return nil, fmt.Errorf("error closing encryption key: %w", err)
		}
	}
	encrypted := cfg.EncryptionKey != ""
	if err := s.createInstanceFS(id, cfg.Size, cfg.FsType, encrypted); err != nil {
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
