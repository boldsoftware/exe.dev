package compute

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	api "exe.dev/pkg/api/exe/compute/v1"
)

const sendVMChunkSize = 4 * 1024 * 1024 // 4MB chunks - optimized for multi-GB transfers

// SendVM streams a stopped VM's disk and config to the caller for migration.
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
	s.log.InfoContext(ctx, "SendVM started", "instance", instanceID)

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
	metadata := &api.SendVMMetadata{
		Instance:          instance,
		BaseImageID:       baseImageID,
		TotalSizeEstimate: instance.VMConfig.Disk, // Estimate based on configured disk size
		Encrypted:         encrypted,
		EncryptionKey:     encryptionKey,
	}

	if err := stream.Send(&api.SendVMResponse{
		Type: &api.SendVMResponse_Metadata{Metadata: metadata},
	}); err != nil {
		return status.Errorf(codes.Internal, "failed to send metadata: %v", err)
	}

	s.log.DebugContext(ctx, "sent metadata", "instance", instanceID, "base_image", baseImageID, "encrypted", encrypted)

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

	// Helper to stream ZFS send data
	streamSnapshot := func(snapshot string, incremental bool, baseSnap string, isBaseImage bool) error {
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
				totalBytes += uint64(n)

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
		// We can't send incremental when target has the base image because ZFS
		// incremental streams contain the GUID of the origin snapshot. Even if we
		// create a snapshot with the same name on the target, it will have a different
		// GUID, causing zfs recv to fail with "local origin does not exist".
		// Sending a full stream creates an independent dataset (not a clone), which
		// uses more disk space but works reliably for migration.
		s.log.InfoContext(ctx, "sending full instance stream", "has_base_image", startReq.TargetHasBaseImage)
		if err := streamSnapshot(snapName, false, "", false); err != nil {
			return status.Errorf(codes.Internal, "failed to send instance: %v", err)
		}
	}

	// Send completion
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

	s.log.InfoContext(ctx, "SendVM completed",
		"instance", instanceID,
		"total_bytes", totalBytes,
		"checksum", checksum)

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
