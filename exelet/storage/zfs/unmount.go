//go:build linux

package zfs

import (
	"context"
)

func (s *ZFS) Unmount(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.unmountInstanceFS(id); err != nil {
		return err
	}

	return nil
}
