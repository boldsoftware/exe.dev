package compute

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	api "exe.dev/pkg/api/exe/compute/v1"
)

const sendVMChunkSize = 4 * 1024 * 1024 // 4MB chunks - optimized for multi-GB transfers

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
	s.log.InfoContext(ctx, "SendVM started", "instance", instanceID, "two_phase", startReq.TwoPhase)

	// Lock instance for migration
	if err := s.lockForMigration(instanceID); err != nil {
		return status.Errorf(codes.FailedPrecondition, "instance %s: %v", instanceID, err)
	}
	defer s.unlockMigration(instanceID)

	// Load instance
	instance, err := s.getInstance(ctx, instanceID)
	if errors.Is(err, api.ErrNotFound) {
		return status.Errorf(codes.NotFound, "instance not found: %s", instanceID)
	}
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get instance: %v", err)
	}

	if startReq.TwoPhase {
		return s.sendVMTwoPhase(ctx, stream, startReq, instance)
	}
	return s.sendVMCold(ctx, stream, startReq, instance)
}

// sendVMCold performs a single-phase (cold) migration: VM must be stopped.
func (s *Service) sendVMCold(ctx context.Context, stream api.ComputeService_SendVMServer, startReq *api.SendVMStartRequest, instance *api.Instance) error {
	instanceID := startReq.InstanceID

	// Verify VM is stopped
	if instance.State != api.VMState_STOPPED {
		return status.Errorf(codes.FailedPrecondition,
			"VM must be stopped for migration, current state: %s", instance.State)
	}

	// Get base image ID from ZFS origin
	origin := s.context.StorageManager.GetOrigin(instanceID)
	baseImageID := extractBaseImageID(origin)

	// Get encryption key if exists
	encryptionKey, err := s.context.StorageManager.GetEncryptionKey(instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get encryption key: %v", err)
	}
	encrypted := encryptionKey != nil

	// Send metadata
	if err := s.sendVMMetadata(stream, instance, baseImageID, encrypted, encryptionKey); err != nil {
		return err
	}

	// Create migration snapshot
	snapName, cleanup, err := s.context.StorageManager.CreateMigrationSnapshot(ctx, instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to create migration snapshot: %v", err)
	}
	defer cleanup()

	// Stream data in chunks with checksum
	hasher := sha256.New()
	buf := make([]byte, sendVMChunkSize)
	var totalBytes uint64

	streamSnapshot := s.makeStreamFunc(ctx, stream, instanceID, hasher, buf, &totalBytes)

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
func (s *Service) sendVMTwoPhase(ctx context.Context, stream api.ComputeService_SendVMServer, startReq *api.SendVMStartRequest, instance *api.Instance) error {
	instanceID := startReq.InstanceID

	// If VM is already stopped, fall back to cold migration
	if instance.State == api.VMState_STOPPED {
		s.log.InfoContext(ctx, "two-phase: VM already stopped, falling back to cold migration", "instance", instanceID)
		return s.sendVMCold(ctx, stream, startReq, instance)
	}
	if instance.State != api.VMState_RUNNING {
		return status.Errorf(codes.FailedPrecondition,
			"VM in invalid state for migration, current state: %s", instance.State)
	}

	// Get encryption key if exists
	encryptionKey, err := s.context.StorageManager.GetEncryptionKey(instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get encryption key: %v", err)
	}
	encrypted := encryptionKey != nil

	// Send metadata (no base image for two-phase - always sends full stream in phase 1)
	if err := s.sendVMMetadata(stream, instance, "", encrypted, encryptionKey); err != nil {
		return err
	}

	// Phase 1: Snapshot while VM is running, send full stream
	dsName := s.context.StorageManager.GetDatasetName(instanceID)
	preSnapName := dsName + "@migration-pre"

	// Clean up any leftover pre-snapshot from a previous failed attempt
	s.context.StorageManager.DestroySnapshot(ctx, preSnapName) //nolint:errcheck

	s.log.InfoContext(ctx, "two-phase: creating pre-copy snapshot", "snapshot", preSnapName)
	if err := s.context.StorageManager.CreateSnapshot(ctx, preSnapName); err != nil {
		return status.Errorf(codes.Internal, "failed to create pre-copy snapshot: %v", err)
	}

	// Ensure pre-snapshot is cleaned up
	preSnapCleaned := false
	defer func() {
		if !preSnapCleaned {
			if err := s.context.StorageManager.DestroySnapshot(ctx, preSnapName); err != nil {
				s.log.WarnContext(ctx, "failed to destroy pre-copy snapshot", "snapshot", preSnapName, "error", err)
			}
		}
	}()

	hasher := sha256.New()
	buf := make([]byte, sendVMChunkSize)
	var totalBytes uint64
	streamSnapshot := s.makeStreamFunc(ctx, stream, instanceID, hasher, buf, &totalBytes)

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
	migrationSnap, cleanup, err := s.context.StorageManager.CreateMigrationSnapshot(ctx, instanceID)
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
	if err := s.context.StorageManager.DestroySnapshot(ctx, preSnapName); err != nil {
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
func (s *Service) makeStreamFunc(ctx context.Context, stream api.ComputeService_SendVMServer, instanceID string, hasher hash.Hash, buf []byte, totalBytes *uint64) func(snapshot string, incremental bool, baseSnap string, isBaseImage bool) error {
	return func(snapshot string, incremental bool, baseSnap string, isBaseImage bool) error {
		s.log.DebugContext(ctx, "starting zfs send",
			"instance", instanceID,
			"snapshot", snapshot,
			"incremental", incremental,
			"base_snap", baseSnap,
			"is_base_image", isBaseImage)

		reader, err := s.context.StorageManager.SendSnapshot(ctx, snapshot, incremental, baseSnap)
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
