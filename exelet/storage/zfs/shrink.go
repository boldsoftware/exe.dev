//go:build linux

package zfs

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mistifyio/go-zfs/v3"
)

const (
	// minVolumeSize is the minimum volume size that will be used
	// this is to account for very small images (e.g. busybox) that
	// have high compression
	minVolumeSize = 256 * 1024 * 1024
)

// Expand resizes the specified filesystem to the desired size
func (s *ZFS) Shrink(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dsName := s.getDSName(id)
	ds, err := zfs.GetDataset(dsName)
	if err != nil {
		return err
	}

	// check that the requested size is larger than the current volsize
	usedSize := ds.Logicalused

	// add 15% overhead to be safe
	volSize := uint64(float64(usedSize) * 1.15)

	// check if below minimum vol size and adjust for compression
	// this is to account for very small images with
	// high compression
	if volSize < minVolumeSize {
		// get compression ratio
		cz, err := ds.GetProperty("compressratio")
		if err != nil {
			return err
		}
		compressRatio, err := strconv.ParseFloat(strings.TrimSuffix(cz, "x"), 64)
		if err != nil {
			return err
		}
		// add compressed + 10% overhead and check if still under
		// minimum and bump to min to account for the edge case tiny images (e.g. busybox)
		volSize = max(uint64(float64(usedSize)*(compressRatio+0.10)), minVolumeSize)
		s.log.DebugContext(ctx, "compressed minimum size", "size", usedSize, "compression", cz, "volSize", volSize)
	}

	// zfs needs to be 4K aligned
	newSize := align4K(volSize)

	diskPath, err := s.getDSDiskPath(id)
	if err != nil {
		return err
	}
	s.log.DebugContext(ctx, "shrinking volume", "id", id, "used", usedSize, "size", newSize)
	// for shrink:
	// - fsck disk
	// - resize filesystem to minimum size
	// - update zvol size to min used + 10%
	// - resize filesystem to remainder of disk
	if err := fsck(ctx, diskPath); err != nil {
		return err
	}

	if err := resizeToMin(ctx, diskPath); err != nil {
		return err
	}

	// update vol size
	if err := ds.SetProperty("volsize", fmt.Sprintf("%d", newSize)); err != nil {
		return err
	}

	if err := fsck(ctx, diskPath); err != nil {
		return err
	}

	// final resize
	if err := resize(ctx, diskPath, 0); err != nil {
		return err
	}

	return nil
}
