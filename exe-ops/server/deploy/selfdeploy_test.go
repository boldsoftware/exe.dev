package deploy

import (
	"errors"
	"testing"
	"time"
)

// mkExeOpsRequest returns a Request that will deploy the exe-ops server itself.
func mkExeOpsRequest() Request {
	return Request{
		Stage:   "prod",
		Role:    "exe-ops",
		Process: "exe-ops",
		Host:    "exe-ops",
		DNSName: "exe-ops.test",
		SHA:     validSHA,
	}
}

func TestSelfDeploy_RefusedWhileOtherDeployActive(t *testing.T) {
	m := newTestManager(t)
	startedCh := make(chan struct{})
	m.runDeploy = blockingRunner(startedCh)

	other, err := m.Start(Request{
		Stage: "staging", Role: "exelet", Process: "exeletd",
		Host: "h1", DNSName: "h1.test", SHA: validSHA,
	})
	if err != nil {
		t.Fatalf("Start other: %v", err)
	}
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("other deploy did not reach upload step")
	}

	_, err = m.Start(mkExeOpsRequest())
	if !errors.Is(err, ErrSelfDeployConflict) {
		t.Fatalf("Start exe-ops err = %v, want ErrSelfDeployConflict", err)
	}

	if err := m.Cancel(other.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitTerminalDeploy(t, m, other.ID, 2*time.Second)
}

func TestSelfDeploy_RefusesNewDeploysWhileSelfDeployActive(t *testing.T) {
	m := newTestManager(t)
	startedCh := make(chan struct{})
	m.runDeploy = blockingRunner(startedCh)

	self, err := m.Start(mkExeOpsRequest())
	if err != nil {
		t.Fatalf("Start self: %v", err)
	}
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("self-deploy did not reach upload step")
	}

	_, err = m.Start(Request{
		Stage: "staging", Role: "exelet", Process: "exeletd",
		Host: "h1", DNSName: "h1.test", SHA: validSHA,
	})
	if !errors.Is(err, ErrSelfDeployConflict) {
		t.Fatalf("Start during self-deploy err = %v, want ErrSelfDeployConflict", err)
	}

	if err := m.Cancel(self.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitTerminalDeploy(t, m, self.ID, 2*time.Second)
}

func TestSelfDeploy_RefusesRolloutWhileSelfDeployActive(t *testing.T) {
	m := newTestManager(t)
	startedCh := make(chan struct{})
	m.runDeploy = blockingRunner(startedCh)

	self, err := m.Start(mkExeOpsRequest())
	if err != nil {
		t.Fatalf("Start self: %v", err)
	}
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("self-deploy did not reach upload step")
	}

	_, err = m.StartRollout(RolloutRequest{
		Targets: []RolloutTarget{
			mkTarget("a", "fra2", "staging"),
		},
	})
	if !errors.Is(err, ErrSelfDeployConflict) {
		t.Fatalf("StartRollout during self-deploy err = %v, want ErrSelfDeployConflict", err)
	}

	if err := m.Cancel(self.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitTerminalDeploy(t, m, self.ID, 2*time.Second)
}

func TestSelfDeploy_RefusedWhileRolloutActive(t *testing.T) {
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
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("rollout deploy did not reach upload step")
	}

	_, err = m.Start(mkExeOpsRequest())
	if !errors.Is(err, ErrSelfDeployConflict) {
		t.Fatalf("Start exe-ops during rollout err = %v, want ErrSelfDeployConflict", err)
	}

	if err := m.CancelRollout(rs.ID); err != nil {
		t.Fatalf("CancelRollout: %v", err)
	}
	waitTerminalRollout(t, m, rs.ID, 3*time.Second)
}

func TestSelfDeploy_AllowedWhenIdle(t *testing.T) {
	m := newTestManager(t)
	startedCh := make(chan struct{})
	m.runDeploy = blockingRunner(startedCh)

	st, err := m.Start(mkExeOpsRequest())
	if err != nil {
		t.Fatalf("Start exe-ops: %v", err)
	}
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("exe-ops deploy did not reach upload step")
	}
	if err := m.Cancel(st.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitTerminalDeploy(t, m, st.ID, 2*time.Second)
}
