package compute

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/klauspost/compress/zstd"

	"exe.dev/exelet/storage"
	api "exe.dev/pkg/api/exe/compute/v1"
)

const sendVMChunkSize = 4*1024*1024 - 1024 // Just under 4MB to leave room for protobuf framing within gRPC's 4MB message limit

// SendVM streams a VM's disk and config to the caller for migration.
func (s *Service) SendVM(stream api.ComputeService_SendVMServer) error {
	ctx := stream.Context()

	// Receive start request
	req, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "failed to receive start request: %v", err)
	}
	startReq := req.GetStart()
	if startReq == nil {
		return status.Error(codes.InvalidArgument, "first message must be SendVMStartRequest")
	}

	instanceID := startReq.InstanceID
	s.log.InfoContext(ctx, "SendVM started", "instance", instanceID, "two_phase", startReq.TwoPhase, "live", startReq.Live)

	// Lock instance for migration
	if err := s.lockForMigration(instanceID); err != nil {
		return status.Errorf(codes.FailedPrecondition, "instance %s: %v", instanceID, err)
	}
	defer s.unlockMigration(instanceID)

	// Suspend replication for this volume to prevent the replication worker
	// from creating snapshots that conflict with migration snapshots.
	if rs := s.context.ReplicationSuspender; rs != nil {
		rs.SuspendVolume(instanceID)
		defer rs.ResumeVolume(instanceID)
		if rs.IsVolumeActive(instanceID) {
			s.log.InfoContext(ctx, "waiting on VM storage replication", "instance", instanceID)
			if startReq.AcceptStatus {
				_ = stream.Send(&api.SendVMResponse{
					Type: &api.SendVMResponse_Status{
						Status: &api.SendVMStatus{
							Message: "waiting for storage replication to complete",
						},
					},
				})
			}
			rs.WaitVolumeIdle(ctx, instanceID)
		}
	}

	// Load instance
	instance, err := s.getInstance(ctx, instanceID)
	if errors.Is(err, api.ErrNotFound) {
		return status.Errorf(codes.NotFound, "instance not found: %s", instanceID)
	}
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get instance: %v", err)
	}

	// Resolve the correct storage manager for this instance (may be on a non-primary pool)
	sm, err := s.resolveStorageForInstance(ctx, instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to resolve storage pool: %v", err)
	}

	if startReq.Live {
		return s.sendVMLive(ctx, stream, startReq, instance, sm)
	}
	if startReq.TwoPhase {
		return s.sendVMTwoPhase(ctx, stream, startReq, instance, sm)
	}
	return s.sendVMCold(ctx, stream, startReq, instance, sm)
}

// sendVMCold performs a single-phase (cold) migration: VM must be stopped.
func (s *Service) sendVMCold(ctx context.Context, stream api.ComputeService_SendVMServer, startReq *api.SendVMStartRequest, instance *api.Instance, sm storage.StorageManager) error {
	instanceID := startReq.InstanceID

	// Verify VM is stopped
	if instance.State != api.VMState_STOPPED {
		return status.Errorf(codes.FailedPrecondition,
			"VM must be stopped for migration, current state: %s", instance.State)
	}

	// Get base image ID from ZFS origin
	origin := sm.GetOrigin(instanceID)
	baseImageID := extractBaseImageID(origin)

	// Get encryption key if exists
	encryptionKey, err := sm.GetEncryptionKey(instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get encryption key: %v", err)
	}
	encrypted := encryptionKey != nil

	// Send metadata
	if err := s.sendVMMetadata(stream, instance, baseImageID, encrypted, encryptionKey); err != nil {
		return err
	}

	// Create migration snapshot
	snapName, cleanup, err := sm.CreateMigrationSnapshot(ctx, instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to create migration snapshot: %v", err)
	}
	defer cleanup()

	// Stream data in chunks with checksum
	hasher := sha256.New()
	buf := make([]byte, sendVMChunkSize)
	var totalBytes uint64

	streamSnapshot := s.makeStreamFunc(ctx, stream, instanceID, hasher, buf, &totalBytes, sm)

	// If target doesn't have base image and this is a clone, send base image first
	if !startReq.TargetHasBaseImage && origin != "" {
		s.log.InfoContext(ctx, "sending base image first", "origin", origin)
		if err := streamSnapshot(origin, false, "", true); err != nil {
			return status.Errorf(codes.Internal, "failed to send base image: %v", err)
		}
		// Now send instance as incremental from origin
		if err := streamSnapshot(snapName, true, origin, false); err != nil {
			return status.Errorf(codes.Internal, "failed to send instance: %v", err)
		}
	} else {
		// Target has base image OR no origin - send full stream of instance.
		s.log.InfoContext(ctx, "sending full instance stream", "has_base_image", startReq.TargetHasBaseImage)
		if err := streamSnapshot(snapName, false, "", false); err != nil {
			return status.Errorf(codes.Internal, "failed to send instance: %v", err)
		}
	}

	return s.sendVMComplete(stream, instanceID, hasher, totalBytes)
}

// sendVMTwoPhase performs a two-phase migration: snapshot while running (phase 1),
// stop VM and send incremental diff (phase 2).
func (s *Service) sendVMTwoPhase(ctx context.Context, stream api.ComputeService_SendVMServer, startReq *api.SendVMStartRequest, instance *api.Instance, sm storage.StorageManager) error {
	instanceID := startReq.InstanceID

	// If VM is already stopped, fall back to cold migration
	if instance.State == api.VMState_STOPPED {
		s.log.InfoContext(ctx, "two-phase: VM already stopped, falling back to cold migration", "instance", instanceID)
		return s.sendVMCold(ctx, stream, startReq, instance, sm)
	}
	if instance.State != api.VMState_RUNNING {
		return status.Errorf(codes.FailedPrecondition,
			"VM in invalid state for migration, current state: %s", instance.State)
	}

	// Get encryption key if exists
	encryptionKey, err := sm.GetEncryptionKey(instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get encryption key: %v", err)
	}
	encrypted := encryptionKey != nil

	// Send metadata (no base image for two-phase - always sends full stream in phase 1)
	if err := s.sendVMMetadata(stream, instance, "", encrypted, encryptionKey); err != nil {
		return err
	}

	// Phase 1: Snapshot while VM is running, send full stream
	dsName := sm.GetDatasetName(instanceID)
	preSnapName := dsName + "@migration-pre"

	// Clean up any leftover pre-snapshot from a previous failed attempt
	sm.DestroySnapshot(ctx, preSnapName) //nolint:errcheck

	s.log.InfoContext(ctx, "two-phase: creating pre-copy snapshot", "snapshot", preSnapName)
	if err := sm.CreateSnapshot(ctx, preSnapName); err != nil {
		return status.Errorf(codes.Internal, "failed to create pre-copy snapshot: %v", err)
	}

	// Ensure pre-snapshot is cleaned up
	preSnapCleaned := false
	defer func() {
		if !preSnapCleaned {
			if err := sm.DestroySnapshot(ctx, preSnapName); err != nil {
				s.log.WarnContext(ctx, "failed to destroy pre-copy snapshot", "snapshot", preSnapName, "error", err)
			}
		}
	}()

	hasher := sha256.New()
	buf := make([]byte, sendVMChunkSize)
	var totalBytes uint64
	streamSnapshot := s.makeStreamFunc(ctx, stream, instanceID, hasher, buf, &totalBytes, sm)

	s.log.InfoContext(ctx, "two-phase: streaming phase 1 (full pre-copy)")
	if err := streamSnapshot(preSnapName, false, "", false); err != nil {
		return status.Errorf(codes.Internal, "failed to send phase 1 data: %v", err)
	}

	phase1Bytes := totalBytes
	s.log.InfoContext(ctx, "two-phase: phase 1 complete", "bytes", phase1Bytes)

	// Send phase complete marker
	if err := stream.Send(&api.SendVMResponse{
		Type: &api.SendVMResponse_PhaseComplete{
			PhaseComplete: &api.SendVMPhaseComplete{
				PhaseBytes: phase1Bytes,
			},
		},
	}); err != nil {
		return status.Errorf(codes.Internal, "failed to send phase complete: %v", err)
	}

	// Phase 2: Stop VM and send incremental diff
	s.log.InfoContext(ctx, "two-phase: stopping VM for phase 2", "instance", instanceID)

	// Reload instance state in case the VM stopped on its own
	instance, err = s.getInstance(ctx, instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to reload instance state: %v", err)
	}
	if instance.State == api.VMState_RUNNING {
		if err := s.stopInstance(ctx, instanceID); err != nil {
			return status.Errorf(codes.Internal, "failed to stop VM for phase 2: %v", err)
		}
	}
	s.log.InfoContext(ctx, "two-phase: VM stopped, creating final snapshot")

	// Create final migration snapshot
	migrationSnap, cleanup, err := sm.CreateMigrationSnapshot(ctx, instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to create migration snapshot: %v", err)
	}
	defer cleanup()

	// Send incremental diff from pre-copy to final
	s.log.InfoContext(ctx, "two-phase: streaming phase 2 (incremental diff)",
		"base", preSnapName, "target", migrationSnap)
	if err := streamSnapshot(migrationSnap, true, preSnapName, false); err != nil {
		return status.Errorf(codes.Internal, "failed to send phase 2 data: %v", err)
	}

	phase2Bytes := totalBytes - phase1Bytes
	s.log.InfoContext(ctx, "two-phase: phase 2 complete",
		"phase1_bytes", phase1Bytes, "phase2_bytes", phase2Bytes)

	// Clean up pre-snapshot now (before final snapshot cleanup)
	if err := sm.DestroySnapshot(ctx, preSnapName); err != nil {
		s.log.WarnContext(ctx, "failed to destroy pre-copy snapshot", "snapshot", preSnapName, "error", err)
	}
	preSnapCleaned = true

	return s.sendVMComplete(stream, instanceID, hasher, totalBytes)
}

// sendVMMetadata sends the metadata message on the stream.
func (s *Service) sendVMMetadata(stream api.ComputeService_SendVMServer, instance *api.Instance, baseImageID string, encrypted bool, encryptionKey []byte) error {
	metadata := &api.SendVMMetadata{
		Instance:          instance,
		BaseImageID:       baseImageID,
		TotalSizeEstimate: instance.VMConfig.Disk,
		Encrypted:         encrypted,
		EncryptionKey:     encryptionKey,
	}
	if err := stream.Send(&api.SendVMResponse{
		Type: &api.SendVMResponse_Metadata{Metadata: metadata},
	}); err != nil {
		return status.Errorf(codes.Internal, "failed to send metadata: %v", err)
	}
	s.log.DebugContext(stream.Context(), "sent metadata", "instance", instance.ID, "base_image", baseImageID, "encrypted", encrypted)
	return nil
}

// sendVMComplete sends the completion message with checksum.
func (s *Service) sendVMComplete(stream api.ComputeService_SendVMServer, instanceID string, hasher hash.Hash, totalBytes uint64) error {
	checksum := fmt.Sprintf("%x", hasher.Sum(nil))
	if err := stream.Send(&api.SendVMResponse{
		Type: &api.SendVMResponse_Complete{
			Complete: &api.SendVMComplete{
				Checksum:   checksum,
				TotalBytes: totalBytes,
			},
		},
	}); err != nil {
		return status.Errorf(codes.Internal, "failed to send completion: %v", err)
	}
	s.log.InfoContext(stream.Context(), "SendVM completed",
		"instance", instanceID,
		"total_bytes", totalBytes,
		"checksum", checksum)
	return nil
}

// makeStreamFunc returns a helper that streams ZFS send data through the gRPC stream.
func (s *Service) makeStreamFunc(ctx context.Context, stream api.ComputeService_SendVMServer, instanceID string, hasher hash.Hash, buf []byte, totalBytes *uint64, sm storage.StorageManager) func(snapshot string, incremental bool, baseSnap string, isBaseImage bool) error {
	return func(snapshot string, incremental bool, baseSnap string, isBaseImage bool) error {
		s.log.DebugContext(ctx, "starting zfs send",
			"instance", instanceID,
			"snapshot", snapshot,
			"incremental", incremental,
			"base_snap", baseSnap,
			"is_base_image", isBaseImage)

		reader, err := sm.SendSnapshot(ctx, snapshot, incremental, baseSnap)
		if err != nil {
			return fmt.Errorf("failed to start zfs send: %w", err)
		}

		for {
			n, err := reader.Read(buf)
			if n > 0 {
				hasher.Write(buf[:n])
				*totalBytes += uint64(n)

				if sendErr := stream.Send(&api.SendVMResponse{
					Type: &api.SendVMResponse_Data{
						Data: &api.SendVMDataChunk{
							Data:        buf[:n],
							IsBaseImage: isBaseImage,
						},
					},
				}); sendErr != nil {
					reader.Close()
					return fmt.Errorf("failed to send chunk: %w", sendErr)
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				reader.Close()
				return fmt.Errorf("failed to read zfs send output: %w", err)
			}
		}

		// Close waits for zfs send to complete and returns any exit errors
		if err := reader.Close(); err != nil {
			return fmt.Errorf("zfs send failed: %w", err)
		}
		return nil
	}
}

// sendVMLive performs a live migration: two-phase ZFS transfer, then CH snapshot/restore.
// The VM's process state is preserved across the migration.
func (s *Service) sendVMLive(ctx context.Context, stream api.ComputeService_SendVMServer, startReq *api.SendVMStartRequest, instance *api.Instance, sm storage.StorageManager) error {
	instanceID := startReq.InstanceID

	// Verify VM is running
	if instance.State != api.VMState_RUNNING {
		return status.Errorf(codes.FailedPrecondition,
			"VM must be running for live migration, current state: %s", instance.State)
	}

	// Verify VM has network configuration (required for IP reconfiguration during live migration)
	if instance.VMConfig == nil || instance.VMConfig.NetworkInterface == nil || instance.VMConfig.NetworkInterface.IP == nil {
		return status.Errorf(codes.FailedPrecondition,
			"VM %s has no network configuration, cannot live migrate", instanceID)
	}

	// Get encryption key if exists
	encryptionKey, err := sm.GetEncryptionKey(instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get encryption key: %v", err)
	}
	encrypted := encryptionKey != nil

	// Send metadata (no base image for live — always sends full stream in phase 1)
	if err := s.sendVMMetadata(stream, instance, "", encrypted, encryptionKey); err != nil {
		return err
	}

	// Phase 1: Snapshot while VM is running, send full ZFS stream
	dsName := sm.GetDatasetName(instanceID)
	preSnapName := dsName + "@migration-pre"

	// Clean up any leftover pre-snapshot from a previous failed attempt
	sm.DestroySnapshot(ctx, preSnapName) //nolint:errcheck

	s.log.InfoContext(ctx, "live: creating pre-copy snapshot", "snapshot", preSnapName)
	if err := sm.CreateSnapshot(ctx, preSnapName); err != nil {
		return status.Errorf(codes.Internal, "failed to create pre-copy snapshot: %v", err)
	}

	preSnapCleaned := false
	defer func() {
		if !preSnapCleaned {
			if err := sm.DestroySnapshot(ctx, preSnapName); err != nil {
				s.log.WarnContext(ctx, "failed to destroy pre-copy snapshot", "snapshot", preSnapName, "error", err)
			}
		}
	}()

	hasher := sha256.New()
	buf := make([]byte, sendVMChunkSize)
	var totalBytes uint64
	streamSnapshot := s.makeStreamFunc(ctx, stream, instanceID, hasher, buf, &totalBytes, sm)

	s.log.InfoContext(ctx, "live: streaming phase 1 (full pre-copy)")
	if err := streamSnapshot(preSnapName, false, "", false); err != nil {
		return status.Errorf(codes.Internal, "failed to send phase 1 data: %v", err)
	}

	phase1Bytes := totalBytes
	s.log.InfoContext(ctx, "live: phase 1 complete", "bytes", phase1Bytes)

	// Send phase complete marker
	if err := stream.Send(&api.SendVMResponse{
		Type: &api.SendVMResponse_PhaseComplete{
			PhaseComplete: &api.SendVMPhaseComplete{
				PhaseBytes: phase1Bytes,
			},
		},
	}); err != nil {
		return status.Errorf(codes.Internal, "failed to send phase complete: %v", err)
	}

	// Tell orchestrator we need the IP reconfigured before we pause
	s.log.InfoContext(ctx, "live: requesting IP reconfiguration")
	if err := stream.Send(&api.SendVMResponse{
		Type: &api.SendVMResponse_AwaitControl{
			AwaitControl: &api.SendVMAwaitControl{
				Reason:        api.SendVMAwaitControl_NEED_IP_RECONFIG,
				SourceNetwork: instance.VMConfig.NetworkInterface,
			},
		},
	}); err != nil {
		return status.Errorf(codes.Internal, "failed to send await control: %v", err)
	}

	// Wait for orchestrator to confirm IP reconfig is done
	s.log.InfoContext(ctx, "live: waiting for control message")
	controlReq, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.Internal, "failed to receive control: %v", err)
	}
	control := controlReq.GetControl()
	if control == nil {
		return status.Errorf(codes.InvalidArgument, "expected control message, got %T", controlReq.Type)
	}
	if control.Action != api.SendVMControl_PROCEED_WITH_PAUSE {
		return status.Errorf(codes.InvalidArgument, "unexpected control action: %v", control.Action)
	}

	// Deflate balloon to force all memory back into the guest before snapshot.
	// With free_page_reporting, the host may have reclaimed pages that would
	// cause "Bad address" errors during snapshot restore.
	s.log.InfoContext(ctx, "live: deflating balloon", "instance", instanceID)
	if err := s.vmm.DeflateBalloon(ctx, instanceID); err != nil {
		s.log.WarnContext(ctx, "live: failed to deflate balloon (continuing)", "instance", instanceID, "error", err)
	}

	// Pause VM — this is the start of downtime
	s.log.InfoContext(ctx, "live: pausing VM", "instance", instanceID)
	vmPaused := true
	defer func() {
		if vmPaused {
			// Use WithoutCancel since the stream context may already be cancelled
			// when this runs (e.g., orchestrator cancelled the context on error).
			resumeCtx := context.WithoutCancel(ctx)
			s.log.WarnContext(resumeCtx, "live: resuming VM due to error", "instance", instanceID)
			if err := s.vmm.Resume(resumeCtx, instanceID); err != nil {
				s.log.ErrorContext(resumeCtx, "live: failed to resume VM", "instance", instanceID, "error", err)
			}
		}
	}()
	if err := s.vmm.Pause(ctx, instanceID); err != nil {
		return status.Errorf(codes.Internal, "failed to pause VM: %v", err)
	}

	// Phase 2: Incremental ZFS diff from pre-copy to final
	migrationSnap, cleanup, err := sm.CreateMigrationSnapshot(ctx, instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to create migration snapshot: %v", err)
	}
	defer cleanup()

	s.log.InfoContext(ctx, "live: streaming phase 2 (incremental diff)")
	if err := streamSnapshot(migrationSnap, true, preSnapName, false); err != nil {
		return status.Errorf(codes.Internal, "failed to send phase 2 data: %v", err)
	}

	phase2Bytes := totalBytes - phase1Bytes
	s.log.InfoContext(ctx, "live: phase 2 complete", "phase1_bytes", phase1Bytes, "phase2_bytes", phase2Bytes)

	// Send phase complete marker for phase 2
	if err := stream.Send(&api.SendVMResponse{
		Type: &api.SendVMResponse_PhaseComplete{
			PhaseComplete: &api.SendVMPhaseComplete{
				PhaseBytes: phase2Bytes,
			},
		},
	}); err != nil {
		return status.Errorf(codes.Internal, "failed to send phase 2 complete: %v", err)
	}

	// Phase 3: CH snapshot — stream snapshot files to orchestrator
	snapshotDir := s.getInstanceDir(instanceID) + "/ch-snapshot"
	defer os.RemoveAll(snapshotDir)

	s.log.InfoContext(ctx, "live: creating CH snapshot", "dir", snapshotDir)
	if err := s.vmm.Snapshot(ctx, instanceID, snapshotDir); err != nil {
		return status.Errorf(codes.Internal, "failed to create CH snapshot: %v", err)
	}

	// Stream snapshot files
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to read snapshot dir: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := s.streamSnapshotFile(stream, snapshotDir, entry.Name()); err != nil {
			return status.Errorf(codes.Internal, "failed to stream snapshot file %s: %v", entry.Name(), err)
		}
	}

	// Clean up pre-snapshot
	if err := sm.DestroySnapshot(ctx, preSnapName); err != nil {
		s.log.WarnContext(ctx, "failed to destroy pre-copy snapshot", "snapshot", preSnapName, "error", err)
	}
	preSnapCleaned = true

	// Mark VM as no longer needing resume — migration succeeded
	vmPaused = false

	return s.sendVMComplete(stream, instanceID, hasher, totalBytes)
}

// streamSnapshotFile streams a single snapshot file as SendVMSnapshotChunk messages.
// Each chunk is independently compressed with zstd. If compression doesn't reduce
// the size (incompressible data), the chunk is sent uncompressed.
func (s *Service) streamSnapshotFile(stream api.ComputeService_SendVMServer, dir, filename string) error {
	f, err := os.Open(dir + "/" + filename)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", filename, err)
	}
	defer f.Close()

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithWindowSize(4*1024*1024))
	if err != nil {
		return fmt.Errorf("failed to create zstd encoder: %w", err)
	}
	defer enc.Close()

	buf := make([]byte, sendVMChunkSize)
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			raw := buf[:n]
			compressed := enc.EncodeAll(raw, make([]byte, 0, n))

			chunk := &api.SendVMSnapshotChunk{
				Filename:    filename,
				IsLastChunk: readErr == io.EOF,
			}
			if len(compressed) < n {
				chunk.Data = compressed
				chunk.Compressed = true
			} else {
				chunk.Data = raw
				chunk.Compressed = false
			}

			if sendErr := stream.Send(&api.SendVMResponse{
				Type: &api.SendVMResponse_SnapshotData{
					SnapshotData: chunk,
				},
			}); sendErr != nil {
				return fmt.Errorf("failed to send chunk: %w", sendErr)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("failed to read %s: %w", filename, readErr)
		}
	}
	return nil
}

// extractBaseImageID extracts the base image ID from a ZFS origin name.
// Origin format: "tank/sha256:abc123@instance-id" -> "sha256:abc123"
func extractBaseImageID(origin string) string {
	if origin == "" {
		return ""
	}

	// Remove pool prefix (e.g., "tank/sha256:abc123@snap" -> "sha256:abc123@snap")
	if idx := strings.Index(origin, "/"); idx >= 0 {
		origin = origin[idx+1:]
	}

	// Remove snapshot suffix (e.g., "sha256:abc123@snap" -> "sha256:abc123")
	if idx := strings.Index(origin, "@"); idx >= 0 {
		origin = origin[:idx]
	}

	return origin
}
