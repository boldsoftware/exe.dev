package compute

import (
	"context"

	api "exe.dev/pkg/api/exe/compute/v1"
)

type migrationSender interface {
	EmitMetadata(*api.SendVMMetadata) error
	EmitTargetReady(*api.SendVMTargetReady) error
	EmitStatus(string) error
	EmitProgress(uint64) error
	EmitAwaitControl(*api.SendVMAwaitControl) (*api.SendVMControl, error)
	EmitResult(*api.SendVMResult) error
	Context() context.Context
}

type sessionMigrationSender struct {
	sess *sendVMSession
}

func (s *sessionMigrationSender) EmitMetadata(m *api.SendVMMetadata) error {
	s.sess.emit(&api.SendVMEvent{Type: &api.SendVMEvent_Metadata{Metadata: m}})
	return s.checkCancelled()
}

func (s *sessionMigrationSender) EmitTargetReady(tr *api.SendVMTargetReady) error {
	s.sess.emit(&api.SendVMEvent{Type: &api.SendVMEvent_TargetReady{TargetReady: tr}})
	return s.checkCancelled()
}

func (s *sessionMigrationSender) EmitStatus(msg string) error {
	s.sess.emit(&api.SendVMEvent{Type: &api.SendVMEvent_Status{Status: &api.SendVMStatus{Message: msg}}})
	return s.checkCancelled()
}

func (s *sessionMigrationSender) EmitProgress(bytesSent uint64) error {
	s.sess.emit(&api.SendVMEvent{Type: &api.SendVMEvent_Progress{Progress: &api.SendVMProgress{BytesSent: int64(bytesSent)}}})
	return s.checkCancelled()
}

func (s *sessionMigrationSender) EmitAwaitControl(ac *api.SendVMAwaitControl) (*api.SendVMControl, error) {
	seq := s.sess.emitAwaitControl(ac)
	select {
	case <-s.sess.ctx.Done():
		return nil, s.sess.ctx.Err()
	case control := <-s.sess.controlCh:
		s.sess.mu.Lock()
		if s.sess.pendingAwaitSeq == seq {
			s.sess.pendingAwaitSeq = 0
		}
		s.sess.mu.Unlock()
		return control, nil
	}
}

func (s *sessionMigrationSender) EmitResult(r *api.SendVMResult) error {
	s.sess.emit(&api.SendVMEvent{Type: &api.SendVMEvent_Result{Result: r}})
	return nil
}

func (s *sessionMigrationSender) Context() context.Context { return s.sess.ctx }

func (s *sessionMigrationSender) checkCancelled() error {
	select {
	case <-s.sess.ctx.Done():
		return s.sess.ctx.Err()
	default:
		return nil
	}
}
