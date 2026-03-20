package deploy

import (
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

	State     string    `json:"state"` // pending, running, done, failed
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

	state     string
	steps     []Step
	startedAt time.Time
	doneAt    time.Time
	err       string
}

// StepNames returns the deploy steps for a given process, accounting for
// optional steps like "backup" when PreRestartCmds are configured.
func StepNames(process string) []string {
	steps := []string{"build", "upload", "install"}
	if r, ok := Recipes[process]; ok && len(r.PreRestartCmds) > 0 {
		steps = append(steps, "backup")
	}
	steps = append(steps, "restart", "verify")
	return steps
}

func newDeploy(id, stage, role, process, host, dnsName, sha, initiatedBy string) *deploy {
	names := StepNames(process)
	steps := make([]Step, len(names))
	for i, name := range names {
		steps[i] = Step{Name: name, Status: "pending"}
	}
	return &deploy{
		id:          id,
		stage:       stage,
		role:        role,
		process:     process,
		host:        host,
		dnsName:     dnsName,
		sha:         sha,
		initiatedBy: initiatedBy,
		state:       "pending",
		steps:       steps,
		startedAt:   time.Now(),
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
func (d *deploy) setStepOutput(output string) {
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
// Returns true when err != nil, signaling the caller to abort.
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
			d.steps[i].Status = "failed"
			d.steps[i].Output = err.Error()
			d.state = "failed"
			d.doneAt = now
			d.err = err.Error()
		} else {
			d.steps[i].Status = "done"
			// Output may already be set by setStepOutput; keep it.
		}
		break
	}
	return err != nil
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
