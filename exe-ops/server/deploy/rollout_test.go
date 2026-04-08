package deploy

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := NewManager(context.Background(), log, t.TempDir(), t.TempDir())
	return m
}

// validSHA is a placeholder 40-char hex string for tests.
const validSHA = "0123456789abcdef0123456789abcdef01234567"

func mkTarget(host, region, stage string) RolloutTarget {
	return RolloutTarget{
		Request: Request{
			Stage:   stage,
			Role:    "exelet",
			Process: "exeletd",
			Host:    host,
			DNSName: host + ".test",
			SHA:     validSHA,
		},
		Region: region,
	}
}

func TestPlanWaves_RegionBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		targets     []RolloutTarget
		batchSize   int
		want        [][]string // wave -> hosts
		wantRegions []string
	}{
		{
			name: "single region single batch",
			targets: []RolloutTarget{
				mkTarget("a", "fra2", "staging"),
				mkTarget("b", "fra2", "staging"),
			},
			batchSize:   3,
			want:        [][]string{{"a", "b"}},
			wantRegions: []string{"fra2"},
		},
		{
			name: "single region split into chunks",
			targets: []RolloutTarget{
				mkTarget("a", "fra2", "staging"),
				mkTarget("b", "fra2", "staging"),
				mkTarget("c", "fra2", "staging"),
				mkTarget("d", "fra2", "staging"),
				mkTarget("e", "fra2", "staging"),
			},
			batchSize:   2,
			want:        [][]string{{"a", "b"}, {"c", "d"}, {"e"}},
			wantRegions: []string{"fra2", "fra2", "fra2"},
		},
		{
			name: "two regions hard boundary",
			targets: []RolloutTarget{
				mkTarget("a", "fra2", "staging"),
				mkTarget("b", "fra2", "staging"),
				mkTarget("c", "lax", "staging"),
				mkTarget("d", "lax", "staging"),
			},
			batchSize:   3,
			want:        [][]string{{"a", "b"}, {"c", "d"}},
			wantRegions: []string{"fra2", "lax"},
		},
		{
			name: "three regions, second is split",
			targets: []RolloutTarget{
				mkTarget("a", "fra2", "staging"),
				mkTarget("b", "lax", "staging"),
				mkTarget("c", "lax", "staging"),
				mkTarget("d", "lax", "staging"),
				mkTarget("e", "nyc", "staging"),
			},
			batchSize:   2,
			want:        [][]string{{"a"}, {"b", "c"}, {"d"}, {"e"}},
			wantRegions: []string{"fra2", "lax", "lax", "nyc"},
		},
		{
			name: "interleaved input is grouped by first-seen region order",
			targets: []RolloutTarget{
				mkTarget("a", "fra2", "staging"),
				mkTarget("b", "lax", "staging"),
				mkTarget("c", "fra2", "staging"),
				mkTarget("d", "lax", "staging"),
			},
			batchSize:   3,
			want:        [][]string{{"a", "c"}, {"b", "d"}},
			wantRegions: []string{"fra2", "lax"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			waves := planWaves(tt.targets, tt.batchSize)
			if len(waves) != len(tt.want) {
				t.Fatalf("got %d waves, want %d", len(waves), len(tt.want))
			}
			for i, w := range waves {
				if w.region != tt.wantRegions[i] {
					t.Errorf("wave %d region = %q, want %q", i, w.region, tt.wantRegions[i])
				}
				gotHosts := make([]string, len(w.requests))
				for j, r := range w.requests {
					gotHosts[j] = r.Host
				}
				if !equalSlice(gotHosts, tt.want[i]) {
					t.Errorf("wave %d hosts = %v, want %v", i, gotHosts, tt.want[i])
				}
			}
		})
	}
}

func TestEffectiveBatchSize(t *testing.T) {
	tests := []struct {
		n    int
		want int
	}{
		{1, 1},
		{2, 1},
		{3, 1},
		{4, 2},
		{6, 2},
		{7, 3},
		{9, 3},
		{10, 4},
	}
	for _, tt := range tests {
		req := RolloutRequest{Targets: make([]RolloutTarget, tt.n)}
		got := effectiveBatchSize(req)
		if got != tt.want {
			t.Errorf("effectiveBatchSize(n=%d) = %d, want %d", tt.n, got, tt.want)
		}
	}
}

// fakeRunner returns a runDeploy function that drives a deploy through
// its steps. If failHosts contains the deploy's host, the deploy is
// failed; otherwise it succeeds. The runner respects ctx cancellation
// (exiting as a failure with the ctx error) so tests can exercise the
// cancel-during-wave path.
func fakeRunner(failHosts map[string]bool, runOrder *atomic.Int32, ranHosts *[]string, mu *sync.Mutex) func(ctx context.Context, d *deploy) {
	return fakeRunnerWith(failHosts, runOrder, ranHosts, mu, 5*time.Millisecond)
}

func fakeRunnerWith(failHosts map[string]bool, runOrder *atomic.Int32, ranHosts *[]string, mu *sync.Mutex, stepDelay time.Duration) func(ctx context.Context, d *deploy) {
	return func(ctx context.Context, d *deploy) {
		mu.Lock()
		*ranHosts = append(*ranHosts, d.host)
		mu.Unlock()
		runOrder.Add(1)

		// sleep returns true if the sleep completed, false if ctx was
		// cancelled during the sleep.
		sleep := func(dur time.Duration) bool {
			if dur <= 0 {
				return true
			}
			t := time.NewTimer(dur)
			defer t.Stop()
			select {
			case <-t.C:
				return true
			case <-ctx.Done():
				return false
			}
		}

		if !sleep(stepDelay) {
			d.beginStep("build")
			d.stepDone(ctx.Err())
			return
		}

		d.beginStep("build")
		if failHosts[d.host] {
			d.stepDone(errFake)
			return
		}
		if !sleep(stepDelay) {
			d.stepDone(ctx.Err())
			return
		}
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
			if !sleep(stepDelay) {
				d.stepDone(ctx.Err())
				return
			}
			d.stepDone(nil)
		}
		d.complete()
	}
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "fake failure" }

func TestRollout_StopOnFailureAbortsLaterWaves(t *testing.T) {
	m := newTestManager(t)
	var order atomic.Int32
	var ran []string
	var mu sync.Mutex
	m.runDeploy = fakeRunner(map[string]bool{"b": true}, &order, &ran, &mu)

	req := RolloutRequest{
		Targets: []RolloutTarget{
			mkTarget("a", "fra2", "staging"),
			mkTarget("b", "fra2", "staging"), // fails
			mkTarget("c", "lax", "staging"),
		},
		BatchSize:     2,
		CooldownSecs:  1,
		StopOnFailure: true,
	}
	st, err := m.StartRollout(req)
	if err != nil {
		t.Fatalf("StartRollout: %v", err)
	}

	// Wait for terminal.
	final := waitTerminalRollout(t, m, st.ID, 5*time.Second)
	if final.State != "failed" {
		t.Fatalf("state = %q, want failed", final.State)
	}
	if final.Failed != 1 {
		t.Errorf("failed = %d, want 1", final.Failed)
	}
	if final.Completed != 1 {
		t.Errorf("completed = %d, want 1", final.Completed)
	}
	if len(final.Waves) != 2 {
		t.Fatalf("waves = %d, want 2", len(final.Waves))
	}
	if final.Waves[0].State != "failed" {
		t.Errorf("wave[0].state = %q, want failed", final.Waves[0].State)
	}
	if final.Waves[1].State != "skipped" {
		t.Errorf("wave[1].state = %q, want skipped", final.Waves[1].State)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, h := range ran {
		if h == "c" {
			t.Errorf("host c should not have run after wave 1 failure, ran=%v", ran)
		}
	}
}

func TestRollout_HappyPathSucceeds(t *testing.T) {
	m := newTestManager(t)
	var order atomic.Int32
	var ran []string
	var mu sync.Mutex
	m.runDeploy = fakeRunner(nil, &order, &ran, &mu)

	req := RolloutRequest{
		Targets: []RolloutTarget{
			mkTarget("a", "fra2", "staging"),
			mkTarget("b", "fra2", "staging"),
			mkTarget("c", "lax", "staging"),
		},
		BatchSize:     2,
		CooldownSecs:  1,
		StopOnFailure: true,
	}
	st, err := m.StartRollout(req)
	if err != nil {
		t.Fatalf("StartRollout: %v", err)
	}
	final := waitTerminalRollout(t, m, st.ID, 5*time.Second)
	if final.State != "done" {
		t.Fatalf("state = %q, want done", final.State)
	}
	if final.Completed != 3 || final.Failed != 0 || final.Remaining != 0 {
		t.Errorf("counts c=%d f=%d r=%d, want 3/0/0", final.Completed, final.Failed, final.Remaining)
	}
}

func TestRollout_PerProcessLockReturnsConflict(t *testing.T) {
	m := newTestManager(t)
	var order atomic.Int32
	var ran []string
	var mu sync.Mutex
	// Slow down the runner so the first rollout is still running when we
	// try to start the second.
	slowRunner := func(ctx context.Context, d *deploy) {
		mu.Lock()
		ran = append(ran, d.host)
		mu.Unlock()
		order.Add(1)
		time.Sleep(200 * time.Millisecond)
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
	m.runDeploy = slowRunner

	req := RolloutRequest{
		Targets: []RolloutTarget{
			mkTarget("a", "fra2", "staging"),
		},
		StopOnFailure: true,
	}
	st1, err := m.StartRollout(req)
	if err != nil {
		t.Fatalf("first StartRollout: %v", err)
	}

	// Second start for same process should conflict.
	_, err = m.StartRollout(req)
	if err == nil {
		t.Fatal("second StartRollout should have failed")
	}
	if !strings.Contains(err.Error(), "deployment in progress") {
		t.Errorf("error = %v, want 'deployment in progress'", err)
	}

	// Different process is allowed in parallel.
	otherReq := RolloutRequest{
		Targets: []RolloutTarget{
			{
				Request: Request{
					Stage: "staging", Role: "exeprox", Process: "exeprox",
					Host: "p", DNSName: "p.test", SHA: validSHA,
				},
				Region: "fra2",
			},
		},
		StopOnFailure: true,
	}
	if _, err := m.StartRollout(otherReq); err != nil {
		t.Errorf("different-process rollout should succeed: %v", err)
	}

	// Wait for original to finish.
	waitTerminalRollout(t, m, st1.ID, 5*time.Second)

	// Once finished, we can start another rollout for the same process.
	if _, err := m.StartRollout(req); err != nil {
		t.Errorf("rollout after first finished should succeed: %v", err)
	}
}

func TestRollout_CooldownBetweenWaves(t *testing.T) {
	m := newTestManager(t)
	var order atomic.Int32
	var ran []string
	var mu sync.Mutex
	m.runDeploy = fakeRunner(nil, &order, &ran, &mu)

	req := RolloutRequest{
		Targets: []RolloutTarget{
			mkTarget("a", "fra2", "staging"),
			mkTarget("b", "lax", "staging"),
		},
		BatchSize:     1,
		CooldownSecs:  1,
		StopOnFailure: true,
	}
	start := time.Now()
	st, err := m.StartRollout(req)
	if err != nil {
		t.Fatalf("StartRollout: %v", err)
	}
	final := waitTerminalRollout(t, m, st.ID, 5*time.Second)
	elapsed := time.Since(start)
	if final.State != "done" {
		t.Fatalf("state = %q, want done", final.State)
	}
	if elapsed < time.Second {
		t.Errorf("elapsed = %v, want at least 1s (cooldown should have applied)", elapsed)
	}
}

func TestRollout_CancelDuringRunningWaveAbortsInFlight(t *testing.T) {
	m := newTestManager(t)
	var order atomic.Int32
	var ran []string
	var mu sync.Mutex
	// Use a long step delay so we can reliably cancel mid-wave.
	m.runDeploy = fakeRunnerWith(nil, &order, &ran, &mu, 500*time.Millisecond)

	req := RolloutRequest{
		Targets: []RolloutTarget{
			mkTarget("a", "fra2", "staging"),
			mkTarget("b", "lax", "staging"),
		},
		BatchSize:     1,
		CooldownSecs:  1,
		StopOnFailure: true,
	}
	st, err := m.StartRollout(req)
	if err != nil {
		t.Fatalf("StartRollout: %v", err)
	}

	// Wait until the first wave is running (at least one host has started).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(ran)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancelStart := time.Now()
	if err := m.CancelRollout(st.ID); err != nil {
		t.Fatalf("CancelRollout: %v", err)
	}

	final := waitTerminalRollout(t, m, st.ID, 3*time.Second)
	// With abort-in-flight semantics, the rollout should terminate much
	// faster than a full step delay sequence. Sanity-check that.
	if elapsed := time.Since(cancelStart); elapsed > 2*time.Second {
		t.Errorf("cancel took %v, wanted abort within 2s", elapsed)
	}
	if final.State != "cancelled" {
		t.Fatalf("state = %q, want cancelled", final.State)
	}
	if len(final.Waves) != 2 {
		t.Fatalf("waves = %d, want 2", len(final.Waves))
	}
	// Wave 0 should be cancelled (in-flight was aborted), wave 1 skipped.
	if final.Waves[0].State != "cancelled" {
		t.Errorf("wave[0].state = %q, want cancelled", final.Waves[0].State)
	}
	if final.Waves[1].State != "skipped" {
		t.Errorf("wave[1].state = %q, want skipped", final.Waves[1].State)
	}
	// Host b (wave 1) should never have run.
	mu.Lock()
	defer mu.Unlock()
	for _, h := range ran {
		if h == "b" {
			t.Errorf("host b should not have run after cancel, ran=%v", ran)
		}
	}
}

func TestRollout_CancelDuringCooldown(t *testing.T) {
	m := newTestManager(t)
	var order atomic.Int32
	var ran []string
	var mu sync.Mutex
	m.runDeploy = fakeRunner(nil, &order, &ran, &mu)

	req := RolloutRequest{
		Targets: []RolloutTarget{
			mkTarget("a", "fra2", "staging"),
			mkTarget("b", "lax", "staging"),
			mkTarget("c", "nyc", "staging"),
		},
		BatchSize:     1,
		CooldownSecs:  10, // long cooldown so we can cancel during it
		StopOnFailure: true,
	}
	st, err := m.StartRollout(req)
	if err != nil {
		t.Fatalf("StartRollout: %v", err)
	}

	// Wait until the rollout is in cooldown after wave 0.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, ok := m.GetRollout(st.ID)
		if ok && s.State == "cooldown" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := m.CancelRollout(st.ID); err != nil {
		t.Fatalf("CancelRollout: %v", err)
	}

	final := waitTerminalRollout(t, m, st.ID, 5*time.Second)
	if final.State != "cancelled" {
		t.Fatalf("state = %q, want cancelled", final.State)
	}
	skipped := 0
	for _, w := range final.Waves {
		if w.State == "skipped" {
			skipped++
		}
	}
	if skipped != 2 {
		t.Errorf("skipped waves = %d, want 2", skipped)
	}
}

func TestRollout_ValidationRejectsMixedProcess(t *testing.T) {
	m := newTestManager(t)
	req := RolloutRequest{
		Targets: []RolloutTarget{
			mkTarget("a", "fra2", "staging"),
			{
				Request: Request{
					Stage: "staging", Role: "exeprox", Process: "exeprox",
					Host: "b", DNSName: "b.test", SHA: validSHA,
				},
				Region: "fra2",
			},
		},
	}
	if _, err := m.StartRollout(req); err == nil {
		t.Fatal("expected validation error for mixed processes")
	}
}

func TestRollout_ValidationRequiresRegion(t *testing.T) {
	m := newTestManager(t)
	req := RolloutRequest{
		Targets: []RolloutTarget{
			{
				Request: Request{
					Stage: "staging", Role: "exelet", Process: "exeletd",
					Host: "a", DNSName: "a.test", SHA: validSHA,
				},
				// missing Region
			},
		},
	}
	if _, err := m.StartRollout(req); err == nil {
		t.Fatal("expected validation error for missing region")
	}
}

func waitTerminalRollout(t *testing.T, m *Manager, id string, timeout time.Duration) RolloutStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s, ok := m.GetRollout(id)
		if !ok {
			t.Fatalf("rollout %s vanished", id)
		}
		if terminalRollout(s.State) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("rollout %s did not reach terminal state in %v", id, timeout)
	return RolloutStatus{}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
