//go:build linux

package zfs

import (
	"context"

	"github.com/mistifyio/go-zfs/v3"
)

// Clone clones the source filesystem to the destination
func (s *ZFS) Clone(ctx context.Context, srcID, destID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.Get(ctx, srcID); err != nil {
		return err
	}

	srcDSName := s.getDSName(srcID)
	destDSName := s.getDSName(destID)

	srcDS, err := zfs.GetDataset(srcDSName)
	if err != nil {
		return err
	}

	// snapshot name is the dest ID
	snapName := destID
	cloneDS, err := srcDS.Snapshot(snapName, false)
	if err != nil {
		return err
	}

	if _, err := cloneDS.Clone(destDSName, nil); err != nil {
		return err
	}

	// wait until volume is ready
	if err := s.waitForZvol(destID); err != nil {
		return err
	}

	return nil
}
