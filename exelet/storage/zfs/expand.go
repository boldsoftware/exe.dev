//go:build linux

package zfs

import (
	"context"
	"fmt"

	"github.com/dustin/go-humanize"
	"github.com/mistifyio/go-zfs/v3"
)

// Expand resizes the specified filesystem to the desired size.
// If resizeFilesystem is true, runs fsck and resize2fs to expand the filesystem.
// Set resizeFilesystem=false when the filesystem is mounted inside a running VM
// (resize must be done from inside the VM using resize2fs /dev/vda).
func (s *ZFS) Expand(ctx context.Context, id string, size uint64, resizeFilesystem bool) error {
	unlock := s.lockVolume(id)
	defer unlock()

	dsName := s.getDSName(id)
	ds, err := zfs.GetDataset(dsName)
	if err != nil {
		return err
	}

	// check that the requested size is larger than the current volsize
	sz, err := ds.GetProperty("volsize")
	if err != nil {
		return err
	}
	volSize, err := humanize.ParseBytes(sz)
	if err != nil {
		return err
	}

	// check if equal and return - no resize needed
	if volSize == size {
		return nil
	}

	// ensure larger
	if size < volSize {
		return fmt.Errorf("instance fs size (%d) cannot be smaller than current (%d)", size, volSize)
	}

	// zfs needs to be 4K aligned
	newSize := align4K(size)

	s.log.DebugContext(ctx, "expanding volume", "id", id, "size", newSize, "resizeFilesystem", resizeFilesystem)
	// note: volumes remain sparse (no refreservation) to allow efficient space sharing
	if err := ds.SetProperty("volsize", fmt.Sprintf("%d", newSize)); err != nil {
		return err
	}

	// If resizeFilesystem is true, run fsck and resize2fs to expand the filesystem
	// This should be done when the filesystem is NOT mounted (e.g., during instance creation)
	if resizeFilesystem {
		diskPath, err := s.getDSDiskPath(id)
		if err != nil {
			return err
		}

		if err := fsck(ctx, diskPath); err != nil {
			return err
		}

		if err := resize(ctx, diskPath, 0); err != nil {
			return err
		}
	}

	return nil
}
