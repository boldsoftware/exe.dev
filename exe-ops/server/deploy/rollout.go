package deploy

import (
	"fmt"
	"sync"
	"time"
)

// DefaultCooldown is the default wait between waves when none is specified.
const DefaultCooldown = 30 * time.Second

// RolloutTarget describes one host in a rollout. It embeds Request and adds
// the region used for wave grouping (hard region boundaries).
type RolloutTarget struct {
	Request
	Region string `json:"region"`
}

// RolloutRequest starts a phased rollout across multiple targets.
//
// Targets are grouped into waves. Region boundaries are hard: targets in
// different regions are never placed in the same wave. Within a region,
// targets are split into chunks of BatchSize, preserving the order in which
// they appear in Targets. Between waves, the orchestrator waits Cooldown
// before starting the next. If StopOnFailure is true (the default), any
// failure aborts subsequent waves.
type RolloutRequest struct {
	Targets       []RolloutTarget `json:"targets"`
	BatchSize     int             `json:"batch_size,omitempty"`
	CooldownSecs  int             `json:"cooldown_secs,omitempty"`
	StopOnFailure bool            `json:"stop_on_failure"`

	InitiatedBy string `json:"-"` // set by handler via Tailscale whois
}

// RolloutStatus is a point-in-time snapshot of a rollout, safe for JSON.
type RolloutStatus struct {
	ID            string    `json:"id"`
	Process       string    `json:"process"`
	SHA           string    `json:"sha"`
	State         string    `json:"state"` // pending, running, cooldown, paused, done, failed, cancelled
	BatchSize     int       `json:"batch_size"`
	CooldownSecs  int       `json:"cooldown_secs"`
	StopOnFailure bool      `json:"stop_on_failure"`
	StartedAt     time.Time `json:"started_at"`
	DoneAt        time.Time `json:"done_at,omitzero"`
	CooldownUntil time.Time `json:"cooldown_until,omitzero"`

	Waves       []Wave `json:"waves"`
	CurrentWave int    `json:"current_wave"` // 0-indexed; -1 once terminal

	Total     int `json:"total"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Remaining int `json:"remaining"`

	// PauseRequested is true when the user has clicked Pause but the
	// rollout has not yet reached the wave boundary. Lets the UI show a
	// "Pausing…" indicator while the current wave finishes.
	PauseRequested bool `json:"pause_requested,omitempty"`

	InitiatedBy string `json:"initiated_by,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Wave is one phase of a rollout — a group of targets deployed concurrently.
type Wave struct {
	Index     int       `json:"index"`
	Region    string    `json:"region"`
	State     string    `json:"state"` // pending, running, done, failed, cancelled, skipped
	Targets   []Target  `json:"targets"`
	StartedAt time.Time `json:"started_at,omitzero"`
	DoneAt    time.Time `json:"done_at,omitzero"`
}

// Target is one host within a wave, including its associated deploy id once
// the deploy has been spawned.
type Target struct {
	Process  string `json:"process"`
	Host     string `json:"host"`
	Region   string `json:"region"`
	Stage    string `json:"stage"`
	DeployID string `json:"deploy_id,omitempty"`
}

// rollout is the internal mutable state for a running rollout.
type rollout struct {
	mu sync.Mutex

	id            string
	process       string
	sha           string
	batchSize     int
	cooldown      time.Duration
	stopOnFailure bool
	initiatedBy   string

	state         string
	waves         []*waveState
	currentWave   int
	startedAt     time.Time
	doneAt        time.Time
	cooldownUntil time.Time
	err           string
	completed     int
	failed        int

	cancelOnce sync.Once
	cancelCh   chan struct{}

	// pauseRequested is set by requestPause and cleared by requestResume.
	// The orchestrator honors it at the next wave boundary. Guarded by mu.
	pauseRequested bool
	// unpaused is non-nil while pauseRequested is true. requestResume
	// closes it to wake the orchestrator. Guarded by mu.
	unpaused chan struct{}
	// pauseSignalCh is a buffered (cap 1) wakeup channel used to interrupt
	// the cooldown timer when pause is requested mid-cooldown. The actual
	// pause state lives in pauseRequested/unpaused; this channel exists
	// only so the orchestrator's select unblocks promptly.
	pauseSignalCh chan struct{}

	// releaseProdLocks are the prod-lock release functions taken when the
	// rollout was started, one per distinct stage. Called (and cleared)
	// by Manager.finishRollout.
	releaseProdLocks []func()
}

// waveState is the internal mutable form of a Wave.
type waveState struct {
	index     int
	region    string
	state     string
	requests  []Request
	deployIDs []string
	startedAt time.Time
	doneAt    time.Time
}

// newRollout constructs an internal rollout from a validated request.
// validation must have already happened in StartRollout.
func newRollout(id string, req RolloutRequest, waves []*waveState) *rollout {
	cooldown := DefaultCooldown
	if req.CooldownSecs > 0 {
		cooldown = time.Duration(req.CooldownSecs) * time.Second
	}
	process := ""
	if len(req.Targets) > 0 {
		process = req.Targets[0].Process
	}
	sha := ""
	if len(req.Targets) > 0 {
		sha = req.Targets[0].SHA
	}
	return &rollout{
		id:            id,
		process:       process,
		sha:           sha,
		batchSize:     effectiveBatchSize(req),
		cooldown:      cooldown,
		stopOnFailure: req.StopOnFailure,
		initiatedBy:   req.InitiatedBy,
		state:         "pending",
		waves:         waves,
		currentWave:   0,
		startedAt:     time.Now(),
		cancelCh:      make(chan struct{}),
		pauseSignalCh: make(chan struct{}, 1),
	}
}

// effectiveBatchSize returns the configured batch size or the default
// (max(1, ceil(len/3))).
func effectiveBatchSize(req RolloutRequest) int {
	if req.BatchSize > 0 {
		return req.BatchSize
	}
	n := len(req.Targets)
	if n <= 0 {
		return 1
	}
	bs := (n + 2) / 3
	if bs < 1 {
		bs = 1
	}
	return bs
}

// planWaves groups targets into waves with hard region boundaries.
// Region order is determined by the first occurrence of each region in
// the targets list. Within a region, targets are chunked into batches
// of batchSize, preserving order.
func planWaves(targets []RolloutTarget, batchSize int) []*waveState {
	if batchSize < 1 {
		batchSize = 1
	}
	// Group preserving first-seen region order.
	type regionGroup struct {
		region string
		items  []RolloutTarget
	}
	var groups []*regionGroup
	idx := map[string]int{}
	for _, t := range targets {
		i, ok := idx[t.Region]
		if !ok {
			idx[t.Region] = len(groups)
			groups = append(groups, &regionGroup{region: t.Region})
			i = idx[t.Region]
		}
		groups[i].items = append(groups[i].items, t)
	}

	var waves []*waveState
	wIdx := 0
	for _, g := range groups {
		for start := 0; start < len(g.items); start += batchSize {
			end := start + batchSize
			if end > len(g.items) {
				end = len(g.items)
			}
			chunk := g.items[start:end]
			ws := &waveState{
				index:    wIdx,
				region:   g.region,
				state:    "pending",
				requests: make([]Request, len(chunk)),
			}
			for i, t := range chunk {
				ws.requests[i] = t.Request
			}
			waves = append(waves, ws)
			wIdx++
		}
	}
	return waves
}

// snapshot returns a thread-safe copy for JSON serialization.
func (r *rollout) snapshot() RolloutStatus {
	r.mu.Lock()
	defer r.mu.Unlock()

	waves := make([]Wave, len(r.waves))
	total := 0
	completed := 0
	failed := 0
	for i, w := range r.waves {
		targets := make([]Target, len(w.requests))
		for j, req := range w.requests {
			deployID := ""
			if j < len(w.deployIDs) {
				deployID = w.deployIDs[j]
			}
			targets[j] = Target{
				Process:  req.Process,
				Host:     req.Host,
				Region:   w.region,
				Stage:    req.Stage,
				DeployID: deployID,
			}
		}
		waves[i] = Wave{
			Index:     w.index,
			Region:    w.region,
			State:     w.state,
			Targets:   targets,
			StartedAt: w.startedAt,
			DoneAt:    w.doneAt,
		}
		total += len(w.requests)
	}

	completed = r.completed
	failed = r.failed

	currentWave := r.currentWave
	if r.state == "done" || r.state == "failed" || r.state == "cancelled" {
		currentWave = -1
	}

	return RolloutStatus{
		ID:             r.id,
		Process:        r.process,
		SHA:            r.sha,
		State:          r.state,
		BatchSize:      r.batchSize,
		CooldownSecs:   int(r.cooldown / time.Second),
		StopOnFailure:  r.stopOnFailure,
		StartedAt:      r.startedAt,
		DoneAt:         r.doneAt,
		CooldownUntil:  r.cooldownUntil,
		Waves:          waves,
		CurrentWave:    currentWave,
		Total:          total,
		Completed:      completed,
		Failed:         failed,
		Remaining:      total - completed - failed,
		PauseRequested: r.pauseRequested,
		InitiatedBy:    r.initiatedBy,
		Error:          r.err,
	}
}

// requestCancel is idempotent.
func (r *rollout) requestCancel() {
	r.cancelOnce.Do(func() {
		close(r.cancelCh)
	})
}

func (r *rollout) cancelled() bool {
	select {
	case <-r.cancelCh:
		return true
	default:
		return false
	}
}

// requestPause marks the rollout as pause-requested. The orchestrator
// honors it at the next wave boundary, after the current wave finishes.
// Returns true if this call performed the request (false if already
// pause-requested or the rollout has reached a terminal state).
func (r *rollout) requestPause() bool {
	r.mu.Lock()
	if r.pauseRequested {
		r.mu.Unlock()
		return false
	}
	if r.state == "done" || r.state == "failed" || r.state == "cancelled" {
		r.mu.Unlock()
		return false
	}
	r.pauseRequested = true
	r.unpaused = make(chan struct{})
	r.mu.Unlock()

	// Wake the orchestrator if it's blocked in a cooldown timer. Drop if
	// the channel is full — the flag is the source of truth, the signal
	// is just a wakeup.
	select {
	case r.pauseSignalCh <- struct{}{}:
	default:
	}
	return true
}

// requestResume clears the pause flag and unblocks the orchestrator if
// it's waiting on the unpaused channel. Returns true if this call
// performed a resume (false if the rollout was not paused).
func (r *rollout) requestResume() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.pauseRequested {
		return false
	}
	r.pauseRequested = false
	if r.unpaused != nil {
		close(r.unpaused)
		r.unpaused = nil
	}
	return true
}

// pauseGate returns the current unpaused channel if the rollout is
// pause-requested, else nil. Callers select on it (along with cancelCh
// and the server context) to wait for resume.
func (r *rollout) pauseGate() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.pauseRequested {
		return nil
	}
	return r.unpaused
}

// rolloutLockKey is the per-process exclusion key. Only one rollout per
// process may be active at a time, regardless of stage/region.
func rolloutLockKey(req RolloutRequest) string {
	if len(req.Targets) == 0 {
		return ""
	}
	return req.Targets[0].Process
}

// rolloutValidate runs all of validateRequest plus rollout-level invariants.
// Returns a single error describing the first failure.
func (m *Manager) rolloutValidate(req RolloutRequest) error {
	if len(req.Targets) == 0 {
		return fmt.Errorf("rollout requires at least one target")
	}
	process := req.Targets[0].Process
	sha := req.Targets[0].SHA
	for i, t := range req.Targets {
		if t.Process != process {
			return fmt.Errorf("rollout targets must share a process: target %d has %q, want %q", i, t.Process, process)
		}
		if t.SHA != sha {
			return fmt.Errorf("rollout targets must share a SHA: target %d differs", i)
		}
		if t.Region == "" {
			return fmt.Errorf("rollout target %d (%s) is missing region", i, t.Host)
		}
		if err := m.validateRequest(t.Request); err != nil {
			return fmt.Errorf("target %d (%s): %w", i, t.Host, err)
		}
	}
	return nil
}
