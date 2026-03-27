package compute

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"golang.org/x/sys/unix"

	"exe.dev/backoff"
	"exe.dev/exelet/storage"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// TierMigrationOp tracks an in-progress tier migration.
type TierMigrationOp struct {
	OperationID string
	InstanceID  string
	SourcePool  string
	TargetPool  string
	State       string  // "pending", "migrating", "completed", "failed", "cancelled"
	Progress    float32 // 0.0 to 1.0
	Error       string
	StartedAt   time.Time
	CompletedAt time.Time

	cancel context.CancelFunc // cancels the migration context
	mu     sync.Mutex
}

func (op *TierMigrationOp) setProgress(p float32) {
	op.mu.Lock()
	op.Progress = p
	op.mu.Unlock()
}

// progressReader wraps an io.Reader and reports byte-level progress as a
// fraction between progressMin and progressMax. totalBytes is the estimated
// stream size; if zero, no intermediate updates are emitted.
type progressReader struct {
	r           io.Reader
	read        int64
	totalBytes  int64
	progressMin float32
	progressMax float32
	op          *TierMigrationOp
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	if n > 0 && pr.totalBytes > 0 {
		pr.read += int64(n)
		frac := float64(pr.read) / float64(pr.totalBytes)
		if frac > 1 {
			frac = 1
		}
		progress := pr.progressMin + float32(frac)*float32(pr.progressMax-pr.progressMin)
		pr.op.setProgress(progress)
	}
	return n, err
}

func (op *TierMigrationOp) complete(err error) {
	op.mu.Lock()
	op.CompletedAt = time.Now()
	if err != nil {
		op.State = "failed"
		op.Error = err.Error()
	} else {
		op.State = "completed"
		op.Progress = 1.0
	}
	op.mu.Unlock()
}

func (op *TierMigrationOp) toProto() *api.TierMigrationOperation {
	op.mu.Lock()
	defer op.mu.Unlock()
	return &api.TierMigrationOperation{
		OperationID: op.OperationID,
		InstanceID:  op.InstanceID,
		SourcePool:  op.SourcePool,
		TargetPool:  op.TargetPool,
		State:       op.State,
		Progress:    op.Progress,
		Error:       op.Error,
		StartedAt:   op.StartedAt.Unix(),
		CompletedAt: op.CompletedAt.Unix(),
	}
}

const (
	// tierMigrationCircuitBreakerThreshold is the number of failures within
	// tierMigrationCircuitBreakerWindow that trips the circuit breaker,
	// disabling all tier migrations until the exelet is restarted.
	tierMigrationCircuitBreakerThreshold = 3
	tierMigrationCircuitBreakerWindow    = 10 * time.Minute
)

// tierMigrationOps tracks active tier migration operations.
// Completed/failed ops are removed after a short TTL.
var (
	tierMigrationMu  sync.Mutex
	tierMigrationOps = make(map[string]*TierMigrationOp) // opID -> op
)

func addTierMigrationOp(op *TierMigrationOp) {
	tierMigrationMu.Lock()
	tierMigrationOps[op.OperationID] = op
	tierMigrationMu.Unlock()
}

func removeTierMigrationOp(opID string) {
	tierMigrationMu.Lock()
	delete(tierMigrationOps, opID)
	tierMigrationMu.Unlock()
}

// recordMigrationFailure records a migration failure timestamp and trips the
// circuit breaker if the threshold is exceeded within the window.
func (s *Service) recordMigrationFailure() {
	s.tierMigrationMu.Lock()
	defer s.tierMigrationMu.Unlock()

	now := time.Now()
	cutoff := now.Add(-tierMigrationCircuitBreakerWindow)

	// Prune old failures outside the window
	valid := s.tierMigrationFailures[:0]
	for _, t := range s.tierMigrationFailures {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	valid = append(valid, now)
	s.tierMigrationFailures = valid

	if len(valid) >= tierMigrationCircuitBreakerThreshold {
		s.tierMigrationDisabled = true
		s.log.ErrorContext(context.Background(),
			"tier migration circuit breaker tripped: too many failures, migrations disabled until restart",
			"failures", len(valid),
			"window", tierMigrationCircuitBreakerWindow,
		)
	}
}

// checkMigrationCircuitBreaker returns an error if migrations are disabled.
func (s *Service) checkMigrationCircuitBreaker() error {
	s.tierMigrationMu.Lock()
	defer s.tierMigrationMu.Unlock()
	if s.tierMigrationDisabled {
		return fmt.Errorf("tier migrations disabled: circuit breaker tripped after %d failures within %s (restart exelet to re-enable)",
			tierMigrationCircuitBreakerThreshold, tierMigrationCircuitBreakerWindow)
	}
	return nil
}

// CancelTierMigration cancels a pending or in-progress tier migration.
func (s *Service) CancelTierMigration(ctx context.Context, req *api.CancelTierMigrationRequest) (*api.CancelTierMigrationResponse, error) {
	if req.OperationID == "" {
		return nil, status.Error(codes.InvalidArgument, "operation_id is required")
	}

	tierMigrationMu.Lock()
	op, ok := tierMigrationOps[req.OperationID]
	tierMigrationMu.Unlock()

	if !ok {
		return nil, status.Errorf(codes.NotFound, "operation %s not found", req.OperationID)
	}

	op.mu.Lock()
	state := op.State
	cancelFn := op.cancel
	op.mu.Unlock()

	if state == "completed" || state == "failed" || state == "cancelled" {
		return &api.CancelTierMigrationResponse{
			OperationID: req.OperationID,
			State:       state,
		}, nil
	}

	if cancelFn != nil {
		cancelFn()
	}

	// Wait briefly for the goroutine to acknowledge cancellation
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		op.mu.Lock()
		state = op.State
		op.mu.Unlock()
		if state == "cancelled" || state == "completed" || state == "failed" {
			break
		}
	}

	s.log.InfoContext(ctx, "tier migration cancel requested", "op", req.OperationID, "state", state)

	return &api.CancelTierMigrationResponse{
		OperationID: req.OperationID,
		State:       state,
	}, nil
}

// ClearTierMigrations removes completed and failed tier migration operations.
func (s *Service) ClearTierMigrations(ctx context.Context, req *api.ClearTierMigrationsRequest) (*api.ClearTierMigrationsResponse, error) {
	tierMigrationMu.Lock()
	var cleared uint32
	for id, op := range tierMigrationOps {
		op.mu.Lock()
		state := op.State
		op.mu.Unlock()
		if state == "completed" || state == "failed" || state == "cancelled" {
			delete(tierMigrationOps, id)
			cleared++
		}
	}
	tierMigrationMu.Unlock()

	return &api.ClearTierMigrationsResponse{Cleared: cleared}, nil
}

// replicationTargetPool returns the ZFS pool name that the storage replication
// target points to, or "" if replication is not configured or targets a remote host.
// This pool is reserved for backups and must not be used as a tier migration target.
func (s *Service) replicationTargetPool() string {
	target := s.config.ReplicationTarget
	if !strings.HasPrefix(target, "zpool:///") {
		return ""
	}
	pool := strings.TrimPrefix(target, "zpool:///")
	if idx := strings.IndexByte(pool, '?'); idx >= 0 {
		pool = pool[:idx]
	}
	return pool
}

// MigrateStorageTier kicks off an async tier migration and returns immediately.
func (s *Service) MigrateStorageTier(ctx context.Context, req *api.MigrateStorageTierRequest) (*api.MigrateStorageTierResponse, error) {
	if err := s.checkMigrationCircuitBreaker(); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}

	if req.InstanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id is required")
	}
	if req.TargetPool == "" {
		return nil, status.Error(codes.InvalidArgument, "target_pool is required")
	}

	tiered, ok := s.context.StorageManager.(*storage.TieredStorageManager)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "storage tiers not configured")
	}

	// Validate target pool exists
	if _, err := tiered.Pool(req.TargetPool); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "target pool: %v", err)
	}

	// Reject migration to the replication target (backup) pool
	if bp := s.replicationTargetPool(); bp != "" && req.TargetPool == bp {
		return nil, status.Errorf(codes.InvalidArgument, "pool %q is the storage replication target and reserved for backups", req.TargetPool)
	}

	// Resolve source pool
	sourcePool, _, err := tiered.PoolForInstance(ctx, req.InstanceID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "instance not found on any pool: %v", err)
	}

	if sourcePool == req.TargetPool {
		return nil, status.Errorf(codes.InvalidArgument, "instance %s is already on pool %s", req.InstanceID, req.TargetPool)
	}

	// Check if the target pool already has a dataset for this instance
	// (e.g. leftover from a previously failed migration). Migrating into
	// an existing dataset would fail or corrupt data.
	targetSM, _ := tiered.Pool(req.TargetPool)
	if _, err := targetSM.Get(ctx, req.InstanceID); err == nil {
		return nil, status.Errorf(codes.AlreadyExists,
			"instance %s already has a dataset on target pool %s; delete it before retrying",
			req.InstanceID, req.TargetPool)
	}

	opID, err := uuid.NewV7()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate operation ID: %v", err)
	}

	// Use first 24 hex chars for a shorter but still unique operation ID.
	// With <4096 concurrent ops the collision probability is negligible.
	shortID := fmt.Sprintf("%x", opID[:12])

	op := &TierMigrationOp{
		OperationID: shortID,
		InstanceID:  req.InstanceID,
		SourcePool:  sourcePool,
		TargetPool:  req.TargetPool,
		State:       "pending",
		StartedAt:   time.Now(),
	}
	addTierMigrationOp(op)

	// Run migration in background, gated by worker semaphore
	migCtx, migCancel := context.WithCancel(context.Background())
	op.mu.Lock()
	op.cancel = migCancel
	op.mu.Unlock()

	go func() {
		defer migCancel()

		// Acquire semaphore slot (blocks if all workers are busy).
		// Check for cancellation while waiting.
		select {
		case s.tierMigrationSem <- struct{}{}:
		case <-migCtx.Done():
			op.mu.Lock()
			op.State = "cancelled"
			op.CompletedAt = time.Now()
			op.Error = "cancelled while pending"
			op.mu.Unlock()
			s.log.InfoContext(context.Background(), "tier migration cancelled while pending",
				"op", op.OperationID, "instance", req.InstanceID)
			time.AfterFunc(5*time.Minute, func() { removeTierMigrationOp(op.OperationID) })
			return
		}
		defer func() { <-s.tierMigrationSem }()

		op.mu.Lock()
		if migCtx.Err() != nil {
			op.State = "cancelled"
			op.CompletedAt = time.Now()
			op.Error = "cancelled before start"
			op.mu.Unlock()
			time.AfterFunc(5*time.Minute, func() { removeTierMigrationOp(op.OperationID) })
			return
		}
		op.State = "migrating"
		op.mu.Unlock()

		var migErr error
		if req.Live {
			migErr = s.migrateTierLive(migCtx, tiered, req.InstanceID, sourcePool, req.TargetPool, op)
		} else {
			migErr = s.migrateTierStopped(migCtx, tiered, req.InstanceID, sourcePool, req.TargetPool, op)
		}

		// If the context was cancelled, mark as cancelled rather than failed
		if migErr != nil && migCtx.Err() != nil {
			op.mu.Lock()
			op.State = "cancelled"
			op.CompletedAt = time.Now()
			op.Error = "cancelled"
			op.mu.Unlock()
			s.log.InfoContext(context.Background(), "tier migration cancelled",
				"op", op.OperationID, "instance", req.InstanceID)
			time.AfterFunc(5*time.Minute, func() { removeTierMigrationOp(op.OperationID) })
			return
		}
		op.complete(migErr)

		if migErr != nil {
			s.recordMigrationFailure()
			s.log.ErrorContext(context.Background(), "tier migration failed",
				"op", op.OperationID, "instance", req.InstanceID, "error", migErr)
		} else {
			s.log.InfoContext(context.Background(), "tier migration completed",
				"op", op.OperationID, "instance", req.InstanceID,
				"from", sourcePool, "to", req.TargetPool)
		}

		// Remove completed ops after 5 minutes
		time.AfterFunc(5*time.Minute, func() {
			removeTierMigrationOp(op.OperationID)
		})
	}()

	return &api.MigrateStorageTierResponse{
		OperationID: op.OperationID,
		InstanceID:  req.InstanceID,
		SourcePool:  sourcePool,
		TargetPool:  req.TargetPool,
	}, nil
}

// GetTierMigrationStatus returns all tracked tier migration operations, newest first.
func (s *Service) GetTierMigrationStatus(ctx context.Context, req *api.GetTierMigrationStatusRequest) (*api.GetTierMigrationStatusResponse, error) {
	tierMigrationMu.Lock()
	ops := make([]*api.TierMigrationOperation, 0, len(tierMigrationOps))
	for _, op := range tierMigrationOps {
		ops = append(ops, op.toProto())
	}
	tierMigrationMu.Unlock()

	sort.Slice(ops, func(i, j int) bool {
		return ops[i].StartedAt > ops[j].StartedAt
	})

	return &api.GetTierMigrationStatusResponse{
		Operations: ops,
	}, nil
}

// migrateTierStopped migrates a stopped VM's storage between pools.
func (s *Service) migrateTierStopped(ctx context.Context, tiered *storage.TieredStorageManager, instanceID, sourcePool, targetPool string, op *TierMigrationOp) error {
	// Lock for migration
	if err := s.lockForMigration(instanceID); err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer s.unlockMigration(instanceID)

	// Verify instance is stopped
	instance, err := s.getInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("get instance: %w", err)
	}
	if instance.State != api.VMState_STOPPED {
		return fmt.Errorf("instance must be stopped for non-live tier migration, current state: %s", instance.State)
	}

	// Suspend replication
	if rs := s.context.ReplicationSuspender; rs != nil {
		rs.SuspendVolume(instanceID)
		defer rs.ResumeVolume(instanceID)
		rs.WaitVolumeIdle(ctx, instanceID)
	}

	srcManager, err := tiered.Pool(sourcePool)
	if err != nil {
		return fmt.Errorf("source pool: %w", err)
	}
	dstManager, err := tiered.Pool(targetPool)
	if err != nil {
		return fmt.Errorf("target pool: %w", err)
	}

	op.setProgress(0.1)

	// Sync filesystem to flush in-flight writes before snapshotting
	unix.Sync()

	// Create migration snapshot on source
	snapName, cleanup, err := srcManager.CreateMigrationSnapshot(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	defer cleanup()

	op.setProgress(0.2)

	// Get filesystem size for progress estimation
	var estimatedBytes int64
	if fs, err := srcManager.Get(ctx, instanceID); err == nil && fs.Size > 0 {
		estimatedBytes = int64(fs.Size)
	}

	// Full ZFS send/recv locally (pipe, no gRPC)
	reader, err := srcManager.SendSnapshot(ctx, snapName, false, "")
	if err != nil {
		return fmt.Errorf("send snapshot: %w", err)
	}

	pr := &progressReader{r: reader, totalBytes: estimatedBytes, progressMin: 0.2, progressMax: 0.7, op: op}
	if err := dstManager.ReceiveSnapshot(ctx, instanceID, pr); err != nil {
		reader.Close()
		return fmt.Errorf("receive snapshot: %w", err)
	}
	if err := reader.Close(); err != nil {
		return fmt.Errorf("close send stream: %w", err)
	}

	op.setProgress(0.7)

	// From this point, the target pool has a received dataset. Clean it up
	// on any error so it doesn't block retries or cause split-brain.
	targetRecvd := true
	defer func() {
		if targetRecvd {
			s.log.WarnContext(ctx, "tier migration: cleaning up target dataset after failure",
				"instance", instanceID, "pool", targetPool)
			if delErr := dstManager.Delete(ctx, instanceID); delErr != nil {
				s.log.ErrorContext(ctx, "tier migration: failed to delete target dataset",
					"instance", instanceID, "pool", targetPool, "error", delErr)
			}
		}
	}()

	// Copy encryption key if present
	if key, err := srcManager.GetEncryptionKey(instanceID); err == nil && key != nil {
		if err := dstManager.SetEncryptionKey(instanceID, key); err != nil {
			return fmt.Errorf("set encryption key: %w", err)
		}
	}

	// Get new disk path from target pool
	dstFS, err := dstManager.Get(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("get target filesystem: %w", err)
	}

	// Update instance config with new RootDiskPath
	iCfg, err := s.loadInstanceConfig(instanceID)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	iCfg.VMConfig.RootDiskPath = dstFS.Path
	iCfg.UpdatedAt = time.Now().UnixNano()
	if err := s.saveInstanceConfig(iCfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	op.setProgress(0.8)

	// Delete source dataset — this must succeed to prevent split-brain where
	// both pools hold a copy and future PoolForInstance resolves the stale one.
	if err := srcManager.Delete(ctx, instanceID); err != nil {
		// Source delete failed but config already points to target — don't
		// delete the target dataset on return.
		targetRecvd = false
		return fmt.Errorf("delete source dataset on pool %s: %w (migration partially complete, target has data)", sourcePool, err)
	}
	targetRecvd = false // Migration succeeded — keep the target dataset

	op.setProgress(1.0)
	return nil
}

// migrateTierLive migrates a running VM's storage between pools with near-zero downtime.
func (s *Service) migrateTierLive(ctx context.Context, tiered *storage.TieredStorageManager, instanceID, sourcePool, targetPool string, op *TierMigrationOp) error {
	// Lock for migration
	if err := s.lockForMigration(instanceID); err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer s.unlockMigration(instanceID)

	// Verify instance is running
	instance, err := s.getInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("get instance: %w", err)
	}
	if instance.State == api.VMState_STOPPED {
		// Fall back to stopped migration
		s.log.InfoContext(ctx, "tier migration: VM already stopped, using cold path", "instance", instanceID)
		s.unlockMigration(instanceID) // unlock since migrateTierStopped will re-lock
		return s.migrateTierStopped(ctx, tiered, instanceID, sourcePool, targetPool, op)
	}
	if instance.State != api.VMState_RUNNING {
		return fmt.Errorf("instance must be running for live tier migration, current state: %s", instance.State)
	}

	// Suspend replication
	if rs := s.context.ReplicationSuspender; rs != nil {
		rs.SuspendVolume(instanceID)
		defer rs.ResumeVolume(instanceID)
		rs.WaitVolumeIdle(ctx, instanceID)
	}

	srcManager, err := tiered.Pool(sourcePool)
	if err != nil {
		return fmt.Errorf("source pool: %w", err)
	}
	dstManager, err := tiered.Pool(targetPool)
	if err != nil {
		return fmt.Errorf("target pool: %w", err)
	}

	op.setProgress(0.05)

	// Phase 1: Pre-copy snapshot while VM is running
	dsName := srcManager.GetDatasetName(instanceID)
	preSnapName := dsName + "@migration-pre"

	// Clean up any leftover pre-snapshot
	srcManager.DestroySnapshot(ctx, preSnapName) //nolint:errcheck

	// Sync before pre-copy snapshot to capture as much data as possible
	unix.Sync()

	if err := srcManager.CreateSnapshot(ctx, preSnapName); err != nil {
		return fmt.Errorf("create pre-copy snapshot: %w", err)
	}
	preSnapCleaned := false
	defer func() {
		if !preSnapCleaned {
			srcManager.DestroySnapshot(ctx, preSnapName) //nolint:errcheck
		}
	}()

	op.setProgress(0.1)

	// Get filesystem size for progress estimation
	var estimatedBytes int64
	if fs, err := srcManager.Get(ctx, instanceID); err == nil && fs.Size > 0 {
		estimatedBytes = int64(fs.Size)
	}

	// Full send of pre-copy snapshot
	reader, err := srcManager.SendSnapshot(ctx, preSnapName, false, "")
	if err != nil {
		return fmt.Errorf("send pre-copy: %w", err)
	}
	preCopyPR := &progressReader{r: reader, totalBytes: estimatedBytes, progressMin: 0.1, progressMax: 0.4, op: op}
	if err := dstManager.ReceiveSnapshot(ctx, instanceID, preCopyPR); err != nil {
		reader.Close()
		return fmt.Errorf("receive pre-copy: %w", err)
	}
	if err := reader.Close(); err != nil {
		return fmt.Errorf("send pre-copy: %w", err)
	}

	op.setProgress(0.4)

	// Pause VM — start of downtime
	s.log.InfoContext(ctx, "tier migration: pausing VM", "instance", instanceID)
	vmPaused := true
	defer func() {
		if vmPaused {
			// Use a fresh context for cleanup — the caller's context may be
			// cancelled (e.g. operator cancel), but we must still resume the VM.
			cleanupCtx := context.Background()
			s.log.WarnContext(cleanupCtx, "tier migration: resuming VM due to error", "instance", instanceID)
			if err := s.vmm.Resume(cleanupCtx, instanceID); err != nil {
				s.log.ErrorContext(cleanupCtx, "tier migration: failed to resume VM", "instance", instanceID, "error", err)
				return
			}
			// After resume, check if the guest kernel is healthy.
			// A failed CH snapshot can leave vCPUs in a bad state that
			// causes RCU stalls, making the VM appear running but
			// unresponsive. Detect this and escalate to stop/start.
			s.checkAndRecoverStuckVM(cleanupCtx, instanceID)
		}
	}()
	if err := s.vmm.Pause(ctx, instanceID); err != nil {
		return fmt.Errorf("pause VM: %w", err)
	}

	op.setProgress(0.5)

	// Phase 2: Incremental send from pre-copy to migration snapshot
	// Sync after pause to flush any remaining in-flight writes
	unix.Sync()

	migrationSnap, cleanup, err := srcManager.CreateMigrationSnapshot(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("create migration snapshot: %w", err)
	}
	defer cleanup()

	reader, err = srcManager.SendSnapshot(ctx, migrationSnap, true, preSnapName)
	if err != nil {
		return fmt.Errorf("send incremental: %w", err)
	}
	if err := dstManager.ReceiveSnapshot(ctx, instanceID, reader); err != nil {
		reader.Close()
		return fmt.Errorf("receive incremental: %w", err)
	}
	if err := reader.Close(); err != nil {
		return fmt.Errorf("send incremental: %w", err)
	}

	op.setProgress(0.6)

	// From this point, the target pool has a received dataset. Clean it up
	// on any error so it doesn't block retries or cause split-brain.
	targetRecvd := true
	defer func() {
		if targetRecvd {
			s.log.WarnContext(ctx, "tier migration: cleaning up target dataset after failure",
				"instance", instanceID, "pool", targetPool)
			if delErr := dstManager.Delete(ctx, instanceID); delErr != nil {
				s.log.ErrorContext(ctx, "tier migration: failed to delete target dataset",
					"instance", instanceID, "pool", targetPool, "error", delErr)
			}
		}
	}()

	// Copy encryption key if present
	if key, err := srcManager.GetEncryptionKey(instanceID); err == nil && key != nil {
		if err := dstManager.SetEncryptionKey(instanceID, key); err != nil {
			return fmt.Errorf("set encryption key: %w", err)
		}
	}

	// CH snapshot for process state
	instanceDir := s.getInstanceDir(instanceID)
	snapshotDir := filepath.Join(instanceDir, "ch-snapshot-tier")
	defer os.RemoveAll(snapshotDir)

	s.log.InfoContext(ctx, "tier migration: creating CH snapshot", "instance", instanceID)
	if err := s.vmm.Snapshot(ctx, instanceID, snapshotDir); err != nil {
		return fmt.Errorf("CH snapshot: %w", err)
	}

	op.setProgress(0.7)

	// Get new disk path from target pool
	dstFS, err := dstManager.Load(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("load target filesystem: %w", err)
	}

	// Edit snapshot config: update disk path, keep same IP (pass nil for targetNetwork)
	kernelPath := filepath.Join(instanceDir, kernelName)
	if err := editSnapshotConfig(snapshotDir, dstFS.Path, kernelPath, instance.VMConfig, nil); err != nil {
		return fmt.Errorf("edit snapshot config: %w", err)
	}

	op.setProgress(0.8)

	// Stop old CH process
	if err := s.vmm.Stop(ctx, instanceID); err != nil {
		return fmt.Errorf("stop old VM: %w", err)
	}
	vmPaused = false // Don't try to resume after stop

	// Past the point of no return — the VM is stopped. All subsequent
	// operations (restore, recovery cold-boot) must use a background
	// context so that operator cancellation cannot strand the VM.
	ctx = context.Background()

	// Restore from snapshot with new disk path
	s.log.InfoContext(ctx, "tier migration: restoring VM", "instance", instanceID)
	if err := s.vmm.RestoreFromSnapshot(ctx, instanceID, snapshotDir); err != nil {
		// Restore failed — VM is down. Try to cold-boot from the source disk
		// so the instance isn't left hard-downed until manual intervention.
		s.log.ErrorContext(ctx, "tier migration: restore failed, cold-booting from source disk",
			"instance", instanceID, "error", err)

		// Clean up the failed CH process
		if stopErr := s.vmm.Stop(ctx, instanceID); stopErr != nil {
			s.log.WarnContext(ctx, "tier migration: failed to stop failed CH process", "instance", instanceID, "error", stopErr)
		}

		// Target dataset cleanup is handled by the targetRecvd defer.

		// Ensure instance config still points to source disk
		if recoverCfg, loadErr := s.loadInstanceConfig(instanceID); loadErr == nil {
			srcFS, srcErr := srcManager.Load(ctx, instanceID)
			if srcErr == nil {
				recoverCfg.VMConfig.RootDiskPath = srcFS.Path
				recoverCfg.State = api.VMState_STOPPED
				s.saveInstanceConfig(recoverCfg) //nolint:errcheck
			}
		}

		// Attempt cold boot from source — unlock migration first since startInstance
		// acquires its own locks
		s.unlockMigration(instanceID)
		startErr := s.startInstance(ctx, instanceID)
		// Re-lock so the deferred unlockMigration doesn't double-delete
		s.lockForMigration(instanceID) //nolint:errcheck

		if startErr != nil {
			return fmt.Errorf("restore from snapshot failed (%w) and cold boot recovery also failed: %v", err, startErr)
		}
		s.log.WarnContext(ctx, "tier migration: cold-booted VM from source disk after restore failure",
			"instance", instanceID)
		return fmt.Errorf("restore from snapshot failed (VM recovered via cold boot on source): %w", err)
	}

	op.setProgress(0.9)

	// Update instance config
	iCfg, err := s.loadInstanceConfig(instanceID)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	iCfg.VMConfig.RootDiskPath = dstFS.Path
	iCfg.UpdatedAt = time.Now().UnixNano()
	if err := s.saveInstanceConfig(iCfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	// Delete source dataset — this must succeed to prevent split-brain where
	// both pools hold a copy and future PoolForInstance resolves the stale one.
	if err := srcManager.Delete(ctx, instanceID); err != nil {
		// Source delete failed but VM is running from target — don't delete
		// the target dataset on return.
		targetRecvd = false
		return fmt.Errorf("delete source dataset on pool %s: %w (migration partially complete, target has data)", sourcePool, err)
	}
	targetRecvd = false // Migration succeeded — keep the target dataset

	// Clean up pre-snapshot (best-effort since source dataset is gone)
	if err := srcManager.DestroySnapshot(ctx, preSnapName); err != nil {
		s.log.WarnContext(ctx, "tier migration: failed to destroy pre-copy snapshot",
			"snapshot", preSnapName, "error", err)
	}
	preSnapCleaned = true

	op.setProgress(1.0)
	return nil
}

// checkAndRecoverStuckVM probes the SSH proxy with exponential backoff to
// verify the guest is actually responsive after a resume. A failed CH
// snapshot can leave vCPUs in a bad state (RCU stalls, etc.) where the VM
// appears running but is unresponsive. If all probes fail within the
// timeout, escalates to stop/start.
func (s *Service) checkAndRecoverStuckVM(ctx context.Context, instanceID string) {
	proxyPort, ok := s.proxyManager.GetPort(ctx, instanceID)
	if !ok {
		s.log.WarnContext(ctx, "tier migration: no proxy port for VM, skipping health check",
			"instance", instanceID)
		return
	}

	addr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	probeCtx, probeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer probeCancel()

	attempt := 0
	for range backoff.Loop(probeCtx, 5*time.Second) {
		attempt++

		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err == nil {
			conn.SetReadDeadline(time.Now().Add(3 * time.Second)) //nolint:errcheck
			buf := make([]byte, 256)
			n, readErr := conn.Read(buf)
			conn.Close()
			if readErr == nil && n > 0 && strings.HasPrefix(string(buf[:n]), "SSH-") {
				// SSH is responding — VM is healthy
				return
			}
			s.log.WarnContext(ctx, "tier migration: proxy connected but no SSH banner",
				"instance", instanceID, "attempt", attempt, "read_bytes", n, "error", readErr)
		} else {
			s.log.WarnContext(ctx, "tier migration: proxy connection failed after resume",
				"instance", instanceID, "attempt", attempt, "addr", addr, "error", err)
		}
	}

	s.log.ErrorContext(ctx, "tier migration: VM unresponsive after resume, performing stop/start recovery",
		"instance", instanceID)

	if err := s.vmm.Stop(ctx, instanceID); err != nil {
		s.log.ErrorContext(ctx, "tier migration: failed to stop stuck VM for recovery",
			"instance", instanceID, "error", err)
		return
	}

	if err := s.startInstance(ctx, instanceID); err != nil {
		s.log.ErrorContext(ctx, "tier migration: failed to start VM after stuck recovery",
			"instance", instanceID, "error", err)
	} else {
		s.log.InfoContext(ctx, "tier migration: VM recovered via stop/start after stuck resume",
			"instance", instanceID)
	}
}

// ListStorageTiers returns all configured storage tiers and their capacity/usage.
func (s *Service) ListStorageTiers(ctx context.Context, req *api.ListStorageTiersRequest) (*api.ListStorageTiersResponse, error) {
	tiered, ok := s.context.StorageManager.(*storage.TieredStorageManager)
	if !ok {
		// Single pool — report it as the only tier
		poolName := "default"
		tier, err := s.getPoolInfo(ctx, poolName, true, s.context.StorageManager)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get pool info: %v", err)
		}
		return &api.ListStorageTiersResponse{Tiers: []*api.StorageTier{tier}}, nil
	}

	var tiers []*api.StorageTier
	primaryName := tiered.PoolNames()[0]
	for _, name := range tiered.PoolNames() {
		sm, _ := tiered.Pool(name)
		tier, err := s.getPoolInfo(ctx, name, name == primaryName, sm)
		if err != nil {
			s.log.WarnContext(ctx, "failed to get pool info", "pool", name, "error", err)
			continue
		}
		md := tiered.PoolMetadata(name)
		if bp := s.replicationTargetPool(); bp != "" && name == bp {
			if md == nil {
				md = make(map[string]string)
			}
			md["role"] = "backup"
		}
		tier.Metadata = md
		tiers = append(tiers, tier)
	}

	return &api.ListStorageTiersResponse{Tiers: tiers}, nil
}

// getPoolInfo queries ZFS for pool capacity and counts instances.
func (s *Service) getPoolInfo(ctx context.Context, poolName string, primary bool, sm storage.StorageManager) (*api.StorageTier, error) {
	tier := &api.StorageTier{
		Name:    poolName,
		Primary: primary,
	}

	// Count VM instances on this pool (datasets with "vm" prefix)
	datasets, err := sm.ListDatasets(ctx)
	if err == nil {
		var count uint32
		for _, ds := range datasets {
			if strings.HasPrefix(ds, "vm") {
				count++
			}
		}
		tier.InstanceCount = count
	}

	// Get pool capacity via zpool get
	size, used, avail, err := getZpoolCapacity(ctx, poolName)
	if err != nil {
		s.log.WarnContext(ctx, "failed to get zpool capacity", "pool", poolName, "error", err)
	} else {
		tier.SizeBytes = size
		tier.UsedBytes = used
		tier.AvailableBytes = avail
	}

	return tier, nil
}

// getZpoolCapacity returns size, used, and available bytes for a ZFS pool.
func getZpoolCapacity(ctx context.Context, pool string) (size, used, avail uint64, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// zpool get -H -p size,allocated,free <pool>
	// Returns three lines:
	//   pool  size       <bytes>  -
	//   pool  allocated  <bytes>  -
	//   pool  free       <bytes>  -
	cmd := exec.CommandContext(ctx, "zpool", "get", "-H", "-p", "size,allocated,free", pool)
	output, err := cmd.Output()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("zpool get failed for %s: %w", pool, err)
	}

	var parsed int
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		val, parseErr := strconv.ParseUint(fields[2], 10, 64)
		if parseErr != nil {
			continue
		}
		switch fields[1] {
		case "size":
			size = val
			parsed++
		case "allocated":
			used = val
			parsed++
		case "free":
			avail = val
			parsed++
		}
	}
	if parsed == 0 {
		return 0, 0, 0, fmt.Errorf("zpool get for %s returned no parseable values: %s", pool, strings.TrimSpace(string(output)))
	}
	return size, used, avail, nil
}
