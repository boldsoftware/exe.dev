package raw

import (
	"context"
	"fmt"
	"os"

	api "exe.dev/pkg/api/exe/storage/v1"
)

// CreateInstanceFS creates a new instance fs
func (s *Raw) Create(ctx context.Context, id string, cfg *api.FilesystemConfig) (*api.Filesystem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// TODO: support encryption
	if v := cfg.EncryptionKey; v != "" {
		return nil, fmt.Errorf("encryption not supported for %s storage", storageType)
	}

	// create backing disk
	diskPath, err := s.getInstanceDiskPath(id)
	if err != nil {
		return nil, err
	}
	if err := s.allocateDisk(diskPath, cfg.Size); err != nil {
		return nil, err
	}
	// loop
	devicePath, err := setupLoopDevice(diskPath)
	if err != nil {
		return nil, err
	}
	// format
	if err := formatDisk(devicePath, cfg.FsType); err != nil {
		return nil, err
	}

	// update state file
	if err := os.WriteFile(s.getInstanceStatePath(id), []byte(devicePath), 0o600); err != nil {
		return nil, err
	}

	return &api.Filesystem{
		ID:   id,
		Path: devicePath,
	}, nil
}
