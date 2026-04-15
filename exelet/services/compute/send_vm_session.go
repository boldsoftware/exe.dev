package compute

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	api "exe.dev/pkg/api/exe/compute/v1"
)

const (
	sendVMIdleTTL         = 2 * time.Minute
	sendVMTerminalTTL     = 10 * time.Minute
	sendVMJanitorTick     = 30 * time.Second
	sendVMEventRetention  = 1000
	sendVMPollWatchdogTTL = 2 * time.Minute
)

type sendVMSessionManager struct {
	log     *slog.Logger
	service *Service

	mu       sync.Mutex
	sessions map[string]*sendVMSession
	byReqKey map[string]string
}

type sendVMSession struct {
	id         string
	instanceID string
	reqKey     string
	createdAt  time.Time

	ctx    context.Context
	cancel context.CancelCauseFunc

	unlockOnce sync.Once
	unlockFn   func()

	mu                sync.Mutex
	events            []*api.SendVMEvent
	nextSeq           uint64
	completed         bool
	lastActivity      time.Time
	terminalAt        time.Time
	waitCh            chan struct{}
	pendingAwaitSeq   uint64
	controlCh         chan *api.SendVMControl
	firstEventAt      time.Time
	lastPollAt        time.Time
	watchdogTriggered bool
	initResp          *api.InitSendVMResponse
}

func newSendVMSessionManager(log *slog.Logger, service *Service) *sendVMSessionManager {
	return &sendVMSessionManager{log: log, service: service, sessions: make(map[string]*sendVMSession), byReqKey: make(map[string]string)}
}

func (m *sendVMSessionManager) create(req *api.InitSendVMRequest) (*sendVMSession, bool, error) {
	reqKey := ""
	if req.ClientRequestID != "" {
		reqKey = req.InstanceID + ":" + req.ClientRequestID
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if reqKey != "" {
		if existingID, ok := m.byReqKey[reqKey]; ok {
			if existing, ok := m.sessions[existingID]; ok {
				existing.mu.Lock()
				existing.lastActivity = time.Now()
				existing.mu.Unlock()
				return existing, true, nil
			}
			delete(m.byReqKey, reqKey)
		}
	}

	sessionID, err := generateMigrationSessionID()
	if err != nil {
		return nil, false, err
	}
	now := time.Now()
	sessCtx, cancel := context.WithCancelCause(context.Background())
	sess := &sendVMSession{
		id:           sessionID,
		instanceID:   req.InstanceID,
		reqKey:       reqKey,
		createdAt:    now,
		ctx:          sessCtx,
		cancel:       cancel,
		nextSeq:      1,
		lastActivity: now,
		waitCh:       make(chan struct{}),
		controlCh:    make(chan *api.SendVMControl, 1),
		initResp:     &api.InitSendVMResponse{SessionID: sessionID},
	}
	m.sessions[sessionID] = sess
	if reqKey != "" {
		m.byReqKey[reqKey] = sessionID
	}
	return sess, false, nil
}

func (m *sendVMSessionManager) get(sessionID string) *sendVMSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[sessionID]
}

func (m *sendVMSessionManager) getAndTouch(sessionID string) *sendVMSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.sessions[sessionID]
	if sess != nil {
		sess.mu.Lock()
		now := time.Now()
		sess.lastActivity = now
		sess.lastPollAt = now
		sess.mu.Unlock()
	}
	return sess
}

func (m *sendVMSessionManager) remove(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.sessions[sessionID]; ok {
		if sess.reqKey != "" {
			delete(m.byReqKey, sess.reqKey)
		}
		delete(m.sessions, sessionID)
	}
}

func (m *sendVMSessionManager) abortAll() {
	m.mu.Lock()
	sessions := make([]*sendVMSession, 0, len(m.sessions))
	for _, sess := range m.sessions {
		sessions = append(sessions, sess)
	}
	m.mu.Unlock()
	for _, sess := range sessions {
		sess.abort("service shutting down")
	}
}

func (m *sendVMSessionManager) janitor(ctx context.Context) {
	ticker := time.NewTicker(sendVMJanitorTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reap()
		}
	}
}

func (m *sendVMSessionManager) reap() {
	now := time.Now()
	m.mu.Lock()
	toReap := make([]string, 0)
	for id, sess := range m.sessions {
		sess.mu.Lock()
		expired := false
		if sess.completed {
			expired = !sess.terminalAt.IsZero() && now.Sub(sess.terminalAt) > sendVMTerminalTTL
		} else {
			expired = now.Sub(sess.lastActivity) > sendVMIdleTTL
			if !expired && !sess.firstEventAt.IsZero() && !sess.watchdogTriggered {
				lastPollAt := sess.lastPollAt
				if lastPollAt.IsZero() {
					lastPollAt = sess.firstEventAt
				}
				if now.Sub(lastPollAt) > sendVMPollWatchdogTTL {
					sess.watchdogTriggered = true
					expired = true
				}
			}
		}
		sess.mu.Unlock()
		if expired {
			toReap = append(toReap, id)
		}
	}
	m.mu.Unlock()

	for _, id := range toReap {
		sess := m.get(id)
		if sess == nil {
			continue
		}
		if sess.watchdogExpired(now) {
			m.log.Warn("cancelling send session due to poll liveness watchdog", "session", id, "instance", sess.instanceID)
			sess.abort("poll liveness watchdog expired")
		} else {
			m.log.Warn("reaping expired send session", "session", id, "instance", sess.instanceID)
			sess.abort("session expired")
		}
		m.remove(id)
	}
}

func (s *sendVMSession) watchdogExpired(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completed || s.firstEventAt.IsZero() || s.watchdogTriggered {
		return s.watchdogTriggered
	}
	lastPollAt := s.lastPollAt
	if lastPollAt.IsZero() {
		lastPollAt = s.firstEventAt
	}
	return now.Sub(lastPollAt) > sendVMPollWatchdogTTL
}

func (s *sendVMSession) emit(event *api.SendVMEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event.Seq = s.nextSeq
	s.nextSeq++
	if event.GetResult() != nil {
		s.completed = true
		s.terminalAt = time.Now()
	}
	if s.firstEventAt.IsZero() {
		now := time.Now()
		s.firstEventAt = now
		s.lastPollAt = now
	}
	if event.GetProgress() != nil && len(s.events) > 0 {
		if s.events[len(s.events)-1].GetProgress() != nil {
			s.events[len(s.events)-1] = event // replace, don't mutate
			ch := s.waitCh
			s.waitCh = make(chan struct{})
			close(ch)
			return
		}
	}
	s.events = append(s.events, event)
	if len(s.events) > sendVMEventRetention {
		s.events = append([]*api.SendVMEvent(nil), s.events[len(s.events)-sendVMEventRetention:]...)
	}
	ch := s.waitCh
	s.waitCh = make(chan struct{})
	close(ch)
}

func (s *sendVMSession) emitAwaitControl(ac *api.SendVMAwaitControl) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	event := &api.SendVMEvent{Seq: s.nextSeq, Type: &api.SendVMEvent_AwaitControl{AwaitControl: ac}}
	s.nextSeq++
	if s.firstEventAt.IsZero() {
		now := time.Now()
		s.firstEventAt = now
		s.lastPollAt = now
	}
	s.events = append(s.events, event)
	if len(s.events) > sendVMEventRetention {
		s.events = append([]*api.SendVMEvent(nil), s.events[len(s.events)-sendVMEventRetention:]...)
	}
	s.pendingAwaitSeq = event.Seq
	ch := s.waitCh
	s.waitCh = make(chan struct{})
	close(ch)
	return event.Seq
}

func (s *sendVMSession) eventsSinceLocked(afterSeq uint64) []*api.SendVMEvent {
	if len(s.events) == 0 {
		return nil
	}
	if afterSeq == 0 {
		out := make([]*api.SendVMEvent, len(s.events))
		copy(out, s.events)
		return out
	}
	var result []*api.SendVMEvent
	for _, e := range s.events {
		if e.Seq > afterSeq {
			result = append(result, e)
		}
	}
	return result
}

func (s *sendVMSession) poll(ctx context.Context, afterSeq uint64, maxWait time.Duration) (*api.PollSendVMResponse, error) {
	if maxWait > 30*time.Second {
		maxWait = 30 * time.Second
	}
	if maxWait < 0 {
		maxWait = 0
	}
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	for {
		s.mu.Lock()
		now := time.Now()
		s.lastActivity = now
		s.lastPollAt = now
		events := s.eventsSinceLocked(afterSeq)
		if len(events) > 0 || s.completed {
			completed := s.completed
			s.mu.Unlock()
			return &api.PollSendVMResponse{Events: events, Completed: completed}, nil
		}
		waitCh := s.waitCh
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return &api.PollSendVMResponse{}, nil
		case <-waitCh:
		}
	}
}

func (s *sendVMSession) abort(reason string) {
	s.cancel(fmt.Errorf("%s", reason))
	s.unlockOnce.Do(func() {
		if s.unlockFn != nil {
			s.unlockFn()
		}
	})
}
