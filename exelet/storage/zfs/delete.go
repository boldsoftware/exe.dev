//go:build linux

package zfs

import (
	"context"
	"os"
)

// Delete removes an instance filesystem
func (s *ZFS) Delete(ctx context.Context, id string) error {
	// Ensure volume is unmounted before attempting to delete
	// This prevents "device or resource busy" errors when destroying the ZFS volume
	if err := s.unmountInstanceFS(id); err != nil {
		// Log but continue - the volume might not be mounted, which is fine
		s.log.DebugContext(ctx, "failed to unmount before delete (continuing)", "id", id, "error", err)
	}

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
