package deploy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// blockingRunner returns a runDeploy function that drives a deploy through
// "build" and then blocks in "upload" until ctx is cancelled. It signals
// startedCh once the upload step is reached so tests can synchronize on
// "deploy is mid-flight" without sleeping.
func blockingRunner(startedCh chan<- struct{}) func(ctx context.Context, d *deploy) {
	var once sync.Once
	return func(ctx context.Context, d *deploy) {
		d.beginStep("build")
		if d.stepDone(nil) {
			return
		}
		d.beginStep("upload")
		once.Do(func() { close(startedCh) })
		<-ctx.Done()
		d.stepDone(ctx.Err())
	}
}

func TestCancel_RunningDeployTransitionsToCancelled(t *testing.T) {
	m := newTestManager(t)
	startedCh := make(chan struct{})
	m.runDeploy = blockingRunner(startedCh)

	st, err := m.Start(Request{
		Stage: "staging", Role: "exelet", Process: "exeletd",
		Host: "h1", DNSName: "h1.test", SHA: validSHA,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait until the deploy is blocked in upload.
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("deploy did not reach upload step")
	}

	if err := m.Cancel(st.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	final := waitTerminalDeploy(t, m, st.ID, 2*time.Second)
	if final.State != "cancelled" {
		t.Fatalf("state = %q, want cancelled", final.State)
	}
	if final.Error != "cancelled by user" {
		t.Errorf("error = %q, want %q", final.Error, "cancelled by user")
	}
	// The currently-running step should also be marked cancelled.
	var sawCancelledStep bool
	for _, step := range final.Steps {
		if step.Status == "cancelled" {
			sawCancelledStep = true
			break
		}
	}
	if !sawCancelledStep {
		t.Errorf("expected at least one step in cancelled status, got %+v", final.Steps)
	}
}

func TestCancel_TerminalDeployIsNoOp(t *testing.T) {
	m := newTestManager(t)
	m.runDeploy = func(ctx context.Context, d *deploy) {
		d.beginStep("build")
		d.stepDone(nil)
		for {
			d.mu.Lock()
			var next string
			for _, s := range d.steps {
				if s.Status == "pending" {
					next = s.Name
					break
				}
			}
			d.mu.Unlock()
			if next == "" {
				break
			}
			d.beginStep(next)
			d.stepDone(nil)
		}
		d.complete()
	}

	st, err := m.Start(Request{
		Stage: "staging", Role: "exelet", Process: "exeletd",
		Host: "h1", DNSName: "h1.test", SHA: validSHA,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final := waitTerminalDeploy(t, m, st.ID, 2*time.Second)
	if final.State != "done" {
		t.Fatalf("state = %q, want done", final.State)
	}

	// Cancel after terminal: must be a no-op (no error, state unchanged).
	if err := m.Cancel(st.ID); err != nil {
		t.Fatalf("Cancel after terminal: %v", err)
	}
	after, ok := m.Get(st.ID)
	if !ok {
		t.Fatal("deploy vanished after cancel")
	}
	if after.State != "done" {
		t.Errorf("state after no-op cancel = %q, want done", after.State)
	}
}

func TestCancel_DoubleCancelIsIdempotent(t *testing.T) {
	m := newTestManager(t)
	startedCh := make(chan struct{})
	m.runDeploy = blockingRunner(startedCh)

	st, err := m.Start(Request{
		Stage: "staging", Role: "exelet", Process: "exeletd",
		Host: "h1", DNSName: "h1.test", SHA: validSHA,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("deploy did not reach upload step")
	}

	if err := m.Cancel(st.ID); err != nil {
		t.Fatalf("first Cancel: %v", err)
	}
	if err := m.Cancel(st.ID); err != nil {
		t.Fatalf("second Cancel: %v", err)
	}
	final := waitTerminalDeploy(t, m, st.ID, 2*time.Second)
	if final.State != "cancelled" {
		t.Fatalf("state = %q, want cancelled", final.State)
	}
}

func TestCancel_UnknownIDReturnsError(t *testing.T) {
	m := newTestManager(t)
	if err := m.Cancel("does-not-exist"); err == nil {
		t.Fatal("expected error for unknown id")
	}
}

func TestCancel_RolloutOwnedDeployRejected(t *testing.T) {
	m := newTestManager(t)
	startedCh := make(chan struct{})
	m.runDeploy = blockingRunner(startedCh)

	rs, err := m.StartRollout(RolloutRequest{
		Targets: []RolloutTarget{
			mkTarget("a", "fra2", "staging"),
		},
	})
	if err != nil {
		t.Fatalf("StartRollout: %v", err)
	}

	// Wait until the rollout's only deploy is mid-flight.
	deadline := time.Now().Add(2 * time.Second)
	var deployID string
	for time.Now().Before(deadline) && deployID == "" {
		s, ok := m.GetRollout(rs.ID)
		if ok && len(s.Waves) > 0 && len(s.Waves[0].Targets) > 0 {
			deployID = s.Waves[0].Targets[0].DeployID
		}
		time.Sleep(10 * time.Millisecond)
	}
	if deployID == "" {
		t.Fatal("rollout did not publish a deploy id")
	}

	err = m.Cancel(deployID)
	if !errors.Is(err, ErrDeployRolloutOwned) {
		t.Fatalf("Cancel rollout-owned deploy err = %v, want ErrDeployRolloutOwned", err)
	}

	// Cancel via rollout to clean up.
	if err := m.CancelRollout(rs.ID); err != nil {
		t.Fatalf("CancelRollout: %v", err)
	}
	waitTerminalRollout(t, m, rs.ID, 3*time.Second)
}

// waitTerminalDeploy polls for a deploy to reach a terminal state.
func waitTerminalDeploy(t *testing.T, m *Manager, id string, timeout time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s, ok := m.Get(id)
		if !ok {
			t.Fatalf("deploy %s vanished", id)
		}
		if s.State == "done" || s.State == "failed" || s.State == "cancelled" {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("deploy %s did not reach terminal state in %v", id, timeout)
	return Status{}
}
