package compute

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// InitReceiveVM creates a migration session on the target exelet.
func (s *Service) InitReceiveVM(ctx context.Context, req *api.InitReceiveVMRequest) (*api.InitReceiveVMResponse, error) {
	instanceID := req.InstanceID
	if instanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id is required")
	}

	s.log.InfoContext(ctx, "InitReceiveVM", "instance", instanceID)

	sess, existing, err := s.receiveVMSessions.create(ctx, req)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create session: %v", err)
	}
	if existing {
		s.log.InfoContext(ctx, "InitReceiveVM: returning existing session (idempotent)",
			"session", sess.id, "instance", instanceID)
		return sess.ready, nil
	}

	sess.startReq = req
	resp, err := s.initReceiveVMSession(ctx, sess, req)
	if err != nil {
		sess.abort("init failed")
		sess.mu.Lock()
		if sess.state != recvStateDone && sess.state != recvStateFailed {
			sess.state = recvStateFailed
			sess.terminalAt = time.Now()
			if sess.rollback != nil {
				sess.rollback.Rollback()
			}
		}
		sess.mu.Unlock()
		s.receiveVMSessions.unreserveReqKey(sess.reqKey)
		return nil, err
	}

	sess.ready = resp
	sess.mu.Lock()
	sess.state = recvStateTransferring
	sess.touchLocked()
	sess.mu.Unlock()
	s.receiveVMSessions.publish(sess)

	return resp, nil
}

func (s *Service) initReceiveVMSession(ctx context.Context, sess *receiveVMSession, req *api.InitReceiveVMRequest) (*api.InitReceiveVMResponse, error) {
	instanceID := req.InstanceID

	if err := s.lockForMigration(instanceID); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "instance %s: %v", instanceID, err)
	}
	sess.unlockFn = func() { s.unlockMigration(instanceID) }

	if rs := s.context.ReplicationSuspender; rs != nil {
		rs.SuspendVolume(instanceID)
		oldUnlock := sess.unlockFn
		sess.unlockFn = func() {
			rs.ResumeVolume(instanceID)
			oldUnlock()
		}
		if rs.IsVolumeActive(instanceID) {
			s.log.InfoContext(ctx, "waiting on VM storage replication", "instance", instanceID)
			rs.WaitVolumeIdle(ctx, instanceID)
		}
	}

	if req.Live && req.SourceInstance != nil && req.SourceInstance.VMConfig != nil {
		requiredBytes := req.SourceInstance.VMConfig.Memory
		if err := checkAvailableMemory(requiredBytes); err != nil {
			mr := s.context.MemoryReclaimer
			if mr == nil {
				return nil, status.Errorf(codes.ResourceExhausted,
					"target host does not have enough memory for live restore: %v", err)
			}

			const maxAttempts = 3
			for attempt := 1; attempt <= maxAttempts; attempt++ {
				if reclaimErr := mr.ReclaimMemory(ctx, requiredBytes); reclaimErr != nil {
					s.log.WarnContext(ctx, "memory reclaim did not free enough",
						"instance", instanceID, "attempt", attempt, "error", reclaimErr)
				}
				if err := checkAvailableMemory(requiredBytes); err == nil {
					break
				} else if attempt == maxAttempts {
					return nil, status.Errorf(codes.ResourceExhausted,
						"target host does not have enough memory for live restore (even after reclaim): %v", err)
				}
			}
		}
	}

	if existing, err := s.loadInstanceConfig(instanceID); err == nil {
		if existing.State == api.VMState_CREATING {
			// Stale CREATING config from a previous crashed migration
			// attempt (we now persist a CREATING placeholder below to hold
			// the IPAM lease during transfer). Clean it up and continue so
			// the retry can proceed. Mirrors the stale-CREATING path in
			// create_instance.go:165–189.
			s.log.WarnContext(ctx, "found stale CREATING migration target, cleaning up", "id", instanceID)
			instanceDir := s.getInstanceDir(instanceID)
			if rmErr := os.RemoveAll(instanceDir); rmErr != nil {
				s.log.ErrorContext(ctx, "failed to clean up stale migration dir", "id", instanceID, "error", rmErr)
			}
			staleIP := ""
			staleMAC := ""
			if existing.VMConfig != nil && existing.VMConfig.NetworkInterface != nil {
				ni := existing.VMConfig.NetworkInterface
				staleMAC = ni.MACAddress
				if ni.IP != nil {
					if ipAddr, _, perr := net.ParseCIDR(ni.IP.IPV4); perr == nil {
						staleIP = ipAddr.String()
					}
				}
			}
			if delErr := s.context.NetworkManager.DeleteInterface(ctx, instanceID, staleIP, staleMAC); delErr != nil {
				s.log.DebugContext(ctx, "no network interface to clean up for stale migration", "id", instanceID)
			}
		} else {
			return nil, status.Errorf(codes.AlreadyExists, "instance %s already exists", instanceID)
		}
	} else if !errors.Is(err, api.ErrNotFound) {
		return nil, status.Errorf(codes.Internal, "failed to check instance existence: %v", err)
	}

	var orphanResumeToken string
	if _, err := s.context.StorageManager.Get(ctx, instanceID); err == nil {
		if req.DiscardOrphan {
			s.log.InfoContext(ctx, "discarding orphaned dataset (sender requested fresh start)", "instance", instanceID)
			if err := s.context.StorageManager.Delete(ctx, instanceID); err != nil {
				s.log.WarnContext(ctx, "failed to delete orphaned dataset, proceeding anyway", "instance", instanceID, "error", err)
			}
		} else {
			token, tokenErr := s.context.StorageManager.GetResumeToken(ctx, instanceID)
			if tokenErr != nil {
				s.log.WarnContext(ctx, "failed to get resume token from orphaned dataset", "instance", instanceID, "error", tokenErr)
			}
			if token != "" {
				s.log.InfoContext(ctx, "found resumable orphaned dataset from prior migration", "instance", instanceID, "token_len", len(token))
				orphanResumeToken = token
			} else {
				s.log.WarnContext(ctx, "destroying orphaned dataset from prior crashed migration", "instance", instanceID)
				if err := s.context.StorageManager.Delete(ctx, instanceID); err != nil {
					s.log.WarnContext(ctx, "failed to delete orphaned dataset, proceeding anyway", "instance", instanceID, "error", err)
				}
			}
		}
	}

	hasBaseImage := false
	if req.BaseImageID != "" {
		if _, err := s.context.StorageManager.Get(ctx, req.BaseImageID); err == nil {
			hasBaseImage = true
		}
	}

	if req.Live {
		var err error
		sess.targetNetwork, err = s.context.NetworkManager.CreateInterface(ctx, instanceID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to allocate network interface: %v", err)
		}
		sess.rollback.targetNetwork = sess.targetNetwork

		// Persist a CREATING-state placeholder so the IPAM reconciler keeps
		// this migration's lease alive through the minutes-long transfer
		// phase. Without this, the reconciler builds validIPs from on-disk
		// configs (listInstances) — the migration's lease isn't in any
		// config yet, so it looks like an orphan and gets released. The
		// allocation cursor makes immediate reassignment unlikely, but on a
		// pool wrap the IP could be handed to a new VM while the migrated
		// one still holds it, producing a delayed duplicate-IP conflict.
		// Persisting a CREATING config both puts the IP into validIPs and
		// trips the CREATING-state abort in reconcileIPLeasesFromInstances.
		// saveInstanceConfig creates the parent dir, so we mark
		// instanceDirCreated here so rollback tears the dir down on failure.
		sess.rollback.instanceDirCreated = true
		if err := s.persistMigrationPlaceholder(instanceID, sess.targetNetwork); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to persist migration placeholder: %v", err)
		}
	}

	if err := os.MkdirAll(s.getInstanceDir(instanceID), 0o770); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create instance dir: %v", err)
	}
	sess.rollback.instanceDirCreated = true

	if req.Encrypted && len(req.EncryptionKey) > 0 {
		if err := s.context.StorageManager.SetEncryptionKey(instanceID, req.EncryptionKey); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to store encryption key: %v", err)
		}
		sess.rollback.encryptionKeyCreated = true
	}

	sidebandAddr, err := sess.openSidebandListener(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to open sideband listener: %v", err)
	}

	skipIPReconfig := supportsIPIsolation(s.context.NetworkManager) && sameVMIP(req.SourceInstance, sess.targetNetwork)

	return &api.InitReceiveVMResponse{
		SessionID:      sess.id,
		HasBaseImage:   hasBaseImage,
		TargetNetwork:  sess.targetNetwork,
		SidebandAddr:   sidebandAddr,
		Resumable:      sidebandAddr != "",
		ResumeToken:    orphanResumeToken,
		SkipIpReconfig: skipIPReconfig,
	}, nil
}

func (s *Service) GetReceiveVMResumeToken(ctx context.Context, req *api.GetReceiveVMResumeTokenRequest) (*api.GetReceiveVMResumeTokenResponse, error) {
	sess := s.receiveVMSessions.getAndTouch(req.SessionID)
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionID)
	}

	sess.mu.Lock()
	state := sess.state
	sess.mu.Unlock()
	if state != recvStateTransferring {
		return nil, status.Errorf(codes.FailedPrecondition, "session in state %s, expected transferring", state)
	}

	sess.abortSideband()
	if err := sess.waitSideband(); err != nil {
		s.log.WarnContext(ctx, "sideband resume wait returned error", "instance", sess.instanceID, "error", err)
	}
	sess.zfsDatasetCreated = true
	sess.rollback.zfsDatasetCreated = true

	// Reset the hasher. On a broken sideband the send and receive sides
	// will have hashed different byte counts (bytes in flight at break
	// time), so accumulated hashes can never match. The send side resets
	// its hasher symmetrically before calling streamViaSidebandResume.
	sess.hasher.Reset()

	token, err := s.context.StorageManager.GetResumeToken(ctx, sess.instanceID)
	if err != nil {
		s.log.WarnContext(ctx, "failed to get resume token", "instance", sess.instanceID, "error", err)
	}

	resp := &api.GetReceiveVMResumeTokenResponse{Token: token}
	if token != "" {
		addr, err := sess.openSidebandListener(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to open resume sideband listener: %v", err)
		}
		resp.SidebandAddr = addr
	}
	return resp, nil
}

func (s *Service) AdvanceReceiveVMPhase(ctx context.Context, req *api.AdvanceReceiveVMPhaseRequest) (*api.AdvanceReceiveVMPhaseResponse, error) {
	sess := s.receiveVMSessions.getAndTouch(req.SessionID)
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionID)
	}

	sess.mu.Lock()
	state := sess.state
	sess.mu.Unlock()
	if state != recvStateTransferring {
		return nil, status.Errorf(codes.FailedPrecondition, "session in state %s, expected transferring", state)
	}

	if err := sess.waitSideband(); err != nil {
		return nil, status.Errorf(codes.Internal, "sideband phase: %v", err)
	}
	sess.zfsDatasetCreated = true
	sess.rollback.zfsDatasetCreated = true

	resp := &api.AdvanceReceiveVMPhaseResponse{}
	if !req.Last {
		addr, err := sess.openSidebandListener(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to open next sideband listener: %v", err)
		}
		resp.SidebandAddr = addr
	}
	return resp, nil
}

func (s *Service) UploadReceiveVMSnapshot(ctx context.Context, req *api.UploadReceiveVMSnapshotRequest) (*api.UploadReceiveVMSnapshotResponse, error) {
	sess := s.receiveVMSessions.getAndTouch(req.SessionID)
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionID)
	}

	sess.mu.Lock()
	state := sess.state
	sess.mu.Unlock()
	if state != recvStateTransferring {
		return nil, status.Errorf(codes.FailedPrecondition, "session in state %s, expected transferring", state)
	}

	if err := sess.writeSnapshotChunk(req.Filename, req.Data, req.Compressed); err != nil {
		return nil, status.Errorf(codes.Internal, "write snapshot chunk: %v", err)
	}
	return &api.UploadReceiveVMSnapshotResponse{}, nil
}

func (s *Service) CompleteReceiveVM(ctx context.Context, req *api.CompleteReceiveVMRequest) (*api.CompleteReceiveVMResponse, error) {
	sess := s.receiveVMSessions.getAndTouch(req.SessionID)
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionID)
	}

	sess.mu.Lock()
	state := sess.state
	if state == recvStateDone {
		result := sess.result
		sess.mu.Unlock()
		return result, nil
	}
	if state != recvStateTransferring {
		sess.mu.Unlock()
		return nil, status.Errorf(codes.FailedPrecondition, "session in state %s, expected transferring", state)
	}
	sess.state = recvStateCompleting
	sess.touchLocked()
	sess.mu.Unlock()

	result, err := s.completeReceiveVMSession(ctx, sess, req)
	if err != nil {
		sess.mu.Lock()
		sess.state = recvStateFailed
		sess.terminalAt = time.Now()
		if sess.rollback != nil {
			sess.rollback.Rollback()
		}
		sess.mu.Unlock()
		sess.abort("complete failed")
		return nil, err
	}

	sess.mu.Lock()
	sess.state = recvStateDone
	sess.result = result
	sess.terminalAt = time.Now()
	sess.mu.Unlock()

	sess.unlockOnce.Do(func() {
		if sess.unlockFn != nil {
			sess.unlockFn()
		}
	})

	return result, nil
}

func (s *Service) completeReceiveVMSession(ctx context.Context, sess *receiveVMSession, req *api.CompleteReceiveVMRequest) (*api.CompleteReceiveVMResponse, error) {
	instanceID := sess.instanceID
	startReq := sess.startReq

	if err := sess.waitSideband(); err != nil {
		return nil, status.Errorf(codes.Internal, "sideband final phase: %v", err)
	}
	sess.zfsDatasetCreated = true
	sess.rollback.zfsDatasetCreated = true

	actualChecksum := fmt.Sprintf("%x", sess.hasher.Sum(nil))
	if actualChecksum != req.Checksum {
		return nil, status.Errorf(codes.DataLoss,
			"checksum mismatch: expected %s, got %s", req.Checksum, actualChecksum)
	}

	if s.receiveFaultCrashAfterData.CompareAndSwap(true, false) {
		s.log.WarnContext(ctx, "fault injection: simulating crash after data receive", "instance", instanceID)
		return nil, status.Errorf(codes.Internal, "fault injection: simulated crash after data receive")
	}

	instanceDir := s.getInstanceDir(instanceID)
	if err := s.setupKernelForMigration(ctx, startReq.SourceInstance.VMConfig, instanceDir); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to setup kernel: %v", err)
	}

	sourceInstance := startReq.SourceInstance
	if startReq.Live {
		newInstance, coldBooted, err := s.finalizeLiveReceive(ctx, instanceID, instanceDir, sess.snapshotDir, sourceInstance, startReq.GroupID, sess.targetNetwork, sess.rollback)
		if err != nil {
			return nil, err
		}
		go s.cleanupMigrationSnapshots(context.Background(), instanceID)
		return &api.CompleteReceiveVMResponse{Instance: newInstance, ColdBooted: coldBooted}, nil
	}

	newInstance := &api.Instance{
		ID:        instanceID,
		Name:      sourceInstance.Name,
		Image:     sourceInstance.Image,
		VMConfig:  s.adaptVMConfigForTarget(sourceInstance.VMConfig, instanceID, instanceDir),
		CreatedAt: sourceInstance.CreatedAt,
		UpdatedAt: time.Now().UnixNano(),
		State:     api.VMState_STOPPED,
		GroupID:   startReq.GroupID,
	}
	if err := s.saveInstanceConfig(newInstance); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to save instance config: %v", err)
	}
	go s.cleanupMigrationSnapshots(context.Background(), instanceID)
	return &api.CompleteReceiveVMResponse{Instance: newInstance}, nil
}

func (s *Service) AbortReceiveVM(ctx context.Context, req *api.AbortReceiveVMRequest) (*api.AbortReceiveVMResponse, error) {
	sess := s.receiveVMSessions.getAndTouch(req.SessionID)
	if sess == nil {
		return &api.AbortReceiveVMResponse{}, nil
	}

	s.log.InfoContext(ctx, "AbortReceiveVM", "session", sess.id, "instance", sess.instanceID, "reason", req.Reason)
	sess.abortSideband()
	sess.abort(req.Reason)

	sess.mu.Lock()
	if sess.state != recvStateDone && sess.state != recvStateFailed {
		sess.state = recvStateFailed
		sess.terminalAt = time.Now()
		if sess.rollback != nil {
			sess.rollback.Rollback()
		}
	}
	sess.mu.Unlock()

	s.receiveVMSessions.remove(sess.id)
	return &api.AbortReceiveVMResponse{}, nil
}

// persistMigrationPlaceholder writes a CREATING-state instance config for an
// in-flight live migration target. The placeholder serves two purposes
// during the transfer phase (which can take minutes):
//
//  1. listInstances includes the placeholder, so the allocated IP appears in
//     reconcileIPLeasesFromInstances' validIPs set instead of looking like
//     an orphan lease.
//  2. State=CREATING trips the transient-state abort in the reconciler, so
//     even broken in-flight migrations won't have their leases released.
//
// finalizeLiveReceive and completeReceiveVMSession overwrite this config
// with the final RUNNING/STOPPED version on completion. On crash, the
// stale-CREATING cleanup in InitReceiveVM clears it on the next retry.
func (s *Service) persistMigrationPlaceholder(instanceID string, iface *api.NetworkInterface) error {
	now := time.Now().UnixNano()
	return s.saveInstanceConfig(&api.Instance{
		ID:        instanceID,
		State:     api.VMState_CREATING,
		Node:      s.config.Name,
		CreatedAt: now,
		UpdatedAt: now,
		VMConfig:  &api.VMConfig{NetworkInterface: iface},
	})
}
