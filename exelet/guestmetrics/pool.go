package guestmetrics

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"exe.dev/exelet/vsockdial"
)

// memdRequest is duplicated here so we don't pull cmd/exe-init into
// non-guest code paths.
const memdRequest = "GET memstat\n"

// Defaults for sample staleness and worker pool.
const (
	// DefaultStaleAfter must comfortably exceed the calm-tier cadence
	// (60s) so a fresh sample is always available to consumers between
	// scrapes, with headroom for scrape latency.
	DefaultStaleAfter    = 90 * time.Second
	DefaultWorkers       = 8
	DefaultScrapeTimeout = 5 * time.Second
)

// DialFunc opens a raw byte stream to the given VM's memd. The default
// implementation goes through the cloud-hypervisor hybrid-vsock unix socket.
// Tests inject net.Pipe-backed connections.
type DialFunc func(ctx context.Context, vmID string) (net.Conn, error)

// HostSampleFunc returns the current host pressure sample. Used by the
// classifier to choose cadence. Returning a zero value implies "calm".
type HostSampleFunc func() HostSample

// VMInfo is the static info Pool needs to drive scrapes for one VM.
type VMInfo struct {
	ID        string
	Name      string
	StartedAt time.Time
	// SocketPath is the host-side CH hybrid-vsock unix socket. If empty
	// and DialFunc is nil, scrapes for this VM fail.
	SocketPath string
}

// PoolConfig configures a Pool.
type PoolConfig struct {
	Cadences      Cadences
	Thresh        TierThresholds
	StaleAfter    time.Duration
	Workers       int
	ScrapeTimeout time.Duration

	DialFunc    DialFunc // overrides default vsockdial-based dialer
	HostSampler HostSampleFunc

	Freeze           FreezeConfig
	FrozenCadence    time.Duration // heartbeat interval for frozen VMs (default 24h)
	FrozenStaleAfter time.Duration // staleness ceiling for frozen VMs (default FrozenCadence + 30m)

	Metrics *Metrics
	Log     *slog.Logger
}

// Pool manages per-VM scraping. It owns the worker pool and the host-tier
// classifier; callers (resourcemanager) call Add/Remove and Snapshot.
type Pool struct {
	cfg        PoolConfig
	classifier *Classifier

	mu      sync.RWMutex
	entries map[string]*entry

	wakeCh  chan struct{} // cap 1; non-blocking signal to dispatcher
	wg      sync.WaitGroup
	cancel  context.CancelFunc
	started bool
}

type entry struct {
	info VMInfo
	ring *Ring

	sched          sync.Mutex // guards everything below
	next           time.Time
	vmTier         VMTier
	idleSince      time.Time // zero ⇒ not currently idle-streaking
	lastCPUPct     float64
	lastWakeReason WakeReason
	frozenSince    time.Time // for dwell-time histograms
}

// NewPool returns a configured but not-yet-started Pool.
func NewPool(cfg PoolConfig) *Pool {
	if cfg.Cadences == (Cadences{}) {
		cfg.Cadences = DefaultCadences
	}
	if cfg.Thresh == (TierThresholds{}) {
		cfg.Thresh = DefaultTierThresholds
	}
	if cfg.StaleAfter == 0 {
		cfg.StaleAfter = DefaultStaleAfter
	}
	if cfg.Workers == 0 {
		cfg.Workers = DefaultWorkers
	}
	if cfg.ScrapeTimeout == 0 {
		cfg.ScrapeTimeout = DefaultScrapeTimeout
	}
	if cfg.FrozenCadence == 0 {
		cfg.FrozenCadence = 24 * time.Hour
	}
	if cfg.FrozenStaleAfter == 0 {
		cfg.FrozenStaleAfter = cfg.FrozenCadence + 30*time.Minute
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Pool{
		cfg:        cfg,
		classifier: NewClassifier(cfg.Thresh),
		entries:    make(map[string]*entry),
		wakeCh:     make(chan struct{}, 1),
	}
}

// Start launches the dispatcher goroutine.
func (p *Pool) Start(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return
	}
	p.started = true
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.run(runCtx)
	}()
}

// Stop terminates the dispatcher and waits for in-flight scrapes.
func (p *Pool) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.started = false
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	p.wg.Wait()
}

// Add registers a VM for scraping. Idempotent.
func (p *Pool) Add(info VMInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.entries[info.ID]; ok {
		p.entries[info.ID].info = info
		return
	}
	p.entries[info.ID] = &entry{
		info: info,
		ring: NewRing(),
		next: time.Now(), // immediate first scrape
	}
}

// Remove deregisters a VM and clears its metrics.
func (p *Pool) Remove(id string) {
	p.mu.Lock()
	e, ok := p.entries[id]
	delete(p.entries, id)
	p.mu.Unlock()
	if ok && p.cfg.Metrics != nil {
		p.cfg.Metrics.Delete(id, e.info.Name)
	}
}

// Latest returns the most recent sample for a VM (and ok=true), or zero
// when nothing has been scraped yet.
func (p *Pool) Latest(id string) (Sample, bool) {
	p.mu.RLock()
	e, ok := p.entries[id]
	p.mu.RUnlock()
	if !ok {
		return Sample{}, false
	}
	return e.ring.Latest()
}

// LatestFresh returns Latest only if the sample is within the staleness
// window. For Active VMs this is StaleAfter (90s); for Frozen VMs it is
// FrozenStaleAfter (default 24h30m) so the most recent heartbeat sample
// remains available to consumers.
func (p *Pool) LatestFresh(id string, now time.Time) (Sample, bool) {
	p.mu.RLock()
	e, ok := p.entries[id]
	p.mu.RUnlock()
	if !ok {
		return Sample{}, false
	}

	s, ok := e.ring.Latest()
	if !ok {
		return Sample{}, false
	}

	e.sched.Lock()
	tier := e.vmTier
	e.sched.Unlock()

	stale := p.cfg.StaleAfter
	if tier == VMTierFrozen {
		stale = p.cfg.FrozenStaleAfter
	}
	if now.Sub(s.FetchedAt) > stale {
		return Sample{}, false
	}
	return s, true
}

// RefaultRate returns the recent refault rate for a VM.
func (p *Pool) RefaultRate(id string, window time.Duration) float64 {
	p.mu.RLock()
	e, ok := p.entries[id]
	p.mu.RUnlock()
	if !ok {
		return 0
	}
	return e.ring.RefaultRate(window)
}

// Tier returns the current host-pressure tier.
func (p *Pool) Tier() Tier { return p.classifier.Tier() }

// Snapshot is a debug-page friendly view of all current state.
type Snapshot struct {
	Tier    Tier
	Entries []SnapshotEntry
}

// SnapshotEntry is one VM's view in a Pool snapshot.
type SnapshotEntry struct {
	ID             string
	Name           string
	Latest         Sample
	HaveLatest     bool
	RefaultRate    float64
	NumSamples     int
	VMTier         VMTier
	LastCPUPct     float64
	IdleFor        time.Duration
	FrozenFor      time.Duration
	LastWakeReason string
}

// Snapshot returns a copy of current per-VM state.
func (p *Pool) Snapshot() Snapshot {
	p.mu.RLock()
	entries := make([]*entry, 0, len(p.entries))
	ids := make([]string, 0, len(p.entries))
	for id, e := range p.entries {
		ids = append(ids, id)
		entries = append(entries, e)
	}
	p.mu.RUnlock()

	now := time.Now()
	out := Snapshot{Tier: p.classifier.Tier()}
	for i, e := range entries {
		latest, ok := e.ring.Latest()

		e.sched.Lock()
		se := SnapshotEntry{
			ID:             ids[i],
			Name:           e.info.Name,
			Latest:         latest,
			HaveLatest:     ok,
			RefaultRate:    e.ring.RefaultRate(60 * time.Second),
			NumSamples:     len(e.ring.Snapshot()),
			VMTier:         e.vmTier,
			LastCPUPct:     e.lastCPUPct,
			LastWakeReason: e.lastWakeReason.String(),
		}
		if !e.idleSince.IsZero() {
			se.IdleFor = now.Sub(e.idleSince)
		}
		if e.vmTier == VMTierFrozen && !e.frozenSince.IsZero() {
			se.FrozenFor = now.Sub(e.frozenSince)
		}
		e.sched.Unlock()

		out.Entries = append(out.Entries, se)
	}
	return out
}

// effectiveCadence composes host Tier and per-VM VMTier into a scrape interval.
// Pressured pre-empts Frozen as a defensive safety net.
func (p *Pool) effectiveCadence(host Tier, vm VMTier) time.Duration {
	if host == TierPressured {
		return p.cfg.Cadences.Pressured
	}
	if vm == VMTierFrozen {
		return p.cfg.FrozenCadence
	}
	return p.cfg.Cadences.For(host)
}

// run is the dispatcher loop. Every cycle it advances host pressure,
// chooses cadence, and fires due scrapes through a bounded worker pool.
func (p *Pool) run(ctx context.Context) {
	workers := make(chan struct{}, p.cfg.Workers)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			p.tick(ctx, now, workers)
		case <-p.wakeCh:
			p.tick(ctx, time.Now(), workers)
		}
	}
}

func (p *Pool) tick(ctx context.Context, now time.Time, workers chan struct{}) {
	// Update host tier first so cadence reflects current pressure.
	tier := p.classifier.Tier()
	if p.cfg.HostSampler != nil {
		tier = p.classifier.Update(p.cfg.HostSampler())
	}
	if p.cfg.Metrics != nil {
		p.cfg.Metrics.SetTier(tier)
	}

	p.mu.RLock()
	entries := make([]*entry, 0, len(p.entries))
	for _, e := range p.entries {
		entries = append(entries, e)
	}
	p.mu.RUnlock()

	for _, e := range entries {
		e.sched.Lock()
		// Defensive: host pressure forces wake even if NoteActivity
		// hasn't run since the line was crossed.
		if tier == TierPressured && e.vmTier == VMTierFrozen {
			p.transitionToActiveLocked(e, now, WakeHostPressure)
		}
		due := !now.Before(e.next)
		if due {
			// Tag the wake reason for metrics if this was a heartbeat.
			if e.vmTier == VMTierFrozen {
				e.lastWakeReason = WakeHeartbeat
			}
			e.next = now.Add(p.effectiveCadence(tier, e.vmTier))
		}
		e.sched.Unlock()
		if !due {
			continue
		}

		select {
		case workers <- struct{}{}:
		default:
			if p.cfg.Metrics != nil {
				p.cfg.Metrics.PoolScrapeDropped.Inc()
			}
			continue
		}
		p.wg.Add(1)
		go func(e *entry) {
			defer p.wg.Done()
			defer func() { <-workers }()
			p.scrape(ctx, e)
		}(e)
	}
}

func (p *Pool) scrape(parent context.Context, e *entry) {
	ctx, cancel := context.WithTimeout(parent, p.cfg.ScrapeTimeout)
	defer cancel()
	lbl := []string{e.info.ID, e.info.Name}
	if p.cfg.Metrics != nil {
		p.cfg.Metrics.ScrapesTotal.WithLabelValues(lbl...).Inc()
	}
	start := time.Now()
	raw, err := p.scrapeOnce(ctx, e.info)
	dur := time.Since(start)
	if p.cfg.Metrics != nil {
		p.cfg.Metrics.ScrapeDuration.Observe(dur.Seconds())
	}
	if err != nil {
		if p.cfg.Metrics != nil {
			p.cfg.Metrics.ScrapeFailures.WithLabelValues(lbl...).Inc()
		}
		p.cfg.Log.DebugContext(ctx, "guestmetrics: scrape failed",
			"vm_id", e.info.ID, "vm_name", e.info.Name, "err", err)
		return
	}
	s := FromRaw(raw, time.Now())
	e.ring.Push(s)
	if p.cfg.Metrics != nil {
		p.cfg.Metrics.Update(e.info.ID, e.info.Name, s, e.ring.RefaultRate(60*time.Second))
	}

	// Guest PSI safety net: if the scrape (including a heartbeat scrape)
	// reveals high guest memory pressure, force the VM back to Active.
	if p.cfg.Freeze.Enabled && s.PSIAvailable && s.PSIFull.Avg60 >= p.cfg.Freeze.GuestPSIWake {
		e.sched.Lock()
		if e.vmTier == VMTierFrozen {
			p.transitionToActiveLocked(e, time.Now(), WakeGuestPSI)
		}
		e.sched.Unlock()
		select {
		case p.wakeCh <- struct{}{}:
		default:
		}
	}
}

func (p *Pool) scrapeOnce(ctx context.Context, info VMInfo) (*RawSample, error) {
	dial := p.cfg.DialFunc
	if dial == nil {
		dial = func(ctx context.Context, _ string) (net.Conn, error) {
			if info.SocketPath == "" {
				return nil, fmt.Errorf("no socket path")
			}
			return vsockdial.Dial(ctx, info.SocketPath, vsockdial.MemdVsockPort)
		}
	}
	conn, err := dial(ctx, info.ID)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(d)
	}
	if _, err := io.WriteString(conn, memdRequest); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}
	// memd lives in untrusted guest userspace. Cap the response so a
	// misbehaving or malicious VM cannot OOM the host or stall a
	// scrape worker by streaming bytes without a newline. memd's real
	// payload is well under 8 KiB; 256 KiB is plenty of headroom.
	const maxResponseBytes = 256 * 1024
	br := bufio.NewReader(io.LimitReader(conn, maxResponseBytes+1))
	line, err := br.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(line) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	var raw RawSample
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if raw.Version != ProtocolVersion {
		return nil, fmt.Errorf("unsupported memd protocol version %d (want %d)", raw.Version, ProtocolVersion)
	}
	return &raw, nil
}
