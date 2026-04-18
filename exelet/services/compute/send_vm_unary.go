package compute

import (
	"context"
	"fmt"
	"runtime/debug"
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

	go s.runSendVMSession(sess, req)

	return sess.initResp, nil
}

// runSendVMSession drives runSendVM for an async InitSendVM session and
// guarantees a terminal SendVMEvent_Result is emitted on every exit path
// (normal return, error return, or panic). Without this guarantee, exed's
// PollSendVM loop — which only returns when it sees a Result event or a
// Completed response — would poll forever if the goroutine exited silently.
func (s *Service) runSendVMSession(sess *sendVMSession, req *api.InitSendVMRequest) {
	sender := &sessionMigrationSender{sess: sess}

	var runErr error
	var panicVal any
	var panicStack []byte
	defer func() {
		// Recover inside the defer so we observe a panic without letting
		// it escape and kill the process.
		if r := recover(); r != nil {
			panicVal = r
			panicStack = debug.Stack()
		}
		s.finalizeSendVMSession(sess, sender, runErr, panicVal, panicStack)
	}()

	startReq := &api.SendVMStartRequest{
		InstanceID:         req.InstanceID,
		TargetHasBaseImage: req.TargetHasBaseImage,
		TwoPhase:           req.TwoPhase,
		Live:               req.Live,
		AcceptStatus:       true,
		TargetAddress:      req.TargetAddress,
		TargetGroupID:      req.TargetGroupID,
	}
	runErr = s.runSendVM(sender, startReq)
}

// finalizeSendVMSession emits a terminal SendVMResult if the session has
// not already been completed by runSendVM itself. Extracted for testability.
func (s *Service) finalizeSendVMSession(sess *sendVMSession, sender migrationSender, runErr error, panicVal any, panicStack []byte) {
	sess.mu.Lock()
	completed := sess.completed
	sess.mu.Unlock()
	if completed {
		return
	}

	var errMsg string
	switch {
	case panicVal != nil:
		s.log.Error("SendVM goroutine panicked", "instance", sess.instanceID, "session", sess.id, "panic", panicVal, "stack", string(panicStack))
		errMsg = fmt.Sprintf("sendVM panic: %v", panicVal)
	case runErr != nil:
		errMsg = runErr.Error()
	default:
		s.log.Error("SendVM goroutine exited without emitting result", "instance", sess.instanceID, "session", sess.id)
		errMsg = "sendVM exited without result"
	}
	_ = sender.EmitResult(&api.SendVMResult{Error: errMsg})
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
