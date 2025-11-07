//go:build linux

package zfs

import (
	"context"
	"os"
)

// Delete removes an instance filesystem
func (s *ZFS) Delete(ctx context.Context, id string) error {
	// delete zfs volume
	if err := s.removeInstanceFS(id); err != nil {
		return err
	}

	// delete storage config
	instancePath, err := s.getInstanceDir(id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(instancePath); err != nil {
		return err
	}

	return nil
}
