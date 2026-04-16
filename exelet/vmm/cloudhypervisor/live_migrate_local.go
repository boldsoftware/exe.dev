package cloudhypervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LiveMigrateLocalResult contains metrics from a local live migration.
type LiveMigrateLocalResult struct {
	Downtime    time.Duration
	ColdRestart bool // true if migration failed and VM was cold-restarted
}

// LiveMigrateLocal migrates a running VM to a new cloud-hypervisor process
// on the same host via snapshot/restore. This enables in-place CH binary
// upgrades without a full cold restart; the VM is paused, not shut down.
//
// Downtime is dominated by the time to write guest memory to disk and read
// it back on restore, so it scales with VM RAM size and disk throughput.
// It is not zero; expect seconds to tens of seconds for multi-GB VMs.
//
// CH's native local=true socket migration (shared-memory FDs, no copy) was
// evaluated and rejected — keep this snapshot/restore path.
//
// The flow:
//  1. Deflate balloon so all guest memory is mapped
//  2. Pause the VM, then snapshot (writes state+memory to disk)
//  3. Stop the old CH process (releases TAP device and disk locks)
//  4. Restore from snapshot on a new CH process (opens TAP, resumes VM)
//  5. Clean up snapshot files
//
// On failure after the old process is stopped, the service-layer caller
// performs a cold restart.
func (v *VMM) LiveMigrateLocal(ctx context.Context, id string) (res *LiveMigrateLocalResult, err error) {
	// Detach from the caller's context: once we've paused the VM and stopped
	// the old CH process, there's no safe way to roll back partway through —
	// letting a disconnected caller cancel us would leave the VM half-migrated
	// and unreachable. Bound with a generous timeout instead since snapshot
	// I/O can take a while for large VMs. The service-layer caller is aware
	// of this and does not expect to be able to cancel an in-flight migration.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()

	snapshotDir := filepath.Join(v.getDataPath(id), "ch-snapshot-local-migrate")
	// Remove any leftover snapshot from a prior failed migration so stale
	// files don't interfere with this run.
	if err := os.RemoveAll(snapshotDir); err != nil {
		return nil, fmt.Errorf("clean stale snapshot dir: %w", err)
	}
	defer func() {
		// Only clean up on success. Keep snapshot files on failure so an
		// operator can inspect what was captured before the migration went
		// wrong. The next run's pre-clean above will remove them.
		if err == nil {
			os.RemoveAll(snapshotDir)
		} else {
			v.log.WarnContext(ctx, "live-migrate-local: keeping snapshot dir for debugging", "id", id, "path", snapshotDir, "error", err)
		}
	}()

	// Step 1: Deflate balloon so all memory regions are mapped. Soft-fail:
	// a VM without a balloon device, or a wedged balloon, shouldn't block a
	// CH binary upgrade. In the worst case the snapshot captures a few
	// unmapped pages that take a bit longer to restore.
	v.log.InfoContext(ctx, "live-migrate-local: deflating balloon", "id", id)
	if err := v.DeflateBalloon(ctx, id); err != nil {
		v.log.WarnContext(ctx, "live-migrate-local: deflate balloon failed, continuing anyway", "id", id, "error", err)
	}

	// Step 2: Pause the VM and snapshot it.
	v.log.InfoContext(ctx, "live-migrate-local: pausing VM", "id", id)
	downtimeStart := time.Now()
	if err := v.Pause(ctx, id); err != nil {
		return nil, fmt.Errorf("pause: %w", err)
	}

	v.log.InfoContext(ctx, "live-migrate-local: snapshotting VM", "id", id)
	if err := v.Snapshot(ctx, id, snapshotDir); err != nil {
		// Resume so the VM isn't left paused.
		if resumeErr := v.Resume(ctx, id); resumeErr != nil {
			v.log.ErrorContext(ctx, "live-migrate-local: failed to resume after snapshot error", "id", id, "error", resumeErr)
		}
		return nil, fmt.Errorf("snapshot: %w", err)
	}

	// Step 3: Stop the old CH process. This releases the TAP device and
	// disk locks so the new process can acquire them.
	v.log.InfoContext(ctx, "live-migrate-local: stopping old CH process", "id", id)
	if err := v.Stop(ctx, id); err != nil {
		return nil, fmt.Errorf("stop old CH: %w", err)
	}

	// Step 4: Restore from snapshot on a new CH process.
	v.log.InfoContext(ctx, "live-migrate-local: restoring VM on new CH process", "id", id)
	if err := v.RestoreFromSnapshot(ctx, id, snapshotDir); err != nil {
		return nil, fmt.Errorf("restore: %w", err)
	}

	downtime := time.Since(downtimeStart)
	v.log.InfoContext(ctx, "live-migrate-local: complete", "id", id, "downtime", downtime)

	return &LiveMigrateLocalResult{Downtime: downtime}, nil
}
