//go:build linux

package zfs

import (
	"context"
	"fmt"
	"os/exec"
	"time"

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
	t0 := time.Now()
	if err := ds.SetProperty("volsize", fmt.Sprintf("%d", newSize)); err != nil {
		return err
	}
	setVolsizeDur := time.Since(t0)

	// Wait for udev to process the device size change
	t1 := time.Now()
	if err := exec.Command("udevadm", "settle").Run(); err != nil {
		return fmt.Errorf("udevadm settle failed: %w", err)
	}
	udevDur := time.Since(t1)

	var resizeDur time.Duration
	var fsckDur time.Duration
	var usedFallback bool

	// If resizeFilesystem is true, expand the ext4 filesystem to fill the new volume size.
	// This should be done when the filesystem is NOT mounted (e.g., during instance creation).
	if resizeFilesystem {
		diskPath, err := s.getDSDiskPath(id)
		if err != nil {
			return err
		}

		// Try resize2fs directly first — it's fast because it only updates metadata
		// at the end of the filesystem. On a cleanly cloned volume this always works.
		// If the superblock has the errors-detected flag, resize2fs will refuse and
		// we fall back to fsck + resize2fs.
		t2 := time.Now()
		if err := resize(ctx, diskPath, 0); err != nil {
			usedFallback = true
			s.log.DebugContext(ctx, "resize2fs failed, falling back to fsck+resize", "id", id, "error", err)

			t3 := time.Now()
			if err := fsck(ctx, diskPath); err != nil {
				return err
			}
			fsckDur = time.Since(t3)

			t4 := time.Now()
			if err := resize(ctx, diskPath, 0); err != nil {
				return err
			}
			resizeDur = time.Since(t4)
		} else {
			resizeDur = time.Since(t2)
		}
	}

	attrs := []any{
		"id", id,
		"set_volsize_ms", setVolsizeDur.Milliseconds(),
		"udev_settle_ms", udevDur.Milliseconds(),
	}
	if resizeFilesystem {
		attrs = append(attrs,
			"resize_ms", resizeDur.Milliseconds(),
			"fsck_fallback", usedFallback,
		)
		if usedFallback {
			attrs = append(attrs, "fsck_ms", fsckDur.Milliseconds())
		}
	}
	total := setVolsizeDur + udevDur + resizeDur + fsckDur
	attrs = append(attrs, "total_ms", total.Milliseconds())
	s.log.InfoContext(ctx, "expand complete", attrs...)

	return nil
}
