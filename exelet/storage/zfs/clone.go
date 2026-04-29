//go:build linux

package zfs

import (
	"context"
	"time"

	"github.com/mistifyio/go-zfs/v3"
)

// Clone clones the source filesystem to the destination
func (s *ZFS) Clone(ctx context.Context, srcID, destID string) error {
	unlock := s.lockVolumes(srcID, destID)
	defer unlock()

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
	t0 := time.Now()
	cloneDS, err := srcDS.Snapshot(snapName, false)
	if err != nil {
		return err
	}
	snapshotDur := time.Since(t0)

	t1 := time.Now()
	if _, err := cloneDS.Clone(destDSName, nil); err != nil {
		return err
	}
	cloneDur := time.Since(t1)

	// wait until volume is ready
	t2 := time.Now()
	if err := s.waitForZvol(destID); err != nil {
		return err
	}
	waitDur := time.Since(t2)

	s.log.InfoContext(ctx, "clone complete",
		"src", srcID, "dest", destID,
		"snapshot_ms", snapshotDur.Milliseconds(),
		"clone_ms", cloneDur.Milliseconds(),
		"wait_zvol_ms", waitDur.Milliseconds(),
		"total_ms", (snapshotDur + cloneDur + waitDur).Milliseconds(),
	)

	return nil
}
