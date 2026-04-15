package compute

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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
		stream := &sessionSendVMStream{ctx: sess.ctx, sender: sender}
		startReq := &api.SendVMStartRequest{
			InstanceID:         req.InstanceID,
			TargetHasBaseImage: req.TargetHasBaseImage,
			TwoPhase:           req.TwoPhase,
			Live:               req.Live,
			AcceptStatus:       true,
			TargetAddress:      req.TargetAddress,
			TargetGroupID:      req.TargetGroupID,
		}
		err := s.runSendVM(stream, startReq)
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

type sessionSendVMStream struct {
	ctx            context.Context
	sender         migrationSender
	pendingControl *api.SendVMControl
}

func (s *sessionSendVMStream) Context() context.Context { return s.ctx }

func (s *sessionSendVMStream) Send(resp *api.SendVMResponse) error {
	switch v := resp.Type.(type) {
	case *api.SendVMResponse_Metadata:
		return s.sender.EmitMetadata(v.Metadata)
	case *api.SendVMResponse_TargetReady:
		return s.sender.EmitTargetReady(v.TargetReady)
	case *api.SendVMResponse_Status:
		return s.sender.EmitStatus(v.Status.Message)
	case *api.SendVMResponse_Progress:
		return s.sender.EmitProgress(uint64(v.Progress.BytesSent))
	case *api.SendVMResponse_AwaitControl:
		control, err := s.sender.EmitAwaitControl(v.AwaitControl)
		if err != nil {
			return err
		}
		s.pendingControl = control
		return nil
	case *api.SendVMResponse_Result:
		return s.sender.EmitResult(v.Result)
	case *api.SendVMResponse_Complete:
		return nil
	case *api.SendVMResponse_PhaseComplete:
		return nil
	case *api.SendVMResponse_Data:
		return fmt.Errorf("unexpected data frame in session sender")
	case *api.SendVMResponse_SnapshotData:
		return fmt.Errorf("unexpected snapshot frame in session sender")
	default:
		return fmt.Errorf("unsupported send vm response type %T", resp.Type)
	}
}

func (s *sessionSendVMStream) Recv() (*api.SendVMRequest, error) {
	if s.pendingControl == nil {
		return nil, fmt.Errorf("no pending control available")
	}
	control := s.pendingControl
	s.pendingControl = nil
	return &api.SendVMRequest{Type: &api.SendVMRequest_Control{Control: control}}, nil
}

func (s *sessionSendVMStream) SendHeader(metadata.MD) error { return nil }
func (s *sessionSendVMStream) SetHeader(metadata.MD) error  { return nil }
func (s *sessionSendVMStream) SetTrailer(metadata.MD)       {}
func (s *sessionSendVMStream) SendMsg(any) error            { return nil }
func (s *sessionSendVMStream) RecvMsg(any) error            { return nil }
