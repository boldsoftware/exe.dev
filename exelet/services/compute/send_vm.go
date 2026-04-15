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

	"exe.dev/exelet/storage"
	api "exe.dev/pkg/api/exe/compute/v1"
)

const sendVMChunkSize = 4*1024*1024 - 1024

type dataChunkSender func(chunk []byte, isBaseImage bool) error

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
	return r.stream.Send(&api.SendVMResponse{Type: &api.SendVMResponse_Progress{Progress: &api.SendVMProgress{BytesSent: int64(r.totalBytes)}}})
}

func (r *progressReporter) addStatus(msg string) error {
	return r.stream.Send(&api.SendVMResponse{Type: &api.SendVMResponse_Status{Status: &api.SendVMStatus{Message: msg}}})
}

func (s *Service) SendVM(stream api.ComputeService_SendVMServer) error {
	req, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "failed to receive start request: %v", err)
	}
	startReq := req.GetStart()
	if startReq == nil {
		return status.Error(codes.InvalidArgument, "first message must be SendVMStartRequest")
	}
	return s.runSendVM(stream, startReq)
}

func (s *Service) runSendVM(stream api.ComputeService_SendVMServer, startReq *api.SendVMStartRequest) error {
	ctx := stream.Context()
	instanceID := startReq.InstanceID
	s.log.InfoContext(ctx, "SendVM started", "instance", instanceID, "two_phase", startReq.TwoPhase, "live", startReq.Live, "direct", startReq.TargetAddress != "")

	if err := s.lockForMigration(instanceID); err != nil {
		return status.Errorf(codes.FailedPrecondition, "instance %s: %v", instanceID, err)
	}
	defer s.unlockMigration(instanceID)

	if rs := s.context.ReplicationSuspender; rs != nil {
		rs.SuspendVolume(instanceID)
		defer rs.ResumeVolume(instanceID)
		if rs.IsVolumeActive(instanceID) {
			s.log.InfoContext(ctx, "waiting on VM storage replication", "instance", instanceID)
			if startReq.AcceptStatus {
				_ = stream.Send(&api.SendVMResponse{Type: &api.SendVMResponse_Status{Status: &api.SendVMStatus{Message: "waiting for storage replication to complete"}}})
			}
			rs.WaitVolumeIdle(ctx, instanceID)
		}
	}

	instance, err := s.getInstance(ctx, instanceID)
	if errors.Is(err, api.ErrNotFound) {
		return status.Errorf(codes.NotFound, "instance not found: %s", instanceID)
	}
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get instance: %v", err)
	}

	sm, err := s.resolveStorageForInstance(ctx, instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to resolve storage pool: %v", err)
	}

	var target receiveVMTarget
	if startReq.TargetAddress != "" {
		s.log.InfoContext(ctx, "direct migration: connecting to target", "target", startReq.TargetAddress)
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
		initReq := &api.InitReceiveVMRequest{InstanceID: instanceID, SourceInstance: instance, BaseImageID: baseImageID, Encrypted: encrypted, EncryptionKey: encryptionKey, GroupID: groupID, Live: startReq.Live}
		var ready *api.InitReceiveVMResponse
		target, ready, err = newReceiveVMTarget(ctx, startReq.TargetAddress, initReq)
		if err != nil {
			return status.Errorf(codes.Internal, "direct migration: %v", err)
		}
		target.SetFaultInjection(&s.sidebandFaultAfterBytes, &s.sidebandFaultSkipCount, &s.sidebandFaultKillGRPC)
		defer target.Close()
		if err := stream.Send(&api.SendVMResponse{Type: &api.SendVMResponse_TargetReady{TargetReady: &api.SendVMTargetReady{HasBaseImage: ready.HasBaseImage, TargetNetwork: ready.TargetNetwork, SkipIpReconfig: ready.SkipIpReconfig}}}); err != nil {
			return status.Errorf(codes.Internal, "direct migration: failed to send target_ready to execore: %v", err)
		}
		startReq.TargetHasBaseImage = ready.HasBaseImage
		s.log.InfoContext(ctx, "direct migration: target ready", "has_base_image", ready.HasBaseImage, "sideband", target.SidebandAddr() != "", "resumable", target.Resumable())
	}

	var progress *progressReporter
	if target != nil {
		progress = &progressReporter{stream: stream}
	}
	sender := &streamMigrationSender{stream: stream}
	if startReq.Live {
		return s.sendVMLive(ctx, sender, startReq, instance, sm, target, progress)
	}
	if startReq.TwoPhase {
		return s.sendVMTwoPhase(ctx, sender, startReq, instance, sm, target, progress)
	}
	return s.sendVMCold(ctx, sender, startReq, instance, sm, target, progress)
}

func (s *Service) sendVMCold(ctx context.Context, sender migrationSender, startReq *api.SendVMStartRequest, instance *api.Instance, sm storage.StorageManager, target receiveVMTarget, progress *progressReporter) error {
	instanceID := startReq.InstanceID
	if instance.State != api.VMState_STOPPED {
		return status.Errorf(codes.FailedPrecondition, "VM must be stopped for migration, current state: %s", instance.State)
	}
	origin := sm.GetOrigin(instanceID)
	baseImageID := extractBaseImageID(origin)
	encryptionKey, err := sm.GetEncryptionKey(instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get encryption key: %v", err)
	}
	encrypted := encryptionKey != nil
	metadataKey := encryptionKey
	if target != nil {
		metadataKey = nil
	}
	if err := s.sendVMMetadata(ctx, sender, instance, baseImageID, encrypted, metadataKey); err != nil {
		return err
	}
	snapName, cleanup, err := sm.CreateMigrationSnapshot(ctx, instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to create migration snapshot: %v", err)
	}
	defer cleanup()
	hasher := sha256.New()
	var totalBytes uint64
	streamSnapshot := s.makeSnapshotStreamer(ctx, sender, instanceID, hasher, &totalBytes, sm, target, progress)
	if !startReq.TargetHasBaseImage && origin != "" && target == nil {
		if err := streamSnapshot(origin, false, "", true); err != nil {
			return status.Errorf(codes.Internal, "failed to send base image: %v", err)
		}
		if err := streamSnapshot(snapName, true, origin, false); err != nil {
			return status.Errorf(codes.Internal, "failed to send instance: %v", err)
		}
	} else {
		if err := streamSnapshot(snapName, false, "", false); err != nil {
			return status.Errorf(codes.Internal, "failed to send instance: %v", err)
		}
	}
	return s.finishSendVM(ctx, sender, instanceID, hasher, totalBytes, target)
}

func (s *Service) sendVMTwoPhase(ctx context.Context, sender migrationSender, startReq *api.SendVMStartRequest, instance *api.Instance, sm storage.StorageManager, target receiveVMTarget, progress *progressReporter) error {
	instanceID := startReq.InstanceID
	if instance.State == api.VMState_STOPPED {
		return s.sendVMCold(ctx, sender, startReq, instance, sm, target, progress)
	}
	if instance.State != api.VMState_RUNNING {
		return status.Errorf(codes.FailedPrecondition, "VM in invalid state for migration, current state: %s", instance.State)
	}
	encryptionKey, err := sm.GetEncryptionKey(instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get encryption key: %v", err)
	}
	encrypted := encryptionKey != nil
	metadataKey := encryptionKey
	if target != nil {
		metadataKey = nil
	}
	if err := s.sendVMMetadata(ctx, sender, instance, "", encrypted, metadataKey); err != nil {
		return err
	}
	dsName := sm.GetDatasetName(instanceID)
	preSnapName := dsName + "@migration-pre"
	sm.DestroySnapshot(ctx, preSnapName)
	unix.Sync()
	if err := sm.CreateSnapshot(ctx, preSnapName); err != nil {
		return status.Errorf(codes.Internal, "failed to create pre-copy snapshot: %v", err)
	}
	preSnapCleaned := false
	defer func() {
		if !preSnapCleaned {
			_ = sm.DestroySnapshot(ctx, preSnapName)
		}
	}()
	hasher := sha256.New()
	var totalBytes uint64
	streamSnapshot := s.makeSnapshotStreamer(ctx, sender, instanceID, hasher, &totalBytes, sm, target, progress)
	if err := streamSnapshot(preSnapName, false, "", false); err != nil {
		return status.Errorf(codes.Internal, "failed to send phase 1 data: %v", err)
	}
	phase1Bytes := totalBytes
	if err := s.sendPhaseComplete(ctx, sender, phase1Bytes, totalBytes, target, false); err != nil {
		return err
	}
	if _, err := sender.EmitAwaitControl(&api.SendVMAwaitControl{Reason: api.SendVMAwaitControl_NEED_GUEST_SYNC}); err != nil {
		return status.Errorf(codes.Internal, "failed to receive sync control: %v", err)
	}
	instance, err = s.getInstance(ctx, instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to reload instance state: %v", err)
	}
	if instance.State == api.VMState_RUNNING {
		if err := s.stopInstance(ctx, instanceID); err != nil {
			return status.Errorf(codes.Internal, "failed to stop VM for phase 2: %v", err)
		}
	}
	unix.Sync()
	migrationSnap, cleanup, err := sm.CreateMigrationSnapshot(ctx, instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to create migration snapshot: %v", err)
	}
	defer cleanup()
	if err := streamSnapshot(migrationSnap, true, preSnapName, false); err != nil {
		return status.Errorf(codes.Internal, "failed to send phase 2 data: %v", err)
	}
	if err := sm.DestroySnapshot(ctx, preSnapName); err != nil {
		s.log.WarnContext(ctx, "failed to destroy pre-copy snapshot", "snapshot", preSnapName, "error", err)
	}
	preSnapCleaned = true
	return s.finishSendVM(ctx, sender, instanceID, hasher, totalBytes, target)
}

func (s *Service) sendVMMetadata(ctx context.Context, sender migrationSender, instance *api.Instance, baseImageID string, encrypted bool, encryptionKey []byte) error {
	metadata := &api.SendVMMetadata{Instance: instance, BaseImageID: baseImageID, TotalSizeEstimate: instance.VMConfig.Disk, Encrypted: encrypted, EncryptionKey: encryptionKey}
	if err := sender.EmitMetadata(metadata); err != nil {
		return status.Errorf(codes.Internal, "failed to send metadata: %v", err)
	}
	s.log.DebugContext(ctx, "sent metadata", "instance", instance.ID, "base_image", baseImageID, "encrypted", encrypted)
	return nil
}

func (s *Service) finishSendVM(ctx context.Context, sender migrationSender, instanceID string, hasher hash.Hash, totalBytes uint64, target receiveVMTarget) error {
	checksum := fmt.Sprintf("%x", hasher.Sum(nil))
	if target != nil {
		completeResp, err := target.Complete(ctx, checksum)
		if err != nil {
			return status.Errorf(codes.Internal, "failed to complete on target: %v", err)
		}
		if completeResp.Error != "" {
			return status.Errorf(codes.Internal, "target reported error: %s", completeResp.Error)
		}
		if err := sender.EmitResult(&api.SendVMResult{Instance: completeResp.Instance, Error: completeResp.Error, ColdBooted: completeResp.ColdBooted}); err != nil {
			s.log.ErrorContext(ctx, "direct migration: failed to forward result to execore (target already committed)", "instance", instanceID, "error", err)
		}
		return nil
	}
	if ss, ok := sender.(*streamMigrationSender); ok {
		if err := ss.stream.Send(&api.SendVMResponse{Type: &api.SendVMResponse_Complete{Complete: &api.SendVMComplete{Checksum: checksum, TotalBytes: totalBytes}}}); err != nil {
			return status.Errorf(codes.Internal, "failed to send completion: %v", err)
		}
	}
	return nil
}

const maxSidebandRetries = 20

func isStaleResumeTokenErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "cannot resume send") || strings.Contains(s, "is no longer the same snapshot")
}

func streamViaSideband(ctx context.Context, target receiveVMTarget, log *slog.Logger, sm storage.StorageManager, snapshot string, incremental bool, baseSnap string, hasher hash.Hash, totalBytes *uint64, progress *progressReporter) error {
	err := streamViaSidebandOnce(ctx, target, sm, snapshot, incremental, baseSnap, hasher, totalBytes, progress)
	if err == nil {
		return nil
	}
	if !target.Resumable() {
		return err
	}
	for attempt := 1; attempt <= maxSidebandRetries; attempt++ {
		if progress != nil {
			_ = progress.addStatus(fmt.Sprintf("transfer interrupted (%v), resuming (attempt %d/%d)...", err, attempt, maxSidebandRetries))
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
		token, newAddr, reqErr := target.ResumeToken(ctx)
		if reqErr != nil {
			return fmt.Errorf("sideband retry %d: get resume token failed: %w (original error: %v)", attempt, reqErr, err)
		}
		if token == "" || newAddr == "" {
			return fmt.Errorf("sideband retry %d: missing resume token/address (original error: %v)", attempt, err)
		}
		target.SetSidebandAddr(newAddr)
		err = streamViaSidebandResume(ctx, target, sm, token, hasher, totalBytes, progress)
		if err == nil {
			return nil
		}
		if isStaleResumeTokenErr(err) {
			if progress != nil {
				_ = progress.addStatus("resume token stale (source snapshot changed), restarting full transfer...")
			}
			if _, reconnErr := target.RestartFresh(ctx); reconnErr != nil {
				return fmt.Errorf("sideband: restart fresh failed: %w (stale token error: %v)", reconnErr, err)
			}
			return streamViaSidebandOnce(ctx, target, sm, snapshot, incremental, baseSnap, hasher, totalBytes, progress)
		}
		_ = log
	}
	return fmt.Errorf("sideband transfer failed after %d retries: %w", maxSidebandRetries, err)
}

func streamViaSidebandOnce(ctx context.Context, target receiveVMTarget, sm storage.StorageManager, snapshot string, incremental bool, baseSnap string, hasher hash.Hash, totalBytes *uint64, progress *progressReporter) error {
	reader, err := sm.SendSnapshot(ctx, snapshot, incremental, baseSnap)
	if err != nil {
		return fmt.Errorf("zfs send: %w", err)
	}
	return pipeToSideband(ctx, target, reader, hasher, totalBytes, progress)
}

func streamViaSidebandResume(ctx context.Context, target receiveVMTarget, sm storage.StorageManager, token string, hasher hash.Hash, totalBytes *uint64, progress *progressReporter) error {
	reader, err := sm.SendSnapshotResume(ctx, token)
	if err != nil {
		return fmt.Errorf("zfs send -t: %w", err)
	}
	return pipeToSideband(ctx, target, reader, hasher, totalBytes, progress)
}

func pipeToSideband(ctx context.Context, target receiveVMTarget, reader io.ReadCloser, hasher hash.Hash, totalBytes *uint64, progress *progressReporter) error {
	conn, err := net.Dial("tcp", target.SidebandAddr())
	if err != nil {
		reader.Close()
		return fmt.Errorf("dial sideband %s: %w", target.SidebandAddr(), err)
	}
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(10 * time.Second)
	}
	go func() { <-ctx.Done(); conn.Close() }()
	dst := io.Writer(conn)
	if progress != nil {
		dst = &progressWriter{w: conn, progress: progress}
	}
	if faultAfterBytes, faultSkipCount, faultKillGRPC := target.FaultInjection(); faultAfterBytes != nil {
		if limit := faultAfterBytes.Load(); limit > 0 {
			if faultSkipCount != nil && faultSkipCount.Load() > 0 {
				faultSkipCount.Add(-1)
			} else {
				faultAfterBytes.Store(0)
				fw := &faultWriter{w: dst, conn: conn, remaining: limit}
				if faultKillGRPC != nil && faultKillGRPC.Load() {
					faultKillGRPC.Store(false)
				}
				dst = fw
			}
		}
	}
	hashReader := io.NopCloser(io.TeeReader(reader, hasher))
	n, copyErr := io.Copy(dst, hashReader)
	*totalBytes += uint64(n)
	closeErr := reader.Close()
	if copyErr != nil && !errors.Is(copyErr, net.ErrClosed) {
		return fmt.Errorf("copy to sideband: %w", copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.CloseWrite()
		tc.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, _ = io.Copy(io.Discard, tc)
	}
	return nil
}

type progressWriter struct {
	w        io.Writer
	progress *progressReporter
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	if n > 0 {
		_ = pw.progress.add(uint64(n))
	}
	return n, err
}

type faultWriter struct {
	w           io.Writer
	conn        net.Conn
	remaining   int64
	cancelExtra func()
}

func (fw *faultWriter) Write(p []byte) (int, error) {
	if fw.remaining <= 0 {
		return 0, net.ErrClosed
	}
	if int64(len(p)) > fw.remaining {
		n, err := fw.w.Write(p[:fw.remaining])
		fw.remaining -= int64(n)
		fw.conn.Close()
		if fw.cancelExtra != nil {
			fw.cancelExtra()
		}
		if err != nil {
			return n, err
		}
		return n, net.ErrClosed
	}
	n, err := fw.w.Write(p)
	fw.remaining -= int64(n)
	return n, err
}

func (s *Service) makeSnapshotStreamer(ctx context.Context, sender migrationSender, instanceID string, hasher hash.Hash, totalBytes *uint64, sm storage.StorageManager, target receiveVMTarget, progress *progressReporter) func(snapshot string, incremental bool, baseSnap string, isBaseImage bool) error {
	if target != nil && target.SidebandAddr() != "" {
		return func(snapshot string, incremental bool, baseSnap string, isBaseImage bool) error {
			if isBaseImage {
				return fmt.Errorf("base image transfer over direct mode is unsupported in this phase")
			}
			return streamViaSideband(ctx, target, s.log, sm, snapshot, incremental, baseSnap, hasher, totalBytes, progress)
		}
	}
	if ss, ok := sender.(*streamMigrationSender); ok {
		buf := make([]byte, sendVMChunkSize)
		sendChunk := func(chunk []byte, isBaseImage bool) error {
			return ss.stream.Send(&api.SendVMResponse{Type: &api.SendVMResponse_Data{Data: &api.SendVMDataChunk{Data: chunk, IsBaseImage: isBaseImage}}})
		}
		return s.makeStreamFunc(ctx, instanceID, hasher, buf, totalBytes, sm, sendChunk, progress)
	}
	return func(string, bool, string, bool) error {
		return fmt.Errorf("no sideband available for session-based migration")
	}
}

func (s *Service) sendPhaseComplete(ctx context.Context, sender migrationSender, phaseBytes, cumulativeBytes uint64, target receiveVMTarget, last bool) error {
	if target != nil {
		nextAddr, err := target.AdvancePhase(ctx, last)
		if err != nil {
			return status.Errorf(codes.Internal, "failed to advance phase on target: %v", err)
		}
		if !last {
			target.SetSidebandAddr(nextAddr)
		} else {
			target.SetSidebandAddr("")
		}
		return sender.EmitProgress(cumulativeBytes)
	}
	if ss, ok := sender.(*streamMigrationSender); ok {
		return ss.stream.Send(&api.SendVMResponse{Type: &api.SendVMResponse_PhaseComplete{PhaseComplete: &api.SendVMPhaseComplete{PhaseBytes: phaseBytes}}})
	}
	return nil
}

func (s *Service) makeStreamFunc(ctx context.Context, instanceID string, hasher hash.Hash, buf []byte, totalBytes *uint64, sm storage.StorageManager, sendChunk dataChunkSender, progress *progressReporter) func(snapshot string, incremental bool, baseSnap string, isBaseImage bool) error {
	return func(snapshot string, incremental bool, baseSnap string, isBaseImage bool) error {
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
		if err := reader.Close(); err != nil {
			return fmt.Errorf("zfs send failed: %w", err)
		}
		return nil
	}
}

func (s *Service) sendVMLive(ctx context.Context, sender migrationSender, startReq *api.SendVMStartRequest, instance *api.Instance, sm storage.StorageManager, target receiveVMTarget, progress *progressReporter) error {
	instanceID := startReq.InstanceID
	if instance.State != api.VMState_RUNNING {
		return status.Errorf(codes.FailedPrecondition, "VM must be running for live migration, current state: %s", instance.State)
	}
	if instance.VMConfig == nil || instance.VMConfig.NetworkInterface == nil || instance.VMConfig.NetworkInterface.IP == nil {
		return status.Errorf(codes.FailedPrecondition, "VM %s has no network configuration, cannot live migrate", instanceID)
	}
	encryptionKey, err := sm.GetEncryptionKey(instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get encryption key: %v", err)
	}
	encrypted := encryptionKey != nil
	metadataKey := encryptionKey
	if target != nil {
		metadataKey = nil
	}
	if err := s.sendVMMetadata(ctx, sender, instance, "", encrypted, metadataKey); err != nil {
		return err
	}
	dsName := sm.GetDatasetName(instanceID)
	preSnapName := dsName + "@migration-pre"
	sm.DestroySnapshot(ctx, preSnapName)
	unix.Sync()
	if err := sm.CreateSnapshot(ctx, preSnapName); err != nil {
		return status.Errorf(codes.Internal, "failed to create pre-copy snapshot: %v", err)
	}
	preSnapCleaned := false
	defer func() {
		if !preSnapCleaned {
			_ = sm.DestroySnapshot(ctx, preSnapName)
		}
	}()
	hasher := sha256.New()
	var totalBytes uint64
	streamSnapshot := s.makeSnapshotStreamer(ctx, sender, instanceID, hasher, &totalBytes, sm, target, progress)
	if err := streamSnapshot(preSnapName, false, "", false); err != nil {
		return status.Errorf(codes.Internal, "failed to send phase 1 data: %v", err)
	}
	if target != nil {
		if updatedReady := target.ConsumeUpdatedReady(); updatedReady != nil {
			_ = sender.EmitTargetReady(&api.SendVMTargetReady{HasBaseImage: updatedReady.HasBaseImage, TargetNetwork: updatedReady.TargetNetwork})
		}
	}
	phase1Bytes := totalBytes
	if err := s.sendPhaseComplete(ctx, sender, phase1Bytes, totalBytes, target, false); err != nil {
		return err
	}
	control, err := sender.EmitAwaitControl(&api.SendVMAwaitControl{Reason: api.SendVMAwaitControl_NEED_IP_RECONFIG, SourceNetwork: instance.VMConfig.NetworkInterface})
	if err != nil {
		return status.Errorf(codes.Internal, "failed to receive control: %v", err)
	}
	if control.Action != api.SendVMControl_PROCEED_WITH_PAUSE {
		return status.Errorf(codes.InvalidArgument, "unexpected control action: %v", control.Action)
	}
	if err := s.vmm.DeflateBalloon(ctx, instanceID); err != nil {
		s.log.WarnContext(ctx, "live: failed to deflate balloon (continuing)", "instance", instanceID, "error", err)
	}
	vmPaused := true
	defer func() {
		if vmPaused {
			resumeCtx := context.WithoutCancel(ctx)
			if err := s.vmm.Resume(resumeCtx, instanceID); err != nil {
				s.log.ErrorContext(resumeCtx, "live: failed to resume VM", "instance", instanceID, "error", err)
			}
		}
	}()
	if err := s.vmm.Pause(ctx, instanceID); err != nil {
		return status.Errorf(codes.Internal, "failed to pause VM: %v", err)
	}
	unix.Sync()
	migrationSnap, cleanup, err := sm.CreateMigrationSnapshot(ctx, instanceID)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to create migration snapshot: %v", err)
	}
	defer cleanup()
	if err := streamSnapshot(migrationSnap, true, preSnapName, false); err != nil {
		return status.Errorf(codes.Internal, "failed to send phase 2 data: %v", err)
	}
	phase2Bytes := totalBytes - phase1Bytes
	if err := s.sendPhaseComplete(ctx, sender, phase2Bytes, totalBytes, target, true); err != nil {
		return err
	}
	snapshotDir := s.getInstanceDir(instanceID) + "/ch-snapshot"
	defer os.RemoveAll(snapshotDir)
	if err := s.vmm.Snapshot(ctx, instanceID, snapshotDir); err != nil {
		return status.Errorf(codes.Internal, "failed to create CH snapshot: %v", err)
	}
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to read snapshot dir: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := s.streamSnapshotFile(ctx, sender, snapshotDir, entry.Name(), target); err != nil {
			return status.Errorf(codes.Internal, "failed to stream snapshot file %s: %v", entry.Name(), err)
		}
	}
	if err := sm.DestroySnapshot(ctx, preSnapName); err != nil {
		s.log.WarnContext(ctx, "failed to destroy pre-copy snapshot", "snapshot", preSnapName, "error", err)
	}
	preSnapCleaned = true
	if progress != nil {
		_ = progress.addStatus("Waiting for target to restore VM...")
	}
	if err := s.finishSendVM(ctx, sender, instanceID, hasher, totalBytes, target); err != nil {
		return err
	}
	vmPaused = false
	return nil
}

func (s *Service) streamSnapshotFile(ctx context.Context, sender migrationSender, dir, filename string, target receiveVMTarget) error {
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
			data := raw
			if useCompressed {
				data = compressed
			}
			if target != nil {
				if sendErr := target.UploadSnapshot(ctx, filename, data, useCompressed, isLast); sendErr != nil {
					return fmt.Errorf("failed to send chunk to target: %w", sendErr)
				}
			} else if ss, ok := sender.(*streamMigrationSender); ok {
				chunk := &api.SendVMSnapshotChunk{Filename: filename, IsLastChunk: isLast, Data: data, Compressed: useCompressed}
				if sendErr := ss.stream.Send(&api.SendVMResponse{Type: &api.SendVMResponse_SnapshotData{SnapshotData: chunk}}); sendErr != nil {
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

func extractBaseImageID(origin string) string {
	if origin == "" {
		return ""
	}
	if idx := strings.Index(origin, "@"); idx >= 0 {
		origin = origin[:idx]
	}
	if idx := strings.LastIndex(origin, "/"); idx >= 0 {
		origin = origin[idx+1:]
	}
	return origin
}
