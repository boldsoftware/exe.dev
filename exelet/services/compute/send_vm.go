package compute

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/klauspost/compress/zstd"

	exeletclient "exe.dev/exelet/client"
	"exe.dev/exelet/storage"
	api "exe.dev/pkg/api/exe/compute/v1"
)

const sendVMChunkSize = 4*1024*1024 - 1024 // Just under 4MB to leave room for protobuf framing within gRPC's 4MB message limit

// directMigrationTarget manages a direct connection to the target exelet for data transfer.
type directMigrationTarget struct {
	client       *exeletclient.Client
	stream       api.ComputeService_ReceiveVMClient
	cancelFunc   context.CancelFunc
	sidebandAddr string // host:port of target's raw TCP listener; empty = use gRPC chunks
	resumable    bool   // target supports resumable sideband receives
}

func newDirectMigrationTarget(ctx context.Context, targetAddress string) (*directMigrationTarget, error) {
	client, err := exeletclient.NewClient(targetAddress, exeletclient.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("dial target exelet %s: %w", targetAddress, err)
	}
	ctx, cancel := context.WithCancel(ctx)

	stream, err := client.ReceiveVM(ctx)
	if err != nil {
		cancel()
		client.Close()
		return nil, fmt.Errorf("open ReceiveVM stream to %s: %w", targetAddress, err)
	}
	return &directMigrationTarget{client: client, stream: stream, cancelFunc: cancel}, nil
}

func (t *directMigrationTarget) Close() {
	t.cancelFunc()
	t.client.Close()
}

func (t *directMigrationTarget) sendData(chunk []byte, isBaseImage bool) error {
	return t.stream.Send(&api.ReceiveVMRequest{
		Type: &api.ReceiveVMRequest_Data{
			Data: &api.ReceiveVMDataChunk{
				Data:        chunk,
				IsBaseImage: isBaseImage,
			},
		},
	})
}

func (t *directMigrationTarget) sendPhaseComplete(last bool) error {
	return t.stream.Send(&api.ReceiveVMRequest{
		Type: &api.ReceiveVMRequest_PhaseComplete{
			PhaseComplete: &api.ReceiveVMPhaseComplete{Last: last},
		},
	})
}

func (t *directMigrationTarget) sendSnapshotData(filename string, data []byte, compressed, isLastChunk bool) error {
	return t.stream.Send(&api.ReceiveVMRequest{
		Type: &api.ReceiveVMRequest_SnapshotData{
			SnapshotData: &api.ReceiveVMSnapshotChunk{
				Filename:    filename,
				Data:        data,
				IsLastChunk: isLastChunk,
				Compressed:  compressed,
			},
		},
	})
}

func (t *directMigrationTarget) sendComplete(checksum string) error {
	return t.stream.Send(&api.ReceiveVMRequest{
		Type: &api.ReceiveVMRequest_Complete{
			Complete: &api.ReceiveVMComplete{
				Checksum: checksum,
			},
		},
	})
}

// requestResumeToken asks the target for a ZFS resume token and a new sideband address.
func (t *directMigrationTarget) requestResumeToken() (token, sidebandAddr string, err error) {
	if err := t.stream.Send(&api.ReceiveVMRequest{
		Type: &api.ReceiveVMRequest_ResumeTokenRequest{
			ResumeTokenRequest: &api.ReceiveVMResumeTokenRequest{},
		},
	}); err != nil {
		return "", "", fmt.Errorf("send resume token request: %w", err)
	}

	resp, err := t.stream.Recv()
	if err != nil {
		return "", "", fmt.Errorf("recv resume token response: %w", err)
	}
	rt := resp.GetResumeToken()
	if rt == nil {
		return "", "", fmt.Errorf("expected resume token response, got %T", resp.Type)
	}
	return rt.Token, rt.SidebandAddr, nil
}

// dataChunkSender sends a ZFS data chunk to the destination (either execore relay or direct target).
type dataChunkSender func(chunk []byte, isBaseImage bool) error

// progressReporter sends periodic progress updates to execore during direct migration.
type progressReporter struct {
	stream         api.ComputeService_SendVMServer
	totalBytes     uint64
	lastReportedMB uint64
}

func (r *progressReporter) add(n uint64) error {
	r.totalBytes += n
	currentMB := r.totalBytes / (1024 * 1024)
	if currentMB < r.lastReportedMB+100 {
		return nil
	}
	r.lastReportedMB = currentMB
	return r.stream.Send(&api.SendVMResponse{
		Type: &api.SendVMResponse_Progress{
			Progress: &api.SendVMProgress{BytesSent: int64(r.totalBytes)},
		},
	})
}

func (r *progressReporter) addStatus(msg string) error {
	return r.stream.Send(&api.SendVMResponse{
		Type: &api.SendVMResponse_Status{
			Status: &api.SendVMStatus{Message: msg},
		},
	})
}

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
	s.log.InfoContext(ctx, "SendVM started", "instance", instanceID, "two_phase", startReq.TwoPhase, "live", startReq.Live, "direct", startReq.TargetAddress != "")

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

	// Set up direct migration target if requested.
	var target *directMigrationTarget
	if startReq.TargetAddress != "" {
		s.log.InfoContext(ctx, "direct migration: connecting to target", "target", startReq.TargetAddress)
		target, err = newDirectMigrationTarget(ctx, startReq.TargetAddress)
		if err != nil {
			return status.Errorf(codes.Internal, "direct migration: %v", err)
		}
		defer target.Close()

		// Gather metadata for ReceiveVMStartRequest.
		origin := sm.GetOrigin(instanceID)
		baseImageID := extractBaseImageID(origin)
		encryptionKey, err := sm.GetEncryptionKey(instanceID)
		if err != nil {
			return status.Errorf(codes.Internal, "failed to get encryption key: %v", err)
		}
		encrypted := encryptionKey != nil

		groupID := startReq.TargetGroupID
		if groupID == "" {
			groupID = instance.GroupID
		}

		if err := target.stream.Send(&api.ReceiveVMRequest{
			Type: &api.ReceiveVMRequest_Start{
				Start: &api.ReceiveVMStartRequest{
					InstanceID:     instanceID,
					SourceInstance: instance,
					BaseImageID:    baseImageID,
					Encrypted:      encrypted,
					EncryptionKey:  encryptionKey,
					GroupID:        groupID,
					Live:           startReq.Live,
				},
			},
		}); err != nil {
			return status.Errorf(codes.Internal, "direct migration: failed to send start to target: %v", err)
		}

		// Wait for ReceiveVMReady from target.
		recvResp, err := target.stream.Recv()
		if err != nil {
			return status.Errorf(codes.Internal, "direct migration: failed to receive ready from target: %v", err)
		}
		ready := recvResp.GetReady()
		if ready == nil {
			return status.Errorf(codes.Internal, "direct migration: expected ready from target, got %T", recvResp.Type)
		}

		// Forward target readiness to execore.
		if err := stream.Send(&api.SendVMResponse{
			Type: &api.SendVMResponse_TargetReady{
				TargetReady: &api.SendVMTargetReady{
					HasBaseImage:  ready.HasBaseImage,
					TargetNetwork: ready.TargetNetwork,
				},
			},
		}); err != nil {
			return status.Errorf(codes.Internal, "direct migration: failed to send target_ready to execore: %v", err)
		}

		startReq.TargetHasBaseImage = ready.HasBaseImage
		target.sidebandAddr = ready.SidebandAddr
		target.resumable = ready.Resumable
		s.log.InfoContext(ctx, "direct migration: target ready",
			"has_base_image", ready.HasBaseImage,
			"sideband", target.sidebandAddr != "",
			"resumable", target.resumable)
	}

	// In direct mode, report periodic progress to execore.
	var progress *progressReporter
	if target != nil {
		progress = &progressReporter{stream: stream}
	}

	if startReq.Live {
		return s.sendVMLive(ctx, stream, startReq, instance, sm, target, progress)
	}
	if startReq.TwoPhase {
		return s.sendVMTwoPhase(ctx, stream, startReq, instance, sm, target, progress)
	}
	return s.sendVMCold(ctx, stream, startReq, instance, sm, target, progress)
}

// sendVMCold performs a single-phase (cold) migration: VM must be stopped.
func (s *Service) sendVMCold(ctx context.Context, stream api.ComputeService_SendVMServer, startReq *api.SendVMStartRequest, instance *api.Instance, sm storage.StorageManager, target *directMigrationTarget, progress *progressReporter) error {
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

	// In direct mode, don't send encryption key to execore (it goes directly to target).
	metadataKey := encryptionKey
	if target != nil {
		metadataKey = nil
	}

	// Send metadata
	if err := s.sendVMMetadata(stream, instance, baseImageID, encrypted, metadataKey); err != nil {
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
	var totalBytes uint64
	streamSnapshot := s.makeSnapshotStreamer(ctx, stream, instanceID, hasher, &totalBytes, sm, target, progress)

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

	return s.finishSendVM(stream, instanceID, hasher, totalBytes, target)
}

// sendVMTwoPhase performs a two-phase migration: snapshot while running (phase 1),
// stop VM and send incremental diff (phase 2).
func (s *Service) sendVMTwoPhase(ctx context.Context, stream api.ComputeService_SendVMServer, startReq *api.SendVMStartRequest, instance *api.Instance, sm storage.StorageManager, target *directMigrationTarget, progress *progressReporter) error {
	instanceID := startReq.InstanceID

	// If VM is already stopped, fall back to cold migration
	if instance.State == api.VMState_STOPPED {
		s.log.InfoContext(ctx, "two-phase: VM already stopped, falling back to cold migration", "instance", instanceID)
		return s.sendVMCold(ctx, stream, startReq, instance, sm, target, progress)
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

	// In direct mode, don't send encryption key to execore (it goes directly to target).
	metadataKey := encryptionKey
	if target != nil {
		metadataKey = nil
	}

	// Send metadata (no base image for two-phase - always sends full stream in phase 1)
	if err := s.sendVMMetadata(stream, instance, "", encrypted, metadataKey); err != nil {
		return err
	}

	// Phase 1: Snapshot while VM is running, send full stream
	dsName := sm.GetDatasetName(instanceID)
	preSnapName := dsName + "@migration-pre"

	// Clean up any leftover pre-snapshot from a previous failed attempt
	sm.DestroySnapshot(ctx, preSnapName) //nolint:errcheck

	// Sync filesystem to flush in-flight writes before snapshotting
	unix.Sync()

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
	var totalBytes uint64
	streamSnapshot := s.makeSnapshotStreamer(ctx, stream, instanceID, hasher, &totalBytes, sm, target, progress)

	s.log.InfoContext(ctx, "two-phase: streaming phase 1 (full pre-copy)")
	if err := streamSnapshot(preSnapName, false, "", false); err != nil {
		return status.Errorf(codes.Internal, "failed to send phase 1 data: %v", err)
	}

	phase1Bytes := totalBytes
	s.log.InfoContext(ctx, "two-phase: phase 1 complete", "bytes", phase1Bytes)

	// Send phase complete marker
	if err := s.sendPhaseComplete(stream, phase1Bytes, totalBytes, target, false); err != nil {
		return err
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

	// Sync after stop to flush any remaining in-flight writes
	unix.Sync()

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

	return s.finishSendVM(stream, instanceID, hasher, totalBytes, target)
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

// finishSendVM sends the completion message with checksum, handling both direct and relay modes.
func (s *Service) finishSendVM(stream api.ComputeService_SendVMServer, instanceID string, hasher hash.Hash, totalBytes uint64, target *directMigrationTarget) error {
	checksum := fmt.Sprintf("%x", hasher.Sum(nil))

	if target != nil {
		if err := target.sendComplete(checksum); err != nil {
			return status.Errorf(codes.Internal, "failed to send complete to target: %v", err)
		}
		if err := target.stream.CloseSend(); err != nil {
			return status.Errorf(codes.Internal, "failed to close send to target: %v", err)
		}

		var result *api.ReceiveVMResult
		for {
			resp, err := target.stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return status.Errorf(codes.Internal, "failed to receive result from target: %v", err)
			}
			if r := resp.GetResult(); r != nil {
				result = r
				break
			}
		}
		if result == nil {
			return status.Errorf(codes.Internal, "no result from target")
		}
		if result.Error != "" {
			return status.Errorf(codes.Internal, "target reported error: %s", result.Error)
		}

		s.log.InfoContext(stream.Context(), "direct migration: target result received",
			"instance", instanceID, "cold_booted", result.ColdBooted)

		if err := stream.Send(&api.SendVMResponse{
			Type: &api.SendVMResponse_Result{
				Result: &api.SendVMResult{
					Instance:   result.Instance,
					Error:      result.Error,
					ColdBooted: result.ColdBooted,
				},
			},
		}); err != nil {
			s.log.ErrorContext(stream.Context(), "direct migration: failed to forward result to execore (target already committed)",
				"instance", instanceID, "error", err)
		}

		s.log.InfoContext(stream.Context(), "SendVM completed (direct)",
			"instance", instanceID, "total_bytes", totalBytes, "checksum", checksum, "cold_booted", result.ColdBooted)
		return nil
	}

	// Relay mode: send complete to execore.
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
		"instance", instanceID, "total_bytes", totalBytes, "checksum", checksum)
	return nil
}

// makeDataChunkSender returns a function that sends a ZFS data chunk to the appropriate destination.
func (s *Service) makeDataChunkSender(stream api.ComputeService_SendVMServer, target *directMigrationTarget) dataChunkSender {
	if target != nil {
		return target.sendData
	}
	return func(chunk []byte, isBaseImage bool) error {
		return stream.Send(&api.SendVMResponse{
			Type: &api.SendVMResponse_Data{
				Data: &api.SendVMDataChunk{
					Data:        chunk,
					IsBaseImage: isBaseImage,
				},
			},
		})
	}
}

const maxSidebandRetries = 20

// streamViaSideband dials the target's raw TCP listener and streams a ZFS snapshot directly
// over TCP, bypassing gRPC framing. If the target supports resumable receives, broken TCP
// connections are retried using ZFS resume tokens — the transfer picks up from where it left off.
func (t *directMigrationTarget) streamViaSideband(ctx context.Context, log *slog.Logger, sm storage.StorageManager, snapshot string, incremental bool, baseSnap string, totalBytes *uint64, progress *progressReporter) error {
	err := t.streamViaSidebandOnce(ctx, sm, snapshot, incremental, baseSnap, totalBytes, progress)
	if err == nil {
		return nil
	}

	if !t.resumable {
		return err
	}

	// Retry loop using ZFS resume tokens.
	for attempt := 1; attempt <= maxSidebandRetries; attempt++ {
		log.WarnContext(ctx, "sideband transfer failed, requesting resume token",
			"attempt", attempt, "error", err, "bytes_so_far", *totalBytes)

		if progress != nil {
			_ = progress.addStatus(fmt.Sprintf("transfer interrupted (%v), resuming (attempt %d/%d)...", err, attempt, maxSidebandRetries))
		}

		// Brief pause so the target's zfs recv -s has time to exit and flush
		// its partial state to disk.
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}

		token, newAddr, reqErr := t.requestResumeToken()
		if reqErr != nil {
			return fmt.Errorf("sideband retry %d: failed to get resume token: %w (original error: %v)", attempt, reqErr, err)
		}
		if token == "" {
			return fmt.Errorf("sideband retry %d: no resume token available (original error: %v)", attempt, err)
		}
		if newAddr == "" {
			return fmt.Errorf("sideband retry %d: no sideband address for resume (original error: %v)", attempt, err)
		}

		log.InfoContext(ctx, "sideband: resuming transfer",
			"attempt", attempt, "new_addr", newAddr, "token_len", len(token))

		t.sidebandAddr = newAddr
		err = t.streamViaSidebandResume(ctx, sm, token, totalBytes, progress)
		if err == nil {
			log.InfoContext(ctx, "sideband: resumed transfer completed successfully", "attempt", attempt)
			return nil
		}
	}

	return fmt.Errorf("sideband transfer failed after %d retries: %w", maxSidebandRetries, err)
}

// streamViaSidebandOnce performs a single sideband transfer attempt.
func (t *directMigrationTarget) streamViaSidebandOnce(ctx context.Context, sm storage.StorageManager, snapshot string, incremental bool, baseSnap string, totalBytes *uint64, progress *progressReporter) error {
	reader, err := sm.SendSnapshot(ctx, snapshot, incremental, baseSnap)
	if err != nil {
		return fmt.Errorf("zfs send: %w", err)
	}
	return t.pipeToSideband(ctx, reader, totalBytes, progress)
}

// streamViaSidebandResume performs a resumed sideband transfer using a ZFS resume token.
func (t *directMigrationTarget) streamViaSidebandResume(ctx context.Context, sm storage.StorageManager, token string, totalBytes *uint64, progress *progressReporter) error {
	reader, err := sm.SendSnapshotResume(ctx, token)
	if err != nil {
		return fmt.Errorf("zfs send -t: %w", err)
	}
	return t.pipeToSideband(ctx, reader, totalBytes, progress)
}

// pipeToSideband dials the target's TCP listener and copies reader into it.
func (t *directMigrationTarget) pipeToSideband(ctx context.Context, reader io.ReadCloser, totalBytes *uint64, progress *progressReporter) error {
	conn, err := net.Dial("tcp", t.sidebandAddr)
	if err != nil {
		reader.Close()
		return fmt.Errorf("dial sideband %s: %w", t.sidebandAddr, err)
	}
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(10 * time.Second)
	}
	go func() { <-ctx.Done(); conn.Close() }()

	dst := io.Writer(conn)
	if progress != nil {
		dst = &progressWriter{w: conn, progress: progress}
	}
	n, copyErr := io.Copy(dst, reader)
	*totalBytes += uint64(n)
	closeErr := reader.Close()
	if copyErr != nil && !errors.Is(copyErr, net.ErrClosed) {
		return fmt.Errorf("copy to sideband: %w", copyErr)
	}
	return closeErr
}

// progressWriter wraps an io.Writer and reports bytes written to a progressReporter.
type progressWriter struct {
	w        io.Writer
	progress *progressReporter
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	if n > 0 {
		_ = pw.progress.add(uint64(n)) // non-fatal: best-effort progress reporting
	}
	return n, err
}

// makeSnapshotStreamer returns the function used to stream ZFS snapshots during migration.
// In sideband mode (direct target with a TCP listener), data flows over raw TCP for better
// I/O throughput. Falls back to gRPC chunks when sideband is unavailable or when a base
// image must be sent first (IsBaseImage=true is not supported over the raw TCP stream).
func (s *Service) makeSnapshotStreamer(ctx context.Context, stream api.ComputeService_SendVMServer, instanceID string, hasher hash.Hash, totalBytes *uint64, sm storage.StorageManager, target *directMigrationTarget, progress *progressReporter) func(snapshot string, incremental bool, baseSnap string, isBaseImage bool) error {
	if target != nil && target.sidebandAddr != "" {
		return func(snapshot string, incremental bool, baseSnap string, isBaseImage bool) error {
			if isBaseImage {
				// Base image transfer requires IsBaseImage framing not available over raw TCP.
				// Disable sideband for this migration and fall through to gRPC chunks below.
				target.sidebandAddr = ""
			} else {
				return target.streamViaSideband(ctx, s.log, sm, snapshot, incremental, baseSnap, totalBytes, progress)
			}
			// sideband was just disabled — fall through to gRPC chunk path
			buf := make([]byte, sendVMChunkSize)
			sendChunk := s.makeDataChunkSender(stream, target)
			return s.makeStreamFunc(ctx, instanceID, hasher, buf, totalBytes, sm, sendChunk, progress)(snapshot, incremental, baseSnap, isBaseImage)
		}
	}
	buf := make([]byte, sendVMChunkSize)
	sendChunk := s.makeDataChunkSender(stream, target)
	return s.makeStreamFunc(ctx, instanceID, hasher, buf, totalBytes, sm, sendChunk, progress)
}

// sendPhaseComplete sends a phase-complete marker, handling both direct and relay modes.
// In sideband mode, it also reads the target's PhaseReady reply to get the next TCP address.
// Set last=true when no more sideband phases follow (e.g. before CH snapshot phase).
func (s *Service) sendPhaseComplete(stream api.ComputeService_SendVMServer, phaseBytes, cumulativeBytes uint64, target *directMigrationTarget, last bool) error {
	if target != nil {
		if err := target.sendPhaseComplete(last); err != nil {
			return status.Errorf(codes.Internal, "failed to send phase complete to target: %v", err)
		}
		if target.sidebandAddr != "" && !last {
			// Target will send PhaseReady with the address for the next phase's TCP listener.
			resp, err := target.stream.Recv()
			if err != nil {
				return status.Errorf(codes.Internal, "failed to receive phase ready from target: %v", err)
			}
			pr := resp.GetPhaseReady()
			if pr == nil {
				return status.Errorf(codes.Internal, "expected PhaseReady from target, got %T", resp.Type)
			}
			target.sidebandAddr = pr.SidebandAddr
		} else if last {
			target.sidebandAddr = ""
		}
		return stream.Send(&api.SendVMResponse{
			Type: &api.SendVMResponse_Progress{
				Progress: &api.SendVMProgress{BytesSent: int64(cumulativeBytes)},
			},
		})
	}
	return stream.Send(&api.SendVMResponse{
		Type: &api.SendVMResponse_PhaseComplete{
			PhaseComplete: &api.SendVMPhaseComplete{
				PhaseBytes: phaseBytes,
			},
		},
	})
}

// makeStreamFunc returns a helper that streams ZFS send data through the gRPC stream.
func (s *Service) makeStreamFunc(ctx context.Context, instanceID string, hasher hash.Hash, buf []byte, totalBytes *uint64, sm storage.StorageManager, sendChunk dataChunkSender, progress *progressReporter) func(snapshot string, incremental bool, baseSnap string, isBaseImage bool) error {
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

				if sendErr := sendChunk(buf[:n], isBaseImage); sendErr != nil {
					reader.Close()
					return fmt.Errorf("failed to send chunk: %w", sendErr)
				}

				if progress != nil {
					if sendErr := progress.add(uint64(n)); sendErr != nil {
						reader.Close()
						return fmt.Errorf("failed to send progress: %w", sendErr)
					}
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
func (s *Service) sendVMLive(ctx context.Context, stream api.ComputeService_SendVMServer, startReq *api.SendVMStartRequest, instance *api.Instance, sm storage.StorageManager, target *directMigrationTarget, progress *progressReporter) error {
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

	// In direct mode, don't send encryption key to execore (it goes directly to target).
	metadataKey := encryptionKey
	if target != nil {
		metadataKey = nil
	}

	// Send metadata (no base image for live — always sends full stream in phase 1)
	if err := s.sendVMMetadata(stream, instance, "", encrypted, metadataKey); err != nil {
		return err
	}

	// Phase 1: Snapshot while VM is running, send full ZFS stream
	dsName := sm.GetDatasetName(instanceID)
	preSnapName := dsName + "@migration-pre"

	// Clean up any leftover pre-snapshot from a previous failed attempt
	sm.DestroySnapshot(ctx, preSnapName) //nolint:errcheck

	// Sync filesystem to flush in-flight writes before snapshotting
	unix.Sync()

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
	var totalBytes uint64
	streamSnapshot := s.makeSnapshotStreamer(ctx, stream, instanceID, hasher, &totalBytes, sm, target, progress)

	s.log.InfoContext(ctx, "live: streaming phase 1 (full pre-copy)")
	if err := streamSnapshot(preSnapName, false, "", false); err != nil {
		return status.Errorf(codes.Internal, "failed to send phase 1 data: %v", err)
	}

	phase1Bytes := totalBytes
	s.log.InfoContext(ctx, "live: phase 1 complete", "bytes", phase1Bytes)

	// Send phase complete marker
	if err := s.sendPhaseComplete(stream, phase1Bytes, totalBytes, target, false); err != nil {
		return err
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
	// Sync after pause to flush any remaining in-flight writes
	unix.Sync()

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

	// Send phase complete marker for phase 2 (last=true: no more sideband, CH snapshot follows via gRPC)
	if err := s.sendPhaseComplete(stream, phase2Bytes, totalBytes, target, true); err != nil {
		return err
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
		if err := s.streamSnapshotFile(stream, snapshotDir, entry.Name(), target); err != nil {
			return status.Errorf(codes.Internal, "failed to stream snapshot file %s: %v", entry.Name(), err)
		}
	}

	// Clean up pre-snapshot
	if err := sm.DestroySnapshot(ctx, preSnapName); err != nil {
		s.log.WarnContext(ctx, "failed to destroy pre-copy snapshot", "snapshot", preSnapName, "error", err)
	}
	preSnapCleaned = true

	if err := s.finishSendVM(stream, instanceID, hasher, totalBytes, target); err != nil {
		return err
	}

	// Mark VM as no longer needing resume — migration succeeded
	vmPaused = false

	return nil
}

// streamSnapshotFile streams a single snapshot file as SendVMSnapshotChunk messages.
// Each chunk is independently compressed with zstd. If compression doesn't reduce
// the size (incompressible data), the chunk is sent uncompressed.
func (s *Service) streamSnapshotFile(stream api.ComputeService_SendVMServer, dir, filename string, target *directMigrationTarget) error {
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

			isLast := readErr == io.EOF
			useCompressed := len(compressed) < n

			var data []byte
			if useCompressed {
				data = compressed
			} else {
				data = raw
			}

			if target != nil {
				if sendErr := target.sendSnapshotData(filename, data, useCompressed, isLast); sendErr != nil {
					return fmt.Errorf("failed to send chunk to target: %w", sendErr)
				}
			} else {
				chunk := &api.SendVMSnapshotChunk{
					Filename:    filename,
					IsLastChunk: isLast,
					Data:        data,
					Compressed:  useCompressed,
				}
				if sendErr := stream.Send(&api.SendVMResponse{
					Type: &api.SendVMResponse_SnapshotData{
						SnapshotData: chunk,
					},
				}); sendErr != nil {
					return fmt.Errorf("failed to send chunk: %w", sendErr)
				}
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
