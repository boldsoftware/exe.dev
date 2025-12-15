//go:build linux

package zfs

import (
	"context"
)

func (s *ZFS) Unmount(ctx context.Context, id string) error {
	unlock := s.lockVolume(id)
	defer unlock()

	if err := s.unmountInstanceFS(id); err != nil {
		return err
	}

	return nil
}
