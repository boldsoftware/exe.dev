//go:build linux

package zfs

import (
	"context"
	"fmt"

	"github.com/dustin/go-humanize"
	"github.com/mistifyio/go-zfs/v3"
)

// Expand resizes the specified filesystem to the desired size
func (s *ZFS) Expand(ctx context.Context, id string, size uint64) error {
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

	// zfs needs to be 16K aligned
	newSize := align16K(size)

	diskPath, err := s.getDSDiskPath(id)
	if err != nil {
		return err
	}
	s.log.DebugContext(ctx, "expanding volume", "id", id, "size", newSize)
	// for expand:
	// - update zvol size
	// - fsck disk
	// - resize filesystem
	if err := ds.SetProperty("volsize", fmt.Sprintf("%d", newSize)); err != nil {
		return err
	}

	// TODO: inspect the actual disk to get the filesystem to perform the correct resizing
	// for now we only support ext4 so this is fine.

	// resize filesystem
	// fsck
	if err := fsck(ctx, diskPath); err != nil {
		return err
	}

	// resize
	if err := resize(ctx, diskPath, 0); err != nil {
		return err
	}

	return nil
}
