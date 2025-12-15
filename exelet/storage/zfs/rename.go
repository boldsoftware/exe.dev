//go:build linux

package zfs

import (
	"context"
	"fmt"

	"github.com/mistifyio/go-zfs/v3"
)

// Rename renames a filesystem from oldID to newID
func (s *ZFS) Rename(ctx context.Context, oldID, newID string) error {
	unlock := s.lockVolumes(oldID, newID)
	defer unlock()

	oldDSName := s.getDSName(oldID)
	newDSName := s.getDSName(newID)

	ds, err := zfs.GetDataset(oldDSName)
	if err != nil {
		return fmt.Errorf("error getting dataset %s: %w", oldDSName, err)
	}

	if _, err := ds.Rename(newDSName, false, false); err != nil {
		return fmt.Errorf("error renaming dataset %s to %s: %w", oldDSName, newDSName, err)
	}

	// wait until the new zvol is ready
	if err := s.waitForZvol(newID); err != nil {
		return fmt.Errorf("error waiting for renamed zvol %s: %w", newID, err)
	}

	return nil
}
