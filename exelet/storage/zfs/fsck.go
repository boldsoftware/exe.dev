//go:build linux

package zfs

import (
	"context"
	"fmt"
)

// Fsck runs filesystem check on the specified filesystem
func (s *ZFS) Fsck(ctx context.Context, id string) error {
	unlock := s.lockVolume(id)
	defer unlock()

	diskPath, err := s.getDSDiskPath(id)
	if err != nil {
		return fmt.Errorf("error getting disk path for %s: %w", id, err)
	}

	s.log.DebugContext(ctx, "running fsck on volume", "id", id, "diskPath", diskPath)
	if err := fsck(ctx, diskPath); err != nil {
		return fmt.Errorf("fsck failed for %s: %w", id, err)
	}

	return nil
}
