package deploy

import (
	"context"
	"sync"
	"time"
)

// Status is a point-in-time snapshot of a deploy, safe for JSON.
type Status struct {
	ID      string `json:"id"`
	Stage   string `json:"stage"`
	Role    string `json:"role"`
	Process string `json:"process"`
	Host    string `json:"host"`
	DNSName string `json:"dns_name"`
	SHA     string `json:"sha"`

	InitiatedBy string `json:"initiated_by,omitempty"` // Tailscale user login, if known
	RolloutID   string `json:"rollout_id,omitempty"`   // set when deploy was started by a rollout

	State     string    `json:"state"` // pending, running, done, failed, cancelled
	Steps     []Step    `json:"steps"`
	StartedAt time.Time `json:"started_at"`
	DoneAt    time.Time `json:"done_at,omitzero"`
	Error     string    `json:"error,omitempty"`
}

// Step tracks one phase of a deploy.
type Step struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"` // pending, running, done, failed
	StartedAt time.Time `json:"started_at,omitzero"`
	DoneAt    time.Time `json:"done_at,omitzero"`
	Output    string    `json:"output,omitempty"`
}

// deploy is the internal mutable state for a running deploy.
type deploy struct {
	mu sync.Mutex

	id          string
	stage       string
	role        string
	process     string
	host        string
	dnsName     string
	sha         string
	initiatedBy string
	rolloutID   string

	state     string
	steps     []Step
	startedAt time.Time
	doneAt    time.Time
	err       string

	// ctx is derived from the manager context and used by execute() for
	// all I/O. cancel aborts in-flight work — the rollout orchestrator
	// calls it when a cancel is requested during the active wave, and
	// Manager.Cancel calls it for single-deploy cancels.
	ctx    context.Context
	cancel context.CancelFunc

	// cancelRequested is set before cancel() is invoked when the abort
	// originated from a user cancel (single deploy or rollout cancel).
	// stepDone uses it to mark the terminal state as "cancelled" rather
	// than "failed". Guarded by mu.
	cancelRequested bool

	// done is closed when the deploy reaches a terminal state. Allows
	// the rollout orchestrator to await wave completion without polling.
	done chan struct{}

	// releaseProdLock releases the prod-lock taken on behalf of this deploy.
	// Set only for single-host deploys started via Manager.Start; nil for
	// rollout-driven deploys (the rollout owns that lock). Called from
	// Manager.finish.
	releaseProdLock func()

	// prefetched, when non-nil, is the rollout-level prefetch entry for
	// this deploy's target. The upload step waits on it and skips its
	// stream-to-/tmp phase if the prefetch already wrote the same temp
	// file. nil for single-host deploys and for rollouts running under a
	// test runner. See prefetch.go.
	prefetched *prefetchState
}

// StepNames returns the deploy steps for a given process, accounting for
// optional steps like "service" and "backup" when configured.
func StepNames(process string) []string {
	steps := []string{"build", "upload", "install"}
	if r, ok := Recipes[process]; ok && len(r.ServiceFiles) > 0 {
		steps = append(steps, "service")
	}
	if r, ok := Recipes[process]; ok && len(r.PreRestartCmds) > 0 {
		steps = append(steps, "backup")
	}
	if r, ok := Recipes[process]; ok && len(r.PreflightCmds) > 0 {
		steps = append(steps, "preflight")
	}
	steps = append(steps, "restart", "verify")
	return steps
}

func newDeploy(parent context.Context, id, stage, role, process, host, dnsName, sha, initiatedBy, rolloutID string) *deploy {
	names := StepNames(process)
	steps := make([]Step, len(names))
	for i, name := range names {
		steps[i] = Step{Name: name, Status: "pending"}
	}
	ctx, cancel := context.WithCancel(parent)
	return &deploy{
		id:          id,
		stage:       stage,
		role:        role,
		process:     process,
		host:        host,
		dnsName:     dnsName,
		sha:         sha,
		initiatedBy: initiatedBy,
		rolloutID:   rolloutID,
		state:       "pending",
		steps:       steps,
		startedAt:   time.Now(),
		ctx:         ctx,
		cancel:      cancel,
		done:        make(chan struct{}),
	}
}

// snapshot returns a thread-safe copy for JSON serialization.
func (d *deploy) snapshot() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := Status{
		ID:          d.id,
		Stage:       d.stage,
		Role:        d.role,
		Process:     d.process,
		Host:        d.host,
		DNSName:     d.dnsName,
		SHA:         d.sha,
		InitiatedBy: d.initiatedBy,
		RolloutID:   d.rolloutID,
		State:       d.state,
		Steps:       make([]Step, len(d.steps)),
		StartedAt:   d.startedAt,
		DoneAt:      d.doneAt,
		Error:       d.err,
	}
	copy(s.Steps, d.steps)
	return s
}

func (d *deploy) beginStep(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state = "running"
	for i := range d.steps {
		if d.steps[i].Name == name {
			d.steps[i].Status = "running"
			d.steps[i].StartedAt = time.Now()
			return
		}
	}
}

// setStepOutput records informational output on the current running step.
// Safe to call on a nil receiver (used by prebuild, which has no deploy).
func (d *deploy) setStepOutput(output string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.steps {
		if d.steps[i].Status == "running" {
			d.steps[i].Output = output
			return
		}
	}
}

// stepDone marks the current running step as done (err==nil) or failed.
// Returns true when err != nil, signaling the caller to abort. When the
// abort was triggered by a user cancel (cancelRequested), the deploy ends
// in the "cancelled" state instead of "failed".
func (d *deploy) stepDone(err error) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for i := range d.steps {
		if d.steps[i].Status != "running" {
			continue
		}
		d.steps[i].DoneAt = now
		if err != nil {
			if d.cancelRequested {
				d.steps[i].Status = "cancelled"
				d.steps[i].Output = "cancelled by user"
				d.state = "cancelled"
				d.doneAt = now
				d.err = "cancelled by user"
			} else {
				d.steps[i].Status = "failed"
				d.steps[i].Output = err.Error()
				d.state = "failed"
				d.doneAt = now
				d.err = err.Error()
			}
		} else {
			d.steps[i].Status = "done"
			// Output may already be set by setStepOutput; keep it.
		}
		break
	}
	return err != nil
}

// requestCancel marks the deploy as user-cancelled and aborts its context.
// Safe to call from any goroutine; safe to call multiple times. Returns
// true if this call performed the cancel (false if it was already cancelled
// or the deploy is already terminal).
func (d *deploy) requestCancel() bool {
	d.mu.Lock()
	if d.cancelRequested {
		d.mu.Unlock()
		return false
	}
	if d.state == "done" || d.state == "failed" || d.state == "cancelled" {
		d.mu.Unlock()
		return false
	}
	d.cancelRequested = true
	cancel := d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

func (d *deploy) complete() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state = "done"
	d.doneAt = time.Now()
}

// activeKey is the uniqueness key that prevents concurrent deploys
// to the same target.
func (d *deploy) activeKey() string {
	return d.stage + "/" + d.role + "/" + d.process + "/" + d.host
}
