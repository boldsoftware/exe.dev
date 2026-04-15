package compute

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *Service) InitSendVM(ctx context.Context, req *api.InitSendVMRequest) (*api.InitSendVMResponse, error) {
	if req.InstanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id is required")
	}
	if req.TargetAddress == "" {
		return nil, status.Error(codes.InvalidArgument, "target_address is required for unary SendVM")
	}

	sess, existing, err := s.sendVMSessions.create(req)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create session: %v", err)
	}
	if existing {
		return sess.initResp, nil
	}

	go func() {
		sender := &sessionMigrationSender{sess: sess}
		startReq := &api.SendVMStartRequest{
			InstanceID:         req.InstanceID,
			TargetHasBaseImage: req.TargetHasBaseImage,
			TwoPhase:           req.TwoPhase,
			Live:               req.Live,
			AcceptStatus:       true,
			TargetAddress:      req.TargetAddress,
			TargetGroupID:      req.TargetGroupID,
		}
		err := s.runSendVM(sender, startReq)
		if err != nil {
			sess.mu.Lock()
			completed := sess.completed
			sess.mu.Unlock()
			if !completed {
				_ = sender.EmitResult(&api.SendVMResult{Error: err.Error()})
			}
		}
	}()

	return sess.initResp, nil
}

func (s *Service) PollSendVM(ctx context.Context, req *api.PollSendVMRequest) (*api.PollSendVMResponse, error) {
	sess := s.sendVMSessions.getAndTouch(req.SessionID)
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionID)
	}
	resp, err := sess.poll(ctx, req.AfterSeq, time.Duration(req.MaxWaitMs)*time.Millisecond)
	if err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return resp, nil
}

func (s *Service) SubmitSendVMControl(ctx context.Context, req *api.SubmitSendVMControlRequest) (*api.SubmitSendVMControlResponse, error) {
	sess := s.sendVMSessions.getAndTouch(req.SessionID)
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %s not found", req.SessionID)
	}

	sess.mu.Lock()
	pendingSeq := sess.pendingAwaitSeq
	sess.mu.Unlock()
	if pendingSeq == 0 {
		return nil, status.Error(codes.FailedPrecondition, "no pending AwaitControl")
	}
	if req.AwaitSeq != pendingSeq {
		return nil, status.Errorf(codes.FailedPrecondition, "await_seq mismatch: expected %d, got %d", pendingSeq, req.AwaitSeq)
	}

	select {
	case sess.controlCh <- req.Control:
		sess.mu.Lock()
		sess.pendingAwaitSeq = 0
		sess.mu.Unlock()
		return &api.SubmitSendVMControlResponse{}, nil
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	case <-sess.ctx.Done():
		return nil, status.Error(codes.Aborted, "session cancelled")
	}
}

func (s *Service) AbortSendVM(ctx context.Context, req *api.AbortSendVMRequest) (*api.AbortSendVMResponse, error) {
	sess := s.sendVMSessions.get(req.SessionID)
	if sess == nil {
		return &api.AbortSendVMResponse{}, nil
	}
	reason := req.Reason
	if reason == "" {
		reason = "aborted by client"
	}
	s.log.InfoContext(ctx, "AbortSendVM", "session", sess.id, "instance", sess.instanceID, "reason", reason)
	sess.abort(reason)
	s.sendVMSessions.remove(sess.id)
	return &api.AbortSendVMResponse{}, nil
}
