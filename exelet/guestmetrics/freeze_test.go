package guestmetrics

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// newTestPool creates a Pool with freeze enabled and short cadences,
// wired to a pipe-based dialer that counts scrapes.
func newTestPool(t *testing.T, scrapes *atomic.Int64, freezeCfg FreezeConfig) *Pool {
	t.Helper()
	dial := func(ctx context.Context, vmID string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			buf := make([]byte, 64)
			n, _ := b.Read(buf)
			if string(buf[:n]) != memdRequest {
				return
			}
			if scrapes != nil {
				scrapes.Add(1)
			}
			raw := RawSample{
				Version:    ProtocolVersion,
				CapturedAt: time.Now(),
				Meminfo:    map[string]uint64{"MemTotal": 1024},
				Vmstat:     map[string]uint64{},
			}
			data, _ := json.Marshal(&raw)
			data = append(data, '\n')
			_, _ = b.Write(data)
		}()
		return a, nil
	}

	p := NewPool(PoolConfig{
		Cadences: Cadences{
			Calm:      100 * time.Millisecond,
			Normal:    50 * time.Millisecond,
			Pressured: 25 * time.Millisecond,
		},
		DialFunc:      dial,
		HostSampler:   func() HostSample { return HostSample{MemTotalBytes: 1 << 30, MemAvailableBytes: 1 << 29} },
		StaleAfter:    90 * time.Second,
		Workers:       4,
		ScrapeTimeout: 5 * time.Second,
		Freeze:        freezeCfg,
		FrozenCadence: 24 * time.Hour,
	})
	return p
}

// freshQuietSample pushes a fresh, quiet sample onto e's ring.
func freshQuietSample(e *entry) {
	e.ring.Push(Sample{
		FetchedAt:    time.Now(),
		CapturedAt:   time.Now(),
		PSIAvailable: true,
		PSIFull:      PSILine{Avg60: 0.0},
	})
}

// Test 1: Freeze entry happy-path.
func TestFreezeHappyPath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := DefaultFreezeConfig
		cfg.Enabled = true
		cfg.IdleWindow = 10 * time.Minute
		cfg.MinUptime = 5 * time.Minute

		p := NewPool(PoolConfig{
			Freeze:        cfg,
			FrozenCadence: 24 * time.Hour,
			StaleAfter:    90 * time.Second,
		})
		p.Add(VMInfo{ID: "vm1", Name: "test"})

		p.mu.RLock()
		e := p.entries["vm1"]
		p.mu.RUnlock()

		// Push a fresh quiet sample.
		freshQuietSample(e)

		// 21 witnesses at 30s spacing, cpu%=0.3, host=Calm, uptime starting at 6min.
		start := time.Now()
		for i := 0; i < 21; i++ {
			now := start.Add(time.Duration(i) * 30 * time.Second)
			time.Sleep(30 * time.Second)
			// Refresh the sample so it stays within StaleAfter.
			freshQuietSample(e)
			p.NoteActivity("vm1", ActivityWitness{
				Now:        now,
				CPUPercent: 0.3,
				HostTier:   TierCalm,
				VMUptime:   6*time.Minute + time.Duration(i)*30*time.Second,
			})
		}

		e.sched.Lock()
		got := e.vmTier
		e.sched.Unlock()

		if got != VMTierFrozen {
			t.Errorf("after 10.5min idle: vmTier=%v, want Frozen", got)
		}
	})
}

// Test 2: Bootstrap gate — no freeze before MinUptime.
func TestFreezeBootstrapGate(t *testing.T) {
	cfg := DefaultFreezeConfig
	cfg.Enabled = true
	cfg.MinUptime = 5 * time.Minute
	cfg.IdleWindow = 1 * time.Second // short so we'd freeze immediately if uptime allowed

	p := NewPool(PoolConfig{
		Freeze:        cfg,
		FrozenCadence: 24 * time.Hour,
		StaleAfter:    90 * time.Second,
	})
	p.Add(VMInfo{ID: "vm1", Name: "test"})

	p.mu.RLock()
	e := p.entries["vm1"]
	p.mu.RUnlock()
	freshQuietSample(e)

	now := time.Now()
	// 10 witnesses at 30s, uptime starting at 0.
	for i := 0; i < 10; i++ {
		t2 := now.Add(time.Duration(i) * 30 * time.Second)
		p.NoteActivity("vm1", ActivityWitness{
			Now:        t2,
			CPUPercent: 0.1,
			HostTier:   TierCalm,
			VMUptime:   time.Duration(i) * 30 * time.Second, // starts at 0
		})
	}

	e.sched.Lock()
	got := e.vmTier
	e.sched.Unlock()

	if got != VMTierActive {
		t.Errorf("with uptime<5min: vmTier=%v, want Active", got)
	}
}

// Test 3: CPU spike wakes immediately.
func TestFreezeCPUSpikeWakes(t *testing.T) {
	cfg := DefaultFreezeConfig
	cfg.Enabled = true
	cfg.IdleWindow = 0 // instant freeze for testing
	cfg.MinUptime = 0

	p := NewPool(PoolConfig{
		Freeze:        cfg,
		FrozenCadence: 24 * time.Hour,
		StaleAfter:    90 * time.Second,
	})
	p.Add(VMInfo{ID: "vm1", Name: "test"})

	p.mu.RLock()
	e := p.entries["vm1"]
	p.mu.RUnlock()
	freshQuietSample(e)

	now := time.Now()
	// Freeze the VM.
	p.NoteActivity("vm1", ActivityWitness{
		Now:        now,
		CPUPercent: 0.1,
		HostTier:   TierCalm,
		VMUptime:   10 * time.Minute,
	})

	e.sched.Lock()
	if e.vmTier != VMTierFrozen {
		t.Fatalf("expected Frozen, got %v", e.vmTier)
	}
	e.sched.Unlock()

	// CPU spike.
	p.NoteActivity("vm1", ActivityWitness{
		Now:        now.Add(30 * time.Second),
		CPUPercent: 5.0,
		HostTier:   TierCalm,
		VMUptime:   10*time.Minute + 30*time.Second,
	})

	e.sched.Lock()
	got := e.vmTier
	reason := e.lastWakeReason
	e.sched.Unlock()

	if got != VMTierActive {
		t.Errorf("after CPU spike: vmTier=%v, want Active", got)
	}
	if reason != WakeCPU {
		t.Errorf("wake reason=%v, want WakeCPU", reason)
	}
}

// Test 4: Hysteresis band — no flapping.
func TestFreezeHysteresisBand(t *testing.T) {
	cfg := DefaultFreezeConfig
	cfg.Enabled = true
	cfg.IdleWindow = 0
	cfg.MinUptime = 0
	cfg.CPUEnter = 1.0
	cfg.CPUExit = 2.0

	p := NewPool(PoolConfig{
		Freeze:        cfg,
		FrozenCadence: 24 * time.Hour,
		StaleAfter:    90 * time.Second,
	})
	p.Add(VMInfo{ID: "vm1", Name: "test"})

	p.mu.RLock()
	e := p.entries["vm1"]
	p.mu.RUnlock()
	freshQuietSample(e)

	now := time.Now()
	// Freeze the VM first.
	p.NoteActivity("vm1", ActivityWitness{
		Now: now, CPUPercent: 0.5, HostTier: TierCalm, VMUptime: 10 * time.Minute,
	})
	e.sched.Lock()
	if e.vmTier != VMTierFrozen {
		t.Fatalf("expected Frozen after idle witness")
	}
	e.sched.Unlock()

	// Witnesses with cpu% in the hysteresis band [1.0, 2.0): should stay Frozen.
	for _, cpu := range []float64{0.5, 1.5, 0.5, 1.5} {
		now = now.Add(30 * time.Second)
		p.NoteActivity("vm1", ActivityWitness{
			Now: now, CPUPercent: cpu, HostTier: TierCalm, VMUptime: 11 * time.Minute,
		})
	}

	e.sched.Lock()
	got := e.vmTier
	e.sched.Unlock()

	if got != VMTierFrozen {
		t.Errorf("in hysteresis band: vmTier=%v, want Frozen (no flapping)", got)
	}

	// Also test Active VM with alternating cpu%: idle streak keeps
	// resetting so it never accumulates enough time to freeze.
	cfg2 := cfg
	cfg2.IdleWindow = 5 * time.Minute // needs sustained idle
	p2 := NewPool(PoolConfig{
		Freeze:        cfg2,
		FrozenCadence: 24 * time.Hour,
		StaleAfter:    90 * time.Second,
	})
	p2.Add(VMInfo{ID: "vm2", Name: "test2"})
	p2.mu.RLock()
	e2 := p2.entries["vm2"]
	p2.mu.RUnlock()
	freshQuietSample(e2)

	now2 := time.Now()
	// Alternating idle/busy every 30s for 4 cycles: never accumulates 5min idle.
	for _, cpu := range []float64{0.5, 1.5, 0.5, 1.5} {
		now2 = now2.Add(30 * time.Second)
		p2.NoteActivity("vm2", ActivityWitness{
			Now: now2, CPUPercent: cpu, HostTier: TierCalm, VMUptime: 10 * time.Minute,
		})
	}
	e2.sched.Lock()
	got2 := e2.vmTier
	e2.sched.Unlock()

	if got2 != VMTierActive {
		t.Errorf("Active VM in band: vmTier=%v, want Active (idle streak resets)", got2)
	}
}

// Test 5: Pressure forces wake.
func TestFreezePressureForcesWake(t *testing.T) {
	cfg := DefaultFreezeConfig
	cfg.Enabled = true
	cfg.IdleWindow = 0
	cfg.MinUptime = 0

	p := NewPool(PoolConfig{
		Freeze:        cfg,
		FrozenCadence: 24 * time.Hour,
		StaleAfter:    90 * time.Second,
	})
	p.Add(VMInfo{ID: "vm1", Name: "test"})

	p.mu.RLock()
	e := p.entries["vm1"]
	p.mu.RUnlock()
	freshQuietSample(e)

	now := time.Now()
	// Freeze.
	p.NoteActivity("vm1", ActivityWitness{
		Now: now, CPUPercent: 0.1, HostTier: TierCalm, VMUptime: 10 * time.Minute,
	})
	e.sched.Lock()
	if e.vmTier != VMTierFrozen {
		t.Fatalf("expected Frozen")
	}
	e.sched.Unlock()

	// Host goes pressured.
	p.NoteActivity("vm1", ActivityWitness{
		Now: now.Add(time.Second), CPUPercent: 0.1, HostTier: TierPressured, VMUptime: 10 * time.Minute,
	})

	e.sched.Lock()
	got := e.vmTier
	reason := e.lastWakeReason
	e.sched.Unlock()

	if got != VMTierActive {
		t.Errorf("after pressure: vmTier=%v, want Active", got)
	}
	if reason != WakeHostPressure {
		t.Errorf("wake reason=%v, want WakeHostPressure", reason)
	}
}

// Test 6: Quiet-but-thrashing guest stays Active.
func TestFreezeQuietButThrashing(t *testing.T) {
	cfg := DefaultFreezeConfig
	cfg.Enabled = true
	cfg.IdleWindow = 0
	cfg.MinUptime = 0

	p := NewPool(PoolConfig{
		Freeze:        cfg,
		FrozenCadence: 24 * time.Hour,
		StaleAfter:    90 * time.Second,
	})
	p.Add(VMInfo{ID: "vm1", Name: "test"})

	p.mu.RLock()
	e := p.entries["vm1"]
	p.mu.RUnlock()

	// Push a sample with high PSI.
	e.ring.Push(Sample{
		FetchedAt:    time.Now(),
		CapturedAt:   time.Now(),
		PSIAvailable: true,
		PSIFull:      PSILine{Avg60: 5.0}, // thrashing
	})

	now := time.Now()
	p.NoteActivity("vm1", ActivityWitness{
		Now: now, CPUPercent: 0.1, HostTier: TierCalm, VMUptime: 10 * time.Minute,
	})

	e.sched.Lock()
	got := e.vmTier
	e.sched.Unlock()

	if got != VMTierActive {
		t.Errorf("with high guest PSI: vmTier=%v, want Active", got)
	}
}

// Test 9: WakeForRPC.
func TestFreezeWakeForRPC(t *testing.T) {
	cfg := DefaultFreezeConfig
	cfg.Enabled = true
	cfg.IdleWindow = 0
	cfg.MinUptime = 0

	p := NewPool(PoolConfig{
		Freeze:        cfg,
		FrozenCadence: 24 * time.Hour,
		StaleAfter:    90 * time.Second,
	})
	p.Add(VMInfo{ID: "vm1", Name: "test"})

	p.mu.RLock()
	e := p.entries["vm1"]
	p.mu.RUnlock()
	freshQuietSample(e)

	// Freeze.
	p.NoteActivity("vm1", ActivityWitness{
		Now: time.Now(), CPUPercent: 0.1, HostTier: TierCalm, VMUptime: 10 * time.Minute,
	})
	e.sched.Lock()
	if e.vmTier != VMTierFrozen {
		t.Fatalf("expected Frozen")
	}
	e.sched.Unlock()

	got := p.WakeForRPC("vm1")
	if !got {
		t.Error("WakeForRPC returned false, want true")
	}

	e.sched.Lock()
	tier := e.vmTier
	reason := e.lastWakeReason
	e.sched.Unlock()

	if tier != VMTierActive {
		t.Errorf("after WakeForRPC: vmTier=%v, want Active", tier)
	}
	if reason != WakeRPC {
		t.Errorf("wake reason=%v, want WakeRPC", reason)
	}

	// Second WakeForRPC on already-active VM returns false.
	if p.WakeForRPC("vm1") {
		t.Error("second WakeForRPC should return false")
	}

	// Unknown VM returns false.
	if p.WakeForRPC("no-such-vm") {
		t.Error("WakeForRPC for unknown VM should return false")
	}
}

// Test 7: 24h heartbeat fires exactly one scrape, VM remains Frozen.
func TestFreezeHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var scrapes atomic.Int64
		cfg := DefaultFreezeConfig
		cfg.Enabled = true
		cfg.IdleWindow = 0
		cfg.MinUptime = 0

		dial := func(ctx context.Context, vmID string) (net.Conn, error) {
			a, b := net.Pipe()
			go func() {
				defer b.Close()
				buf := make([]byte, 64)
				n, _ := b.Read(buf)
				if string(buf[:n]) != memdRequest {
					return
				}
				scrapes.Add(1)
				raw := RawSample{
					Version:    ProtocolVersion,
					CapturedAt: time.Now(),
					Meminfo:    map[string]uint64{"MemTotal": 1024},
					Vmstat:     map[string]uint64{},
				}
				data, _ := json.Marshal(&raw)
				data = append(data, '\n')
				_, _ = b.Write(data)
			}()
			return a, nil
		}

		p := NewPool(PoolConfig{
			Cadences:      Cadences{Calm: time.Second, Normal: time.Second, Pressured: time.Second},
			DialFunc:      dial,
			HostSampler:   func() HostSample { return HostSample{MemTotalBytes: 1 << 30, MemAvailableBytes: 1 << 29} },
			StaleAfter:    90 * time.Second,
			Workers:       4,
			ScrapeTimeout: 5 * time.Second,
			Freeze:        cfg,
			FrozenCadence: 24 * time.Hour,
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		p.Start(ctx)
		defer p.Stop()

		p.Add(VMInfo{ID: "vm1", Name: "test"})

		// Wait for initial scrape.
		time.Sleep(2 * time.Second)

		p.mu.RLock()
		e := p.entries["vm1"]
		p.mu.RUnlock()

		// Freeze the VM via NoteActivity.
		freshQuietSample(e)
		p.NoteActivity("vm1", ActivityWitness{
			Now: time.Now(), CPUPercent: 0.1, HostTier: TierCalm, VMUptime: 10 * time.Minute,
		})

		e.sched.Lock()
		if e.vmTier != VMTierFrozen {
			t.Fatalf("expected Frozen, got %v", e.vmTier)
		}
		e.sched.Unlock()

		// Give a few seconds for any in-flight active scrapes to drain,
		// then record the baseline.
		time.Sleep(3 * time.Second)
		before := scrapes.Load()

		// Advance 24h so the heartbeat fires.
		time.Sleep(24 * time.Hour)
		// Wait a few more seconds for the scrape to complete.
		time.Sleep(5 * time.Second)

		after := scrapes.Load()
		heartbeatScrapes := after - before
		if heartbeatScrapes != 1 {
			t.Errorf("heartbeat scrapes = %d, want 1", heartbeatScrapes)
		}

		// VM should still be Frozen (heartbeat doesn't flip to Active permanently).
		e.sched.Lock()
		got := e.vmTier
		reason := e.lastWakeReason
		e.sched.Unlock()

		if got != VMTierFrozen {
			t.Errorf("after heartbeat: vmTier=%v, want Frozen", got)
		}
		if reason != WakeHeartbeat {
			t.Errorf("wake reason=%v, want WakeHeartbeat", reason)
		}
	})
}

// Test 8: Heartbeat wakes via guest PSI.
func TestFreezeHeartbeatWakesOnGuestPSI(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := DefaultFreezeConfig
		cfg.Enabled = true
		cfg.IdleWindow = 0
		cfg.MinUptime = 0
		cfg.GuestPSIWake = 5.0

		// Dial returns high-PSI sample.
		dial := func(ctx context.Context, vmID string) (net.Conn, error) {
			a, b := net.Pipe()
			go func() {
				defer b.Close()
				buf := make([]byte, 64)
				n, _ := b.Read(buf)
				if string(buf[:n]) != memdRequest {
					return
				}
				raw := RawSample{
					Version:    ProtocolVersion,
					CapturedAt: time.Now(),
					Meminfo:    map[string]uint64{"MemTotal": 1024},
					Vmstat:     map[string]uint64{},
					PSI: map[string]PSILine{
						"full": {Avg60: 8.0}, // high PSI!
						"some": {Avg60: 10.0},
					},
				}
				data, _ := json.Marshal(&raw)
				data = append(data, '\n')
				_, _ = b.Write(data)
			}()
			return a, nil
		}

		p := NewPool(PoolConfig{
			Cadences:      Cadences{Calm: time.Second, Normal: time.Second, Pressured: time.Second},
			DialFunc:      dial,
			HostSampler:   func() HostSample { return HostSample{MemTotalBytes: 1 << 30, MemAvailableBytes: 1 << 29} },
			StaleAfter:    90 * time.Second,
			Workers:       4,
			ScrapeTimeout: 5 * time.Second,
			Freeze:        cfg,
			FrozenCadence: 24 * time.Hour,
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		p.Start(ctx)
		defer p.Stop()

		p.Add(VMInfo{ID: "vm1", Name: "test"})

		// Wait for initial scrape. (It will return high PSI, but VM is Active,
		// so the PSI wake net is a no-op.)
		time.Sleep(2 * time.Second)

		p.mu.RLock()
		e := p.entries["vm1"]
		p.mu.RUnlock()

		// Push a low-PSI sample to satisfy shouldFreeze, then freeze.
		e.ring.Push(Sample{
			FetchedAt:    time.Now(),
			CapturedAt:   time.Now(),
			PSIAvailable: true,
			PSIFull:      PSILine{Avg60: 0.0},
		})
		p.NoteActivity("vm1", ActivityWitness{
			Now: time.Now(), CPUPercent: 0.1, HostTier: TierCalm, VMUptime: 10 * time.Minute,
		})

		e.sched.Lock()
		if e.vmTier != VMTierFrozen {
			t.Fatalf("expected Frozen, got %v", e.vmTier)
		}
		e.sched.Unlock()

		// Advance 24h so heartbeat fires (which scrapes high-PSI response).
		time.Sleep(24 * time.Hour)
		time.Sleep(5 * time.Second)

		e.sched.Lock()
		got := e.vmTier
		reason := e.lastWakeReason
		e.sched.Unlock()

		if got != VMTierActive {
			t.Errorf("after heartbeat with high PSI: vmTier=%v, want Active", got)
		}
		if reason != WakeGuestPSI {
			t.Errorf("wake reason=%v, want WakeGuestPSI", reason)
		}
	})
}

// Test 11: Concurrency / race. Dispatcher + 8 goroutines hammering
// NoteActivity for 5 simulated minutes. -race clean.
func TestFreezeConcurrencyRace(t *testing.T) {
	cfg := DefaultFreezeConfig
	cfg.Enabled = true
	cfg.IdleWindow = 30 * time.Second
	cfg.MinUptime = 0

	var scrapes atomic.Int64
	p := newTestPool(t, &scrapes, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	defer p.Stop()

	for i := 0; i < 10; i++ {
		p.Add(VMInfo{ID: "vm" + string(rune('0'+i)), Name: "vm" + string(rune('0'+i))})
	}

	// Push fresh samples on all entries.
	p.mu.RLock()
	for _, e := range p.entries {
		freshQuietSample(e)
	}
	p.mu.RUnlock()

	var wg sync.WaitGroup
	done := make(chan struct{})

	// 8 goroutines hammering NoteActivity.
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				for i := 0; i < 10; i++ {
					id := "vm" + string(rune('0'+i))
					cpu := float64(g%3) * 1.5 // varies 0, 1.5, 3.0
					p.NoteActivity(id, ActivityWitness{
						Now:        time.Now(),
						CPUPercent: cpu,
						HostTier:   TierCalm,
						VMUptime:   10 * time.Minute,
					})
				}
			}
		}(g)
	}

	// Let it churn for 100ms real time.
	time.Sleep(100 * time.Millisecond)
	close(done)
	wg.Wait()

	// Final invariant: vmTier is valid and next is not zero.
	p.mu.RLock()
	for id, e := range p.entries {
		e.sched.Lock()
		if e.vmTier != VMTierActive && e.vmTier != VMTierFrozen {
			t.Errorf("vm %s: invalid vmTier %v", id, e.vmTier)
		}
		if e.next.IsZero() {
			t.Errorf("vm %s: next is zero", id)
		}
		e.sched.Unlock()
	}
	p.mu.RUnlock()
}
