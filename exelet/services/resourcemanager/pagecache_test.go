package resourcemanager

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"exe.dev/exelet/config"
)

func TestIdleProbeAvgCPUPercent(t *testing.T) {
	p := &idleProbe{}
	t0 := time.Unix(0, 0)
	// 31 minutes of identical-CPU samples: average should be 0% (no delta).
	for i := 0; i <= 62; i++ {
		p.recordCPUSample(t0.Add(time.Duration(i)*30*time.Second), 0)
	}
	avg, ok := p.avgCPUPercent(t0.Add(31 * time.Minute))
	if !ok {
		t.Fatalf("expected enough history; got ok=false")
	}
	if avg != 0 {
		t.Fatalf("avg = %v, want 0", avg)
	}
}

func TestIdleProbeNotEnoughHistory(t *testing.T) {
	p := &idleProbe{}
	t0 := time.Unix(0, 0)
	for i := 0; i < 10; i++ {
		p.recordCPUSample(t0.Add(time.Duration(i)*30*time.Second), float64(i))
	}
	_, ok := p.avgCPUPercent(t0.Add(5 * time.Minute))
	if ok {
		t.Fatalf("expected ok=false with <30 minutes of history")
	}
}

func TestIdleProbeBusyVM(t *testing.T) {
	p := &idleProbe{}
	t0 := time.Unix(0, 0)
	// 31 min of poll, +1 cpu sec per 30 sec poll → 100% CPU constant.
	for i := 0; i <= 62; i++ {
		p.recordCPUSample(
			t0.Add(time.Duration(i)*30*time.Second),
			float64(i)*30.0, // 30 cpu-seconds per 30 wall-seconds
		)
	}
	avg, ok := p.avgCPUPercent(t0.Add(31 * time.Minute))
	if !ok || avg < 99 || avg > 101 {
		t.Fatalf("avg = %v ok = %v, want ~100", avg, ok)
	}
}

func TestMaybeProbeIdleCacheDropFires(t *testing.T) {
	// Stub exed: count calls, verify form value.
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got := r.FormValue("container_id"); got != "vm-idle" {
			t.Errorf("container_id = %q, want vm-idle", got)
		}
		calls.Add(1)
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	m := &ResourceManager{
		config:              &config.ExeletConfig{ExedURL: srv.URL},
		usageState:          map[string]*vmUsageState{},
		log:                 slog.Default(),
		cgroupRoot:          tmpDir,
		idleCacheDropRandFn: func() float64 { return 0 }, // always trigger
	}
	state := &vmUsageState{name: "idle-vm", groupID: "g"}
	state.idle = &idleProbe{}
	t0 := time.Unix(0, 0)
	for i := 0; i <= 62; i++ {
		state.idle.recordCPUSample(t0.Add(time.Duration(i)*30*time.Second), 0)
	}
	m.usageState["vm-idle"] = state

	m.dropInflight.Store(false)
	m.maybeProbeIdleCacheDrop(t.Context(), "vm-idle", "idle-vm", "g", t0.Add(31*time.Minute), 0)

	// Wait briefly for goroutine to fire.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 call to exed, got %d", calls.Load())
	}
}

func TestMaybeProbeIdleCacheDropSkipsBusy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("exed should not be called for busy VM")
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	m := &ResourceManager{
		config:              &config.ExeletConfig{ExedURL: srv.URL},
		usageState:          map[string]*vmUsageState{},
		log:                 slog.Default(),
		cgroupRoot:          t.TempDir(),
		idleCacheDropRandFn: func() float64 { return 0 },
	}
	state := &vmUsageState{name: "busy-vm", groupID: "g", idle: &idleProbe{}}
	t0 := time.Unix(0, 0)
	for i := 0; i <= 62; i++ {
		// Burns ~50% CPU.
		state.idle.recordCPUSample(t0.Add(time.Duration(i)*30*time.Second), float64(i)*15.0)
	}
	m.usageState["vm-busy"] = state

	m.dropInflight.Store(false)
	m.maybeProbeIdleCacheDrop(t.Context(), "vm-busy", "busy-vm", "g", t0.Add(31*time.Minute), 30*float64(63))
	// maybeProbeIdleCacheDrop returns synchronously when the VM is not
	// idle — no goroutine is launched, so no waiting is necessary.
}

func TestMaybeProbeIdleCacheDropProbabilityGate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("exed should not be called when rand >= probability")
	}))
	defer srv.Close()

	m := &ResourceManager{
		config:              &config.ExeletConfig{ExedURL: srv.URL},
		usageState:          map[string]*vmUsageState{},
		log:                 slog.Default(),
		cgroupRoot:          t.TempDir(),
		idleCacheDropRandFn: func() float64 { return 0.5 }, // far above 1/100
	}
	state := &vmUsageState{name: "idle-vm", groupID: "g", idle: &idleProbe{}}
	t0 := time.Unix(0, 0)
	for i := 0; i <= 62; i++ {
		state.idle.recordCPUSample(t0.Add(time.Duration(i)*30*time.Second), 0)
	}
	m.usageState["vm-idle"] = state
	m.dropInflight.Store(false)
	m.maybeProbeIdleCacheDrop(t.Context(), "vm-idle", "idle-vm", "g", t0.Add(31*time.Minute), 0)
	// maybeProbeIdleCacheDrop returns synchronously when the random gate
	// rejects — no goroutine is launched, so no waiting is necessary.
}

func TestMaybeProbeIdleCacheDropSingleflight(t *testing.T) {
	// While a probe is in-flight, additional eligible VMs must not start
	// concurrent probes — the dropInflight CAS singles them out.
	var calls atomic.Int32
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		<-unblock
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	m := &ResourceManager{
		config:              &config.ExeletConfig{ExedURL: srv.URL},
		usageState:          map[string]*vmUsageState{},
		log:                 slog.Default(),
		cgroupRoot:          t.TempDir(),
		idleCacheDropRandFn: func() float64 { return 0 },
	}
	t0 := time.Unix(0, 0)
	for _, id := range []string{"vm-a", "vm-b", "vm-c"} {
		state := &vmUsageState{name: id, groupID: "g", idle: &idleProbe{}}
		for i := 0; i <= 62; i++ {
			state.idle.recordCPUSample(t0.Add(time.Duration(i)*30*time.Second), 0)
		}
		m.usageState[id] = state
	}

	// Fire the first probe; it should block on <-unblock.
	m.maybeProbeIdleCacheDrop(t.Context(), "vm-a", "vm-a", "g", t0.Add(31*time.Minute), 0)
	// Wait for the goroutine to start its HTTP request.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 in-flight call, got %d", calls.Load())
	}
	// Subsequent attempts must be skipped by the singleflight gate.
	m.maybeProbeIdleCacheDrop(t.Context(), "vm-b", "vm-b", "g", t0.Add(31*time.Minute), 0)
	m.maybeProbeIdleCacheDrop(t.Context(), "vm-c", "vm-c", "g", t0.Add(31*time.Minute), 0)
	if n := calls.Load(); n != 1 {
		t.Fatalf("expected dropInflight to gate further probes, but got %d calls", n)
	}
	close(unblock)
}

// TestMaybeProbeIdleCacheDropMemoryGrowthGate exercises the new 20%
// (memory.current - memory.file) growth gate: a successful drop stamps a
// baseline, after which probes only re-fire once the same metric has grown
// by at least 20%.
func TestMaybeProbeIdleCacheDropMemoryGrowthGate(t *testing.T) {
	m := &ResourceManager{
		config:              &config.ExeletConfig{ExedURL: "http://stub-not-called"},
		usageState:          map[string]*vmUsageState{},
		log:                 slog.Default(),
		cgroupRoot:          t.TempDir(),
		idleCacheDropRandFn: func() float64 { return 0 }, // always trigger if other gates pass
	}

	t0 := time.Unix(0, 0)
	state := &vmUsageState{
		name: "idle-vm", groupID: "g", idle: &idleProbe{},
		memoryBytes: 1_000_000_000, memoryFileBytes: 100_000_000,
	}
	for i := 0; i <= 62; i++ {
		state.idle.recordCPUSample(t0.Add(time.Duration(i)*30*time.Second), 0)
	}
	m.usageState["vm-idle"] = state

	// Simulate a previous successful drop that pinned the baseline at
	// (memory.current - memory.file) = 900_000_000.
	state.idle.lastDropMemoryBytes = 900_000_000

	// Same usage → no growth → must NOT fire.
	calls := 0
	origRand := m.idleCacheDropRandFn
	m.idleCacheDropRandFn = func() float64 { calls++; return 0 }
	m.maybeProbeIdleCacheDrop(t.Context(), "vm-idle", state.name, state.groupID, t0.Add(31*time.Minute), 0)
	if calls != 0 {
		t.Fatalf("growth gate should have skipped; rand was consulted %d times", calls)
	}

	// Bump current so non-file memory = 1_080_000_000 = +20% over 900M; gate
	// passes and rand should be consulted.
	state.memoryBytes = 1_180_000_000
	state.memoryFileBytes = 100_000_000
	m.idleCacheDropRandFn = origRand
	calls = 0
	m.idleCacheDropRandFn = func() float64 { calls++; return 0.999 } // suppress so we just observe gate pass
	m.maybeProbeIdleCacheDrop(t.Context(), "vm-idle", state.name, state.groupID, t0.Add(31*time.Minute), 0)
	if calls != 1 {
		t.Fatalf("growth gate should have admitted >=20%% growth; rand consulted %d times", calls)
	}
}
