package compute

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func newTestSendVMSession() *sendVMSession {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &sendVMSession{
		id:         "test-session",
		instanceID: "test-instance",
		ctx:        ctx,
		cancel:     cancel,
		nextSeq:    1,
		waitCh:     make(chan struct{}),
		controlCh:  make(chan *api.SendVMControl, 1),
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestFinalizeSendVMSessionEmitsOnError covers the normal error path: if
// runSendVM returned an error and no result has been emitted yet, the
// finalizer emits one so exed's PollSendVM sees Completed.
func TestFinalizeSendVMSessionEmitsOnError(t *testing.T) {
	t.Parallel()

	sess := newTestSendVMSession()
	sender := &sessionMigrationSender{sess: sess}
	svc := &Service{log: discardLogger(), mu: &sync.Mutex{}}

	svc.finalizeSendVMSession(sess, sender, fmt.Errorf("boom: target vanished"), nil, nil)

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.completed {
		t.Fatal("session was not marked completed")
	}
	if len(sess.events) != 1 {
		t.Fatalf("expected 1 emitted event, got %d", len(sess.events))
	}
	r := sess.events[0].GetResult()
	if r == nil {
		t.Fatalf("expected Result event, got %T", sess.events[0].Type)
	}
	if !strings.Contains(r.Error, "boom: target vanished") {
		t.Fatalf("result error = %q, want it to contain the run error", r.Error)
	}
}

// TestFinalizeSendVMSessionEmitsOnPanic covers the recover path: if
// runSendVM panicked, the finalizer must still emit a terminal result.
func TestFinalizeSendVMSessionEmitsOnPanic(t *testing.T) {
	t.Parallel()

	sess := newTestSendVMSession()
	sender := &sessionMigrationSender{sess: sess}
	svc := &Service{log: discardLogger(), mu: &sync.Mutex{}}

	svc.finalizeSendVMSession(sess, sender, nil, "nil map write somewhere", []byte("goroutine 1 [running]:\n..."))

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.completed {
		t.Fatal("session was not marked completed")
	}
	r := sess.events[len(sess.events)-1].GetResult()
	if r == nil {
		t.Fatalf("expected Result event, got %T", sess.events[len(sess.events)-1].Type)
	}
	if !strings.Contains(r.Error, "sendVM panic") || !strings.Contains(r.Error, "nil map write") {
		t.Fatalf("result error = %q, want panic prefix + value", r.Error)
	}
}

// TestFinalizeSendVMSessionEmitsOnSilentExit covers the invariant-violation
// path: runSendVM returned nil error but never emitted a result (e.g. a
// future bug). The finalizer must still terminate the session.
func TestFinalizeSendVMSessionEmitsOnSilentExit(t *testing.T) {
	t.Parallel()

	sess := newTestSendVMSession()
	sender := &sessionMigrationSender{sess: sess}
	svc := &Service{log: discardLogger(), mu: &sync.Mutex{}}

	svc.finalizeSendVMSession(sess, sender, nil, nil, nil)

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.completed {
		t.Fatal("session was not marked completed")
	}
	r := sess.events[len(sess.events)-1].GetResult()
	if r == nil {
		t.Fatal("expected Result event")
	}
	if !strings.Contains(r.Error, "without result") {
		t.Fatalf("result error = %q, want to mention silent exit", r.Error)
	}
}

// TestFinalizeSendVMSessionSkipsIfAlreadyCompleted ensures we don't
// double-emit a result when runSendVM succeeded and already emitted one.
func TestFinalizeSendVMSessionSkipsIfAlreadyCompleted(t *testing.T) {
	t.Parallel()

	sess := newTestSendVMSession()
	sender := &sessionMigrationSender{sess: sess}
	svc := &Service{log: discardLogger(), mu: &sync.Mutex{}}

	// Simulate runSendVM's successful terminal emit.
	if err := sender.EmitResult(&api.SendVMResult{}); err != nil {
		t.Fatalf("prime emit: %v", err)
	}

	svc.finalizeSendVMSession(sess, sender, nil, nil, nil)

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if n := len(sess.events); n != 1 {
		t.Fatalf("expected 1 event (no double emit), got %d", n)
	}
}

func TestExtractBaseImageID(t *testing.T) {
	tests := []struct {
		origin string
		want   string
	}{
		{"", ""},
		{"tank/sha256:abc123@snap", "sha256:abc123"},
		{"tank/e1e-XXXX/sha256:abc123@snap", "sha256:abc123"},
		{"tank/a/b/sha256:abc123@snap", "sha256:abc123"},
		{"sha256:abc123@snap", "sha256:abc123"},
		{"tank/sha256:abc123", "sha256:abc123"},
		{"tank/instance-id@migration", "instance-id"},
	}
	for _, tt := range tests {
		got := extractBaseImageID(tt.origin)
		if got != tt.want {
			t.Errorf("extractBaseImageID(%q) = %q, want %q", tt.origin, got, tt.want)
		}
	}
}

func TestIsStaleResumeTokenErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"generic error", fmt.Errorf("something broke"), false},
		{"connection reset", fmt.Errorf("write tcp: connection reset by peer"), false},
		{"cannot resume send", fmt.Errorf("zfs send -t: zfs send failed: exit status 255 (cannot resume send: 'tank/vm@snap' used in the initial send)"), true},
		{"no longer same snapshot", fmt.Errorf("zfs send -t: zfs send failed: exit status 255 (cannot resume send: 'tank/vm@migration-pre' is no longer the same snapshot used in the initial send\n)"), true},
		{"wrapped", fmt.Errorf("sideband: %w", fmt.Errorf("cannot resume send: stale")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isStaleResumeTokenErr(tt.err)
			if got != tt.want {
				t.Errorf("isStaleResumeTokenErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
