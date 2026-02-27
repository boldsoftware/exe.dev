package replication

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	api "exe.dev/pkg/api/exe/replication/v1"
)

const (
	// MaxRetries is the maximum number of retries for transient failures
	MaxRetries = 3
	// SnapshotPrefix is the prefix for replication snapshots
	SnapshotPrefix = "repl-"
)

// VolumeInfo holds information about a volume to replicate
type VolumeInfo struct {
	ID      string // Remote volume ID (may include node-name suffix)
	LocalID string // Local dataset child name (for isRestoring checks)
	Name    string
	Dataset string // Full ZFS dataset path
}

// WorkerPool manages concurrent replication workers
type WorkerPool struct {
	ctx       context.Context
	cancel    context.CancelFunc
	target    Target
	state     *State
	metrics   *Metrics
	log       *slog.Logger
	retention int

	// isRestoring checks if a volume is currently being restored (skip replication)
	isRestoring func(volumeID string) bool

	mu           sync.Mutex
	pendingQueue []VolumeInfo // tracks items waiting in jobsCh
	activeJobs   map[string]*Job
	workerCount  int
	wg           sync.WaitGroup
	jobsCh       chan VolumeInfo
	busyWorkers  atomic.Int32
}

// Job represents an active replication job
type Job struct {
	mu               sync.Mutex
	VolumeID         string
	VolumeName       string
	State            api.ReplicationState
	ProgressPercent  float64
	BytesTransferred int64
	BytesTotal       int64
	ErrorMessage     string
	StartedAt        time.Time
	CompletedAt      time.Time
}

// snapshot returns a copy of the job's current state for safe reading.
func (j *Job) snapshot() Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	return Job{
		VolumeID:         j.VolumeID,
		VolumeName:       j.VolumeName,
		State:            j.State,
		ProgressPercent:  j.ProgressPercent,
		BytesTransferred: j.BytesTransferred,
		BytesTotal:       j.BytesTotal,
		ErrorMessage:     j.ErrorMessage,
		StartedAt:        j.StartedAt,
		CompletedAt:      j.CompletedAt,
	}
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(target Target, state *State, metrics *Metrics, retention int, log *slog.Logger, isRestoring func(string) bool) *WorkerPool {
	// Calculate worker count: max(1, numCPU / 4)
	workerCount := max(runtime.NumCPU()/4, 1)

	ctx, cancel := context.WithCancel(context.Background())

	wp := &WorkerPool{
		ctx:          ctx,
		cancel:       cancel,
		target:       target,
		state:        state,
		metrics:      metrics,
		log:          log,
		retention:    retention,
		isRestoring:  isRestoring,
		pendingQueue: make([]VolumeInfo, 0),
		activeJobs:   make(map[string]*Job),
		workerCount:  workerCount,
		jobsCh:       make(chan VolumeInfo, 100),
	}

	// Start workers
	for i := 0; i < workerCount; i++ {
		wp.wg.Add(1)
		go wp.worker(i)
	}

	return wp
}

// worker processes replication jobs
func (wp *WorkerPool) worker(id int) {
	defer wp.wg.Done()

	for {
		select {
		case <-wp.ctx.Done():
			return
		case volume, ok := <-wp.jobsCh:
			if !ok {
				return
			}
			wp.busyWorkers.Add(1)
			wp.processVolume(volume)
			wp.busyWorkers.Add(-1)
		}
	}
}

// processVolume handles replication for a single volume
func (wp *WorkerPool) processVolume(volume VolumeInfo) {
	// Remove from pending queue now that we're processing
	wp.removeFromPending(volume.ID)

	// Skip if volume is being restored (use LocalID since restoringVolumes tracks local IDs)
	if wp.isRestoring != nil && wp.isRestoring(volume.LocalID) {
		wp.log.Info("skipping replication, volume is being restored", "volume_id", volume.LocalID)
		return
	}

	startTime := time.Now()

	// Create job entry (VolumeID uses LocalID for API-visible output)
	job := &Job{
		VolumeID:   volume.LocalID,
		VolumeName: volume.Name,
		State:      api.ReplicationState_REPLICATION_STATE_SNAPSHOTTING,
		StartedAt:  startTime,
	}
	wp.setJob(volume.ID, job)
	defer wp.removeJob(volume.ID)

	wp.log.Info("starting replication", "volume_id", volume.LocalID, "volume_name", volume.Name)

	// Create snapshot
	snapshotName := fmt.Sprintf("%s%s", SnapshotPrefix, time.Now().UTC().Format("20060102T150405Z"))
	if err := wp.createSnapshot(volume.Dataset, snapshotName); err != nil {
		wp.recordFailure(job, volume, startTime, fmt.Errorf("failed to create snapshot: %w", err))
		return
	}
	wp.log.Debug("created snapshot", "volume_id", volume.LocalID, "snapshot", snapshotName)

	// Update job state
	job.mu.Lock()
	job.State = api.ReplicationState_REPLICATION_STATE_SENDING
	job.mu.Unlock()

	// Get existing snapshots on target to determine if incremental
	remoteSnapshots, err := wp.target.ListSnapshots(wp.ctx, volume.ID)
	if err != nil {
		wp.recordFailure(job, volume, startTime, fmt.Errorf("failed to list remote snapshots: %w", err))
		wp.destroySnapshot(volume.Dataset, snapshotName)
		return
	}

	// Determine base snapshot for incremental send
	var baseSnapshot string
	incremental := false
	if len(remoteSnapshots) > 0 {
		// Find matching local snapshot for incremental
		// Remote snapshots are in format: repl-20240115T143022Z
		// We need to find the most recent one that exists locally
		for i := len(remoteSnapshots) - 1; i >= 0; i-- {
			remoteSnap := remoteSnapshots[i]
			if !strings.HasPrefix(remoteSnap, SnapshotPrefix) {
				continue
			}
			// Check if this snapshot exists locally
			if wp.snapshotExists(volume.Dataset, remoteSnap) {
				baseSnapshot = remoteSnap
				incremental = true
				break
			}
		}
	}

	if incremental {
		wp.log.Debug("using incremental send", "volume_id", volume.LocalID, "base", baseSnapshot)
	} else {
		wp.log.Debug("using full send", "volume_id", volume.LocalID)
	}

	// Send with retries
	var sendErr error
	sendWithRetries := func(base string) error {
		for attempt := 1; attempt <= MaxRetries; attempt++ {
			// Reset progress for each attempt
			job.mu.Lock()
			job.BytesTransferred = 0
			job.mu.Unlock()

			err := wp.target.Send(wp.ctx, SendOptions{
				VolumeID:     volume.ID,
				Dataset:      volume.Dataset,
				SnapshotName: snapshotName,
				BaseSnapshot: base,
				OnProgress: func(bytesTransferred, bytesTotal int64) {
					job.mu.Lock()
					job.BytesTransferred = bytesTransferred
					job.BytesTotal = bytesTotal
					if bytesTotal > 0 {
						job.ProgressPercent = float64(bytesTransferred) / float64(bytesTotal) * 100
					}
					job.mu.Unlock()
				},
			})
			if err == nil {
				return nil
			}
			wp.log.Warn("send attempt failed", "volume_id", volume.LocalID, "attempt", attempt, "incremental", base != "", "error", err)
			if attempt < MaxRetries {
				backoff := []time.Duration{0, 5 * time.Second, 30 * time.Second}[attempt]
				select {
				case <-wp.ctx.Done():
					return wp.ctx.Err()
				case <-time.After(backoff):
				}
			}
			sendErr = err
		}
		return sendErr
	}

	if err := sendWithRetries(baseSnapshot); err != nil {
		if wp.ctx.Err() != nil {
			wp.recordFailure(job, volume, startTime, wp.ctx.Err())
			return
		}
		if !incremental {
			wp.recordFailure(job, volume, startTime, fmt.Errorf("failed to send after %d attempts: %w", MaxRetries, err))
			wp.destroySnapshot(volume.Dataset, snapshotName)
			return
		}
		// Incremental failed. Destroy remote dataset and retry as full send.
		wp.log.Warn("incremental send failed, destroying remote and retrying full send", "volume_id", volume.LocalID, "error", err)
		if deleter, ok := wp.target.(VolumeDeleter); ok {
			if err := deleter.DeleteVolume(wp.ctx, volume.ID); err != nil {
				wp.log.Warn("failed to delete remote volume before full send", "volume_id", volume.LocalID, "error", err)
			}
		}
		baseSnapshot = ""
		incremental = false
		if err := sendWithRetries(""); err != nil {
			if wp.ctx.Err() != nil {
				wp.recordFailure(job, volume, startTime, wp.ctx.Err())
				return
			}
			wp.recordFailure(job, volume, startTime, fmt.Errorf("failed to send (full fallback) after %d attempts: %w", MaxRetries, err))
			wp.destroySnapshot(volume.Dataset, snapshotName)
			return
		}
	}

	// Cleanup old replication snapshots on target (retention)
	// Filter to only replication snapshots to avoid deleting user snapshots
	// - ZFS snapshots: repl- prefix (e.g., repl-20240115T143022Z)
	// - File backups: .tar.gz suffix (e.g., <volumeID>-20240115T143022Z.tar.gz)
	if wp.retention > 0 {
		var replSnapshots []string
		for _, snap := range remoteSnapshots {
			if strings.HasPrefix(snap, SnapshotPrefix) || strings.HasSuffix(snap, ".tar.gz") {
				replSnapshots = append(replSnapshots, snap)
			}
		}
		if len(replSnapshots) >= wp.retention {
			// Delete oldest replication snapshots to maintain retention
			toDelete := len(replSnapshots) - wp.retention + 1 // +1 for the new one we just sent
			for i := range toDelete {
				if err := wp.target.Delete(wp.ctx, volume.ID, replSnapshots[i]); err != nil {
					wp.log.Warn("failed to delete old snapshot", "volume_id", volume.LocalID, "snapshot", replSnapshots[i], "error", err)
				} else {
					wp.log.Debug("deleted old snapshot", "volume_id", volume.LocalID, "snapshot", replSnapshots[i])
				}
			}
		}
	}

	// Cleanup old local replication snapshots - keep only the one we just sent for incremental
	wp.cleanupLocalSnapshots(volume.Dataset, 1)

	// Record success
	duration := time.Since(startTime)
	job.mu.Lock()
	job.State = api.ReplicationState_REPLICATION_STATE_COMPLETE
	job.CompletedAt = time.Now()
	bytesTransferred := job.BytesTransferred
	job.mu.Unlock()

	historyEntry := HistoryEntry{
		VolumeID:         volume.LocalID,
		VolumeName:       volume.Name,
		StartedAt:        startTime,
		CompletedAt:      job.CompletedAt,
		DurationMS:       duration.Milliseconds(),
		BytesTransferred: bytesTransferred,
		Success:          true,
		SnapshotName:     snapshotName,
		Incremental:      incremental,
	}
	wp.state.AddHistory(historyEntry)
	wp.metrics.RecordSuccess(volume.LocalID, wp.target.Type(), bytesTransferred, duration.Seconds())

	wp.log.Info("replication complete", "volume_id", volume.LocalID, "duration", duration, "incremental", incremental)
}

// recordFailure records a failed replication
func (wp *WorkerPool) recordFailure(job *Job, volume VolumeInfo, startTime time.Time, err error) {
	job.mu.Lock()
	job.State = api.ReplicationState_REPLICATION_STATE_FAILED
	job.ErrorMessage = err.Error()
	job.CompletedAt = time.Now()
	job.mu.Unlock()

	duration := time.Since(startTime)
	historyEntry := HistoryEntry{
		VolumeID:     volume.LocalID,
		VolumeName:   volume.Name,
		StartedAt:    startTime,
		CompletedAt:  job.CompletedAt,
		DurationMS:   duration.Milliseconds(),
		Success:      false,
		ErrorMessage: err.Error(),
	}
	wp.state.AddHistory(historyEntry)
	wp.metrics.RecordFailure(wp.target.Type())

	wp.log.Error("replication failed", "volume_id", volume.LocalID, "error", err)
}

// setJob sets a job in the active jobs map
func (wp *WorkerPool) setJob(volumeID string, job *Job) {
	wp.mu.Lock()
	wp.activeJobs[volumeID] = job
	wp.mu.Unlock()
}

// removeJob removes a job from the active jobs map
func (wp *WorkerPool) removeJob(volumeID string) {
	wp.mu.Lock()
	delete(wp.activeJobs, volumeID)
	wp.metrics.SetQueueSize(len(wp.pendingQueue) + len(wp.activeJobs))
	wp.mu.Unlock()
}

// removeFromPending removes a volume from the pending queue
func (wp *WorkerPool) removeFromPending(volumeID string) {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	for i, v := range wp.pendingQueue {
		if v.ID == volumeID {
			wp.pendingQueue = append(wp.pendingQueue[:i], wp.pendingQueue[i+1:]...)
			return
		}
	}
}

// QueueVolumes adds volumes to the replication queue
func (wp *WorkerPool) QueueVolumes(volumes []VolumeInfo) int {
	// Sort by ID for determinism
	sort.Slice(volumes, func(i, j int) bool {
		return volumes[i].ID < volumes[j].ID
	})

	queued := 0
	for _, v := range volumes {
		wp.mu.Lock()
		// Skip if already queued or being processed
		if wp.isVolumeQueuedLocked(v.ID) {
			wp.mu.Unlock()
			wp.log.Debug("skipping already queued volume", "volume_id", v.ID)
			continue
		}
		// Add to pending queue
		wp.pendingQueue = append(wp.pendingQueue, v)
		wp.mu.Unlock()

		select {
		case wp.jobsCh <- v:
			queued++
		case <-wp.ctx.Done():
			wp.removeFromPending(v.ID)
			wp.mu.Lock()
			wp.metrics.SetQueueSize(len(wp.pendingQueue) + len(wp.activeJobs))
			wp.mu.Unlock()
			return queued
		}
	}

	wp.mu.Lock()
	wp.metrics.SetQueueSize(len(wp.pendingQueue) + len(wp.activeJobs))
	wp.mu.Unlock()
	return queued
}

// isVolumeQueuedLocked checks if a volume is already in pending queue or active jobs.
// Caller must hold wp.mu.
func (wp *WorkerPool) isVolumeQueuedLocked(volumeID string) bool {
	// Check active jobs
	if _, exists := wp.activeJobs[volumeID]; exists {
		return true
	}
	// Check pending queue
	for _, v := range wp.pendingQueue {
		if v.ID == volumeID {
			return true
		}
	}
	return false
}

// ErrAlreadyQueued is returned when a volume is already queued or being processed.
var ErrAlreadyQueued = fmt.Errorf("volume already queued or in progress")

// QueueVolume adds a single volume to the queue. Blocks until the volume
// is accepted by a worker or the pool's context is cancelled.
func (wp *WorkerPool) QueueVolume(volume VolumeInfo) error {
	wp.mu.Lock()
	if wp.isVolumeQueuedLocked(volume.ID) {
		wp.mu.Unlock()
		wp.log.Debug("skipping already queued volume", "volume_id", volume.ID)
		return ErrAlreadyQueued
	}
	wp.pendingQueue = append(wp.pendingQueue, volume)
	wp.mu.Unlock()

	select {
	case wp.jobsCh <- volume:
		return nil
	case <-wp.ctx.Done():
		wp.removeFromPending(volume.ID)
		return wp.ctx.Err()
	}
}

// GetStatus returns the current worker pool status
func (wp *WorkerPool) GetStatus() (busyWorkers, totalWorkers int, jobs []*Job) {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	// Include active jobs (snapshot to avoid races on job fields)
	jobs = make([]*Job, 0, len(wp.activeJobs)+len(wp.pendingQueue))
	for _, j := range wp.activeJobs {
		snap := j.snapshot()
		jobs = append(jobs, &snap)
	}

	// Include pending items (not yet picked up by a worker)
	for _, v := range wp.pendingQueue {
		// Skip if already in activeJobs (race window)
		if _, exists := wp.activeJobs[v.ID]; exists {
			continue
		}
		jobs = append(jobs, &Job{
			VolumeID:   v.LocalID,
			VolumeName: v.Name,
			State:      api.ReplicationState_REPLICATION_STATE_PENDING,
		})
	}

	return int(wp.busyWorkers.Load()), wp.workerCount, jobs
}

// WaitIdle blocks until all pending and active jobs have completed,
// or until ctx is canceled. This is used to prevent overlapping
// replication cycles.
func (wp *WorkerPool) WaitIdle(ctx context.Context) {
	for {
		wp.mu.Lock()
		idle := len(wp.pendingQueue) == 0 && len(wp.activeJobs) == 0
		wp.mu.Unlock()
		if idle {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// WaitVolumeIdle blocks until the given volume has no active replication job,
// or until ctx is cancelled. This is used during migration to wait for an
// in-progress replication to finish before proceeding with ZFS operations.
func (wp *WorkerPool) WaitVolumeIdle(ctx context.Context, volumeID string) {
	for {
		wp.mu.Lock()
		_, active := wp.activeJobs[volumeID]
		wp.mu.Unlock()
		if !active {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Stop stops the worker pool
func (wp *WorkerPool) Stop() {
	wp.cancel()
	wp.wg.Wait()
}

// createSnapshot creates a ZFS snapshot
func (wp *WorkerPool) createSnapshot(dataset, name string) error {
	snapshotPath := fmt.Sprintf("%s@%s", dataset, name)
	cmd := exec.CommandContext(wp.ctx, "zfs", "snapshot", snapshotPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", err, string(output))
	}
	return nil
}

// destroySnapshot destroys a ZFS snapshot
func (wp *WorkerPool) destroySnapshot(dataset, name string) error {
	snapshotPath := fmt.Sprintf("%s@%s", dataset, name)
	cmd := exec.CommandContext(wp.ctx, "zfs", "destroy", snapshotPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", err, string(output))
	}
	return nil
}

// snapshotExists checks if a local snapshot exists
func (wp *WorkerPool) snapshotExists(dataset, name string) bool {
	snapshotPath := fmt.Sprintf("%s@%s", dataset, name)
	cmd := exec.CommandContext(wp.ctx, "zfs", "list", "-t", "snapshot", "-H", snapshotPath)
	return cmd.Run() == nil
}

// cleanupLocalSnapshots removes old replication snapshots locally
func (wp *WorkerPool) cleanupLocalSnapshots(dataset string, keep int) {
	// List all replication snapshots
	cmd := exec.CommandContext(wp.ctx, "zfs", "list", "-t", "snapshot", "-H", "-o", "name", "-s", "creation", dataset)
	output, err := cmd.Output()
	if err != nil {
		return
	}

	var replSnapshots []string
	for line := range strings.SplitSeq(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "@", 2)
		if len(parts) != 2 {
			continue
		}
		snapName := parts[1]
		if strings.HasPrefix(snapName, SnapshotPrefix) {
			replSnapshots = append(replSnapshots, snapName)
		}
	}

	// Delete old ones
	if len(replSnapshots) > keep {
		toDelete := replSnapshots[:len(replSnapshots)-keep]
		for _, snap := range toDelete {
			wp.destroySnapshot(dataset, snap)
		}
	}
}
