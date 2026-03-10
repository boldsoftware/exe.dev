package replication

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	"exe.dev/exelet/storage"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
	api "exe.dev/pkg/api/exe/replication/v1"
)

const (
	// ReplicationServiceType is the service type identifier
	ReplicationServiceType services.Type = "exe.services.replication.v1"
)

// Service implements the replication service
type Service struct {
	api.UnimplementedReplicationServiceServer

	config  *config.ExeletConfig
	context *services.ServiceContext
	log     *slog.Logger

	mu         sync.RWMutex
	target     Target
	state      *State
	metrics    *Metrics
	workerPool *WorkerPool
	pruner     *Pruner

	runCtx  context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	nextRun time.Time
	running bool

	// restoringVolumes tracks volumes currently being restored (skip replication for these)
	restoringVolumes map[string]struct{}

	// orphanCandidates tracks dataset IDs that appeared orphaned in the previous
	// replication cycle. A dataset is only deleted if it appears orphaned in two
	// consecutive cycles (and a point-check confirms the instance is gone).
	orphanCandidates map[string]struct{}
}

// New creates a new replication service
func New(cfg *config.ExeletConfig, log *slog.Logger) (services.Service, error) {
	return &Service{
		config: cfg,
		log:    log.With("service", "replication"),
	}, nil
}

func (s *Service) Type() services.Type {
	return ReplicationServiceType
}

// Requires returns service dependencies
func (s *Service) Requires() []services.Type {
	return []services.Type{services.ComputeService}
}

func (s *Service) Register(ctx *services.ServiceContext, server *grpc.Server) error {
	if !s.config.ReplicationEnabled {
		s.log.Info("replication service disabled")
		return nil
	}

	if ctx == nil {
		return fmt.Errorf("service context is required")
	}
	if ctx.StorageManager == nil {
		return fmt.Errorf("storage manager is required for replication")
	}
	if ctx.ComputeService == nil {
		return fmt.Errorf("compute service is required for replication")
	}
	if s.config.Name == "" {
		return fmt.Errorf("exelet name is required for replication dataset namespacing")
	}

	s.context = ctx

	// Initialize state
	state, err := NewState(s.config.DataDir)
	if err != nil {
		return fmt.Errorf("failed to initialize state: %w", err)
	}
	s.state = state
	s.restoringVolumes = make(map[string]struct{})

	// Initialize metrics
	s.metrics = NewMetrics(ctx.MetricsRegistry)

	// Parse and create target
	target, err := ParseTarget(
		s.config.ReplicationTarget,
		s.config.ReplicationSSHKey,
		s.config.ReplicationSSHCommand,
		s.config.ReplicationKnownHostsPath,
		s.config.ReplicationBandwidthLimit,
	)
	if err != nil {
		return fmt.Errorf("failed to parse replication target: %w", err)
	}
	s.target = target

	// Initialize pruner
	s.pruner = NewPruner(target, s.config.ReplicationPrune, s.config.Name, s.log)

	// Initialize worker pool
	s.workerPool = NewWorkerPool(
		target,
		state,
		s.metrics,
		s.config.ReplicationRetention,
		s.config.ReplicationWorkers,
		s.log,
		s.IsRestoring,
	)

	// Register as the replication suspender so other services (compute)
	// can exclude volumes during migration.
	ctx.ReplicationSuspender = s

	api.RegisterReplicationServiceServer(server, s)
	s.log.Info("replication service registered",
		"target", s.config.ReplicationTarget,
		"interval", s.config.ReplicationInterval,
		"retention", s.config.ReplicationRetention,
	)

	return nil
}

func (s *Service) Start(ctx context.Context) error {
	if !s.config.ReplicationEnabled {
		return nil
	}

	if s.config.ReplicationInterval <= 0 {
		return fmt.Errorf("replication interval must be positive, got %s", s.config.ReplicationInterval)
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.runCtx = runCtx
	s.cancel = cancel

	// Start the replication ticker
	s.wg.Add(1)
	go s.runLoop(runCtx)

	s.log.InfoContext(ctx, "replication service started")
	return nil
}

func (s *Service) Stop(ctx context.Context) error {
	if !s.config.ReplicationEnabled {
		return nil
	}

	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()

	if s.workerPool != nil {
		s.workerPool.Stop()
	}

	if s.target != nil {
		s.target.Close()
	}

	s.log.InfoContext(ctx, "replication service stopped")
	return nil
}

// runLoop runs the periodic replication cycle
func (s *Service) runLoop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(s.config.ReplicationInterval)
	defer ticker.Stop()

	// Set initial next run time
	s.mu.Lock()
	s.nextRun = time.Now().Add(s.config.ReplicationInterval)
	s.mu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runReplicationCycle(ctx)
			s.mu.Lock()
			s.nextRun = time.Now().Add(s.config.ReplicationInterval)
			s.mu.Unlock()
		}
	}
}

// runReplicationCycle executes one full replication cycle
func (s *Service) runReplicationCycle(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.log.WarnContext(ctx, "skipping replication cycle, previous cycle still running")
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	s.log.InfoContext(ctx, "starting replication cycle")
	startTime := time.Now()

	storageManager := s.context.StorageManager

	// Get all datasets from storage (excludes base images)
	datasetIDs, err := storageManager.ListDatasets(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "failed to list datasets", "error", err)
		return
	}

	// Build instance name lookup (best-effort, for logging only).
	// instancesOK tracks whether Instances() succeeded so the orphan cleanup
	// can distinguish "no instances" from "call failed".
	nameByID := make(map[string]string)
	instances, err := s.context.ComputeService.Instances(ctx)
	instancesOK := err == nil
	if err != nil {
		s.log.WarnContext(ctx, "failed to get instances for name lookup", "error", err)
	} else {
		for _, inst := range instances {
			nameByID[inst.ID] = inst.Name
		}
	}

	// Build volume list from all datasets
	volumes := make([]VolumeInfo, 0, len(datasetIDs))
	localVolumeIDs := make(map[string]struct{})

	for _, id := range datasetIDs {
		// Skip temporary image extraction datasets
		if strings.HasPrefix(id, "tmp-sha256:") {
			continue
		}

		dataset := storageManager.GetDatasetName(id)
		if dataset == "" {
			s.log.WarnContext(ctx, "skipping dataset with no dataset name", "id", id)
			continue
		}

		remoteID := remoteVolumeID(id, s.config.Name)
		localVolumeIDs[remoteID] = struct{}{}

		volumes = append(volumes, VolumeInfo{
			ID:      remoteID,
			LocalID: id,
			Name:    nameByID[id],
			Dataset: dataset,
		})
	}

	s.log.InfoContext(ctx, "queuing volumes for replication", "count", len(volumes))

	// Queue volumes for replication
	queued := s.workerPool.QueueVolumes(volumes)
	s.log.DebugContext(ctx, "queued volumes", "queued", queued, "total", len(volumes))

	// Wait for all queued volumes to finish processing before
	// running pruning or allowing a new cycle to start
	s.workerPool.WaitIdle(ctx)

	// Run pruning after replication
	if err := s.pruner.Prune(ctx, localVolumeIDs); err != nil {
		s.log.ErrorContext(ctx, "pruning failed", "error", err)
	}

	// Prune orphaned base images (sha256: datasets with no dependent clones)
	if s.config.ReplicationPrune {
		pruned, err := storageManager.PruneOrphanedBaseImages(ctx)
		if err != nil {
			s.log.ErrorContext(ctx, "base image pruning failed", "error", err)
		} else if pruned > 0 {
			s.log.InfoContext(ctx, "pruned orphaned base images", "count", pruned)
		}
	}

	// Clean up orphaned VM datasets (requires two-cycle confirmation + point check).
	if s.config.ReplicationPrune {
		s.cleanOrphanedVMDatasets(ctx, datasetIDs, nameByID, instancesOK)
	}

	duration := time.Since(startTime)
	s.metrics.SetLastSuccessTimestamp(float64(time.Now().Unix()))
	s.log.InfoContext(ctx, "replication cycle complete", "duration", duration, "volumes", len(volumes))
}

// cleanOrphanedVMDatasets removes VM datasets whose instances no longer exist.
// Two safety layers protect against accidental deletion:
//  1. Two-cycle confirmation: a dataset must appear orphaned in two consecutive
//     cycles before deletion.
//  2. Point check: GetInstanceByID is called to verify the instance is gone
//     (guards against incomplete Instances() results).
//
// If instancesOK is false (Instances() failed), orphanCandidates is reset so a
// failed cycle never counts toward confirmation.
func (s *Service) cleanOrphanedVMDatasets(ctx context.Context, datasetIDs []string, nameByID map[string]string, instancesOK bool) {
	if !instancesOK {
		s.orphanCandidates = nil
		return
	}

	currentOrphans := make(map[string]struct{})
	for _, id := range datasetIDs {
		if !isVMInstanceID(id) {
			continue
		}
		if _, ok := nameByID[id]; ok {
			continue
		}
		currentOrphans[id] = struct{}{}
	}

	for id := range currentOrphans {
		if _, wasPrev := s.orphanCandidates[id]; !wasPrev {
			continue
		}
		// Confirmed orphan across two cycles — point-check before deleting.
		// Only proceed when GetInstanceByID returns a definitive ErrNotFound.
		// Transient errors (read races, VMM unavailable) must not trigger deletion.
		inst, err := s.context.ComputeService.GetInstanceByID(ctx, id)
		if err == nil && inst != nil {
			s.log.InfoContext(ctx, "skipping orphan deletion, instance still exists", "id", id)
			continue
		}
		if !errors.Is(err, computeapi.ErrNotFound) {
			s.log.WarnContext(ctx, "skipping orphan deletion, point check inconclusive", "id", id, "error", err)
			continue
		}
		s.log.InfoContext(ctx, "deleting orphaned VM dataset", "id", id)
		if err := s.context.StorageManager.Delete(ctx, id); err != nil {
			s.log.ErrorContext(ctx, "failed to delete orphaned VM dataset", "id", id, "error", err)
		}
	}

	s.orphanCandidates = currentOrphans
}

// GetStorageManager returns the storage manager (for use by other packages)
func (s *Service) GetStorageManager() storage.StorageManager {
	return s.context.StorageManager
}

// GetStatus implements ReplicationService.GetStatus
func (s *Service) GetStatus(ctx context.Context, req *api.GetStatusRequest) (*api.GetStatusResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	busyWorkers, totalWorkers, jobs := s.workerPool.GetStatus()

	// Build queue entries from active jobs
	queue := make([]*api.QueueEntry, 0, len(jobs))
	for _, job := range jobs {
		entry := &api.QueueEntry{
			VolumeID:         job.VolumeID,
			VolumeName:       job.VolumeName,
			State:            job.State,
			ProgressPercent:  job.ProgressPercent,
			BytesTransferred: job.BytesTransferred,
			BytesTotal:       job.BytesTotal,
			ErrorMessage:     job.ErrorMessage,
		}
		if !job.CompletedAt.IsZero() {
			entry.CompletedAt = job.CompletedAt.Unix()
		}
		queue = append(queue, entry)
	}

	// Calculate time until next run
	var nextRunSeconds int64
	if !s.nextRun.IsZero() {
		remaining := time.Until(s.nextRun)
		if remaining > 0 {
			nextRunSeconds = int64(remaining.Seconds())
		}
	}

	status := &api.ReplicatorStatus{
		Enabled:         s.config.ReplicationEnabled,
		Target:          s.config.ReplicationTarget,
		TargetType:      s.target.Type(),
		IntervalSeconds: int64(s.config.ReplicationInterval.Seconds()),
		NextRunSeconds:  nextRunSeconds,
		WorkersBusy:     int32(busyWorkers),
		WorkersTotal:    int32(totalWorkers),
		Queue:           queue,
	}

	return &api.GetStatusResponse{Status: status}, nil
}

// TriggerReplication implements ReplicationService.TriggerReplication
func (s *Service) TriggerReplication(ctx context.Context, req *api.TriggerReplicationRequest) (*api.TriggerReplicationResponse, error) {
	if req.VolumeID != "" {
		// Replicate specific volume - verify dataset exists
		dataset := s.context.StorageManager.GetDatasetName(req.VolumeID)
		if dataset == "" {
			return nil, fmt.Errorf("volume %s has no dataset", req.VolumeID)
		}
		if _, err := s.context.StorageManager.Get(ctx, req.VolumeID); err != nil {
			return nil, fmt.Errorf("volume not found: %s", req.VolumeID)
		}

		// Best-effort name resolution from compute service
		var name string
		if inst, err := s.context.ComputeService.GetInstanceByID(ctx, req.VolumeID); err == nil && inst != nil {
			name = inst.Name
		}

		volume := VolumeInfo{
			ID:      remoteVolumeID(req.VolumeID, s.config.Name),
			LocalID: req.VolumeID,
			Name:    name,
			Dataset: dataset,
		}
		if err := s.workerPool.QueueVolume(volume); err != nil {
			return nil, err
		}

		return &api.TriggerReplicationResponse{
			QueuedCount: 1,
			VolumeIds:   []string{req.VolumeID},
		}, nil
	}

	// Check if a cycle is already running before launching a new one
	s.mu.RLock()
	running := s.running
	serviceCtx := s.runCtx
	s.mu.RUnlock()

	if running {
		return nil, fmt.Errorf("replication cycle already in progress")
	}

	// Trigger full cycle asynchronously using service context (not request context)
	// Request context is canceled when RPC returns, which would abort the cycle.
	// Track in s.wg so Stop() waits for it to finish before tearing down the worker pool.
	s.wg.Go(func() {
		s.runReplicationCycle(serviceCtx)
	})

	return &api.TriggerReplicationResponse{
		QueuedCount: -1, // Indicates full cycle started
	}, nil
}

// GetHistory implements ReplicationService.GetHistory
func (s *Service) GetHistory(req *api.GetHistoryRequest, stream api.ReplicationService_GetHistoryServer) error {
	limit := int(req.Limit)
	if limit <= 0 {
		limit = MaxHistoryEntries
	}

	entries := s.state.GetHistory(limit)
	for _, entry := range entries {
		resp := &api.GetHistoryResponse{
			Entry: &api.HistoryEntry{
				VolumeID:         entry.VolumeID,
				VolumeName:       entry.VolumeName,
				StartedAt:        entry.StartedAt.Unix(),
				CompletedAt:      entry.CompletedAt.Unix(),
				DurationMs:       entry.DurationMS,
				BytesTransferred: entry.BytesTransferred,
				Success:          entry.Success,
				ErrorMessage:     entry.ErrorMessage,
				SnapshotName:     entry.SnapshotName,
				Incremental:      entry.Incremental,
			},
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}

	return nil
}

// RestoreVolume implements ReplicationService.RestoreVolume
func (s *Service) RestoreVolume(ctx context.Context, req *api.RestoreVolumeRequest) (*api.RestoreVolumeResponse, error) {
	if req.TargetRef == "" {
		return nil, fmt.Errorf("target_ref is required")
	}
	if req.VolumeID == "" {
		return nil, fmt.Errorf("volume_id is required")
	}

	dataset := s.context.StorageManager.GetDatasetName(req.VolumeID)
	if dataset == "" {
		return nil, fmt.Errorf("volume %s has no dataset configured", req.VolumeID)
	}

	// Mark volume as restoring (prevents snapshotting during restore)
	s.mu.Lock()
	s.restoringVolumes[req.VolumeID] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.restoringVolumes, req.VolumeID)
		s.mu.Unlock()
	}()

	// Check if VM exists (needed to decide whether to stop/start it)
	instance, err := s.context.ComputeService.GetInstanceByID(ctx, req.VolumeID)
	instanceExists := err == nil && instance != nil
	wasRunning := instanceExists && instance.State == computeapi.VMState_RUNNING

	// Helper to start VM after restore - restarts if it was running before
	startVMAfterRestore := func() {
		if !wasRunning {
			return
		}
		s.log.InfoContext(ctx, "starting VM after restore", "volume_id", req.VolumeID)
		if err := s.context.ComputeService.StartInstanceByID(ctx, req.VolumeID); err != nil {
			s.log.ErrorContext(ctx, "failed to start VM after restore", "volume_id", req.VolumeID, "error", err)
		} else {
			s.log.InfoContext(ctx, "VM started", "volume_id", req.VolumeID)
		}
	}

	// stopVM stops the VM if running. Called only after validation passes,
	// right before destructive operations, to avoid leaving the VM down on
	// validation failures.
	stopVM := func() error {
		if !wasRunning {
			return nil
		}
		s.log.InfoContext(ctx, "stopping VM before restore", "volume_id", req.VolumeID)
		if err := s.context.ComputeService.StopInstanceByID(ctx, req.VolumeID); err != nil {
			return fmt.Errorf("failed to stop VM before restore: %w", err)
		}
		s.log.InfoContext(ctx, "VM stopped", "volume_id", req.VolumeID)
		return nil
	}

	// Check if the snapshot exists locally - if so, just rollback to it
	snapshotPath := fmt.Sprintf("%s@%s", dataset, req.TargetRef)
	if s.snapshotExists(ctx, snapshotPath) {
		// Rollback destroys more recent snapshots, so require Force
		if !req.Force {
			return nil, fmt.Errorf("rollback to %s will destroy more recent snapshots (use force to proceed)", req.TargetRef)
		}
		// Validation passed - now stop the VM before the destructive operation
		if err := stopVM(); err != nil {
			return nil, err
		}
		s.log.InfoContext(ctx, "snapshot exists locally, rolling back", "snapshot", snapshotPath)
		if err := s.rollbackToSnapshot(ctx, snapshotPath); err != nil {
			startVMAfterRestore()
			return nil, fmt.Errorf("rollback failed: %w", err)
		}
		s.log.InfoContext(ctx, "rollback complete", "volume_id", req.VolumeID)
		startVMAfterRestore()
		return &api.RestoreVolumeResponse{
			VolumeID: req.VolumeID,
		}, nil
	}

	// Snapshot doesn't exist locally - need to receive from remote
	_, err = s.context.StorageManager.Get(ctx, req.VolumeID)
	volumeExists := err == nil

	// Validate force flag before doing anything destructive
	if volumeExists && !req.Force {
		return nil, fmt.Errorf("dataset %s already exists (use force to overwrite)", dataset)
	}

	// Verify the snapshot exists on the remote BEFORE stopping VM or destroying data.
	// Normalize TargetRef to basename so callers can pass either a bare snapshot name
	// or a full path (e.g., for file-based targets).
	targetRef := filepath.Base(req.TargetRef)
	remoteID := remoteVolumeID(req.VolumeID, s.config.Name)
	remoteSnapshots, err := s.target.ListSnapshots(ctx, remoteID)
	if err != nil {
		return nil, fmt.Errorf("failed to list remote snapshots: %w", err)
	}
	if !slices.Contains(remoteSnapshots, targetRef) {
		return nil, fmt.Errorf("snapshot %s not found on remote for volume %s", targetRef, req.VolumeID)
	}

	// All validation passed - now stop the VM before destructive operations
	if err := stopVM(); err != nil {
		return nil, err
	}

	s.log.InfoContext(ctx, "starting restore from remote", "volume_id", req.VolumeID, "snapshot", targetRef, "dataset", dataset)

	// If dataset exists, destroy it first (including all snapshots) to receive fresh from remote
	// Safe to destroy now since we've verified the snapshot exists on remote
	if volumeExists {
		s.log.InfoContext(ctx, "destroying existing dataset for restore", "dataset", dataset)
		if err := s.destroyDataset(ctx, dataset); err != nil {
			startVMAfterRestore()
			return nil, fmt.Errorf("failed to destroy existing dataset: %w", err)
		}
	}

	// Perform restore from remote
	err = s.target.Receive(ctx, ReceiveOptions{
		VolumeID:     remoteID,
		SnapshotName: targetRef,
		Dataset:      dataset,
		Force:        req.Force,
	})
	if err != nil {
		s.log.ErrorContext(ctx, "restore failed", "volume_id", req.VolumeID, "error", err)
		startVMAfterRestore()
		return nil, fmt.Errorf("restore failed: %w", err)
	}

	s.log.InfoContext(ctx, "restore complete", "volume_id", req.VolumeID)
	startVMAfterRestore()
	return &api.RestoreVolumeResponse{
		VolumeID: req.VolumeID,
	}, nil
}

// snapshotExists checks if a ZFS snapshot exists
func (s *Service) snapshotExists(ctx context.Context, snapshotPath string) bool {
	cmd := exec.CommandContext(ctx, "zfs", "list", "-t", "snapshot", "-H", snapshotPath)
	return cmd.Run() == nil
}

// rollbackToSnapshot rolls back a dataset to a snapshot
func (s *Service) rollbackToSnapshot(ctx context.Context, snapshotPath string) error {
	// -r destroys any snapshots more recent than the one specified
	cmd := exec.CommandContext(ctx, "zfs", "rollback", "-r", snapshotPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// destroyDataset destroys a ZFS dataset and all its snapshots
func (s *Service) destroyDataset(ctx context.Context, dataset string) error {
	cmd := exec.CommandContext(ctx, "zfs", "destroy", "-r", dataset)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// IsRestoring reports whether volumeID is currently being restored or
// otherwise excluded from replication (e.g. during migration).
func (s *Service) IsRestoring(volumeID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.restoringVolumes[volumeID]
	return ok
}

// SuspendVolume temporarily excludes a volume from replication.
// Call ResumeVolume when done. Used during migration to prevent the replication
// worker from snapshotting a dataset that is being transferred via zfs recv.
func (s *Service) SuspendVolume(volumeID string) {
	s.mu.Lock()
	s.restoringVolumes[volumeID] = struct{}{}
	s.mu.Unlock()
	s.log.Info("suspended replication for volume", "volume_id", volumeID)
}

// WaitVolumeIdle blocks until the given volume has no active replication job.
func (s *Service) WaitVolumeIdle(ctx context.Context, volumeID string) {
	if s.workerPool != nil {
		s.workerPool.WaitVolumeIdle(ctx, volumeID)
	}
}

// ResumeVolume re-enables replication for a previously suspended volume.
func (s *Service) ResumeVolume(volumeID string) {
	s.mu.Lock()
	delete(s.restoringVolumes, volumeID)
	s.mu.Unlock()
	s.log.Info("resumed replication for volume", "volume_id", volumeID)
}

// ListSnapshots implements ReplicationService.ListSnapshots
func (s *Service) ListSnapshots(ctx context.Context, req *api.ListSnapshotsRequest) (*api.ListSnapshotsResponse, error) {
	if req.VolumeID == "" {
		return nil, fmt.Errorf("volume_id is required")
	}

	resp := &api.ListSnapshotsResponse{
		VolumeID: req.VolumeID,
	}

	// Get local snapshots
	dataset := s.context.StorageManager.GetDatasetName(req.VolumeID)
	if dataset != "" {
		localSnapshots, err := s.listLocalSnapshots(ctx, dataset)
		if err != nil {
			s.log.WarnContext(ctx, "failed to list local snapshots", "volume_id", req.VolumeID, "error", err)
		} else {
			resp.LocalSnapshots = localSnapshots
		}
	}

	// Get remote snapshots from target with full metadata
	if s.target != nil {
		remoteSnapshots, err := s.target.ListSnapshotsWithMetadata(ctx, remoteVolumeID(req.VolumeID, s.config.Name))
		if err != nil {
			s.log.WarnContext(ctx, "failed to list remote snapshots", "volume_id", req.VolumeID, "error", err)
		} else {
			for _, snap := range remoteSnapshots {
				resp.RemoteSnapshots = append(resp.RemoteSnapshots, &api.SnapshotInfo{
					Name:          snap.Name,
					CreatedAt:     snap.CreatedAt,
					SizeBytes:     snap.SizeBytes,
					IsReplication: strings.HasPrefix(snap.Name, SnapshotPrefix),
				})
			}
		}
	}

	return resp, nil
}

// ListRemoteSnapshots implements ReplicationService.ListRemoteSnapshots
func (s *Service) ListRemoteSnapshots(req *api.ListRemoteSnapshotsRequest, stream api.ReplicationService_ListRemoteSnapshotsServer) error {
	ctx := stream.Context()

	if s.target == nil {
		return fmt.Errorf("replication target not configured")
	}

	// Get all replication snapshots directly from the remote
	snapshots, err := s.target.ListAllReplicationSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("failed to list remote snapshots: %w", err)
	}

	// Sort by creation time (newest first)
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Snapshot.CreatedAt > snapshots[j].Snapshot.CreatedAt
	})

	// Apply limit if specified
	limit := int(req.Limit)
	if limit > 0 && len(snapshots) > limit {
		snapshots = snapshots[:limit]
	}

	// Stream results
	for _, entry := range snapshots {
		resp := &api.ListRemoteSnapshotsResponse{
			VolumeID: entry.VolumeID,
			Snapshot: &api.SnapshotInfo{
				Name:          entry.Snapshot.Name,
				CreatedAt:     entry.Snapshot.CreatedAt,
				SizeBytes:     entry.Snapshot.SizeBytes,
				IsReplication: true, // ListAllReplicationSnapshots only returns replication snapshots
			},
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}

	return nil
}

// isVMInstanceID reports whether id matches the globally unique vm\d{6}-* pattern (e.g., vm000123-blue-falcon).
func isVMInstanceID(id string) bool {
	// vm followed by exactly 6 digits then a dash: vm000123-...
	if len(id) < 9 || id[0] != 'v' || id[1] != 'm' || id[8] != '-' {
		return false
	}
	for i := 2; i < 8; i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

// remoteVolumeID transforms a local dataset ID into a remote-safe ID.
// VM instance IDs are already globally unique and returned as-is.
// Non-VM datasets get a -<nodeName> suffix to avoid collisions when
// multiple exelets target the same replication pool.
func remoteVolumeID(localID, nodeName string) string {
	if isVMInstanceID(localID) {
		return localID
	}
	suffix := "-" + nodeName
	if strings.HasSuffix(localID, suffix) {
		return localID
	}
	return localID + suffix
}

// listLocalSnapshots lists all snapshots for a dataset
func (s *Service) listLocalSnapshots(ctx context.Context, dataset string) ([]*api.SnapshotInfo, error) {
	// List snapshots with creation time and referenced size
	cmd := exec.CommandContext(ctx, "zfs", "list", "-t", "snapshot", "-H", "-p", "-o", "name,creation,referenced", "-s", "creation", dataset)
	output, err := cmd.Output()
	if err != nil {
		// Dataset might not exist
		return nil, nil
	}

	var snapshots []*api.SnapshotInfo
	for line := range strings.SplitSeq(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		// Extract snapshot name from full path (pool/dataset@snapshot -> snapshot)
		fullName := fields[0]
		parts := strings.SplitN(fullName, "@", 2)
		if len(parts) != 2 {
			continue
		}
		name := parts[1]

		// Parse creation time (Unix timestamp)
		var createdAt int64
		fmt.Sscanf(fields[1], "%d", &createdAt)

		// Parse size
		var sizeBytes int64
		fmt.Sscanf(fields[2], "%d", &sizeBytes)

		snapshots = append(snapshots, &api.SnapshotInfo{
			Name:          name,
			CreatedAt:     createdAt,
			SizeBytes:     sizeBytes,
			IsReplication: strings.HasPrefix(name, SnapshotPrefix),
		})
	}

	return snapshots, nil
}
