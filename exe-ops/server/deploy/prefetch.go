package deploy

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// maxConcurrentPrefetch caps how many simultaneous ssh upload streams the
// rollout prefetcher will open across all waves. The goal is to start
// every target's transfer well before its install moment without
// thrashing file descriptors or saturating the build host's uplink.
const maxConcurrentPrefetch = 32

// prefetcher streams binaries to each rollout target's /tmp ahead of the
// wave that will install them. It exists to keep slow uploads on
// distant targets (e.g. exeprox over high-latency links) off the
// critical path between waves.
//
// Safety relies on three properties of the existing upload pipeline
// (see Manager.upload):
//
//  1. Upload streams to /tmp/deploy-{binary}-{sha[:12]} (a deterministic
//     path keyed by the artifact SHA).
//  2. It then sudo-mv's the temp file to a versioned RemoteDir path.
//  3. install symlinks the new path into place.
//
// Until a deploy reaches its install step, nothing in RemoteDir is
// touched and the running service is undisturbed. A target that gets a
// prefetched temp file but never reaches install just leaves the file
// in /tmp — vestigial and self-evident.
type prefetcher struct {
	m       *Manager
	rollout *rollout
	sem     chan struct{}

	// states is keyed by Request.DNSName. Entries are created up front
	// in newPrefetcher so deploys can look up their state without a lock
	// after the prefetcher is started.
	states map[string]*prefetchState

	// done is closed once run has returned (every per-host goroutine
	// has finished). Used by runRollout to wait for prefetch goroutines
	// to drain on rollout shutdown.
	done chan struct{}

	// cancel cancels the prefetch context. Called when the rollout is
	// cancelled or the manager is shutting down.
	cancel context.CancelFunc
}

// prefetchState records the outcome of one host's prefetch. done is
// closed exactly once when the prefetch is settled. After done is
// closed, err and tmpPath are read-only.
type prefetchState struct {
	tmpPath string
	bytes   int64
	dur     time.Duration
	err     error
	done    chan struct{}
}

// newPrefetcher builds a prefetcher and pre-creates a state entry for
// every distinct target in the rollout's waves. It does not start any
// goroutines — call start for that.
func newPrefetcher(m *Manager, r *rollout) *prefetcher {
	p := &prefetcher{
		m:       m,
		rollout: r,
		sem:     make(chan struct{}, maxConcurrentPrefetch),
		states:  make(map[string]*prefetchState),
		done:    make(chan struct{}),
	}
	for _, w := range r.waves {
		for _, req := range w.requests {
			if _, ok := p.states[req.DNSName]; ok {
				continue
			}
			p.states[req.DNSName] = &prefetchState{done: make(chan struct{})}
		}
	}
	return p
}

// start launches the prefetcher goroutines. The supplied parent context
// should be the manager's lifetime context; start derives a child
// cancelled when the rollout is cancelled.
func (p *prefetcher) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel

	// Watch for rollout cancellation; cancel the prefetch context so
	// in-flight ssh streams abort promptly.
	go func() {
		select {
		case <-p.rollout.cancelCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	go func() {
		defer close(p.done)
		defer cancel()
		p.run(ctx)
	}()
}

// stateFor returns the prefetch entry for a target, or nil if the
// rollout had no prefetcher attached. Safe to call before start.
func (p *prefetcher) stateFor(dnsName string) *prefetchState {
	if p == nil {
		return nil
	}
	return p.states[dnsName]
}

// wait blocks until every per-host prefetch has settled (success,
// failure, or context cancellation). Safe to call multiple times.
func (p *prefetcher) wait() {
	if p == nil {
		return
	}
	<-p.done
}

// run builds the artifact (a no-op if some other deploy already
// triggered the same build) and then dispatches a prefetch goroutine
// per target. It returns when every host's prefetch has settled.
func (p *prefetcher) run(ctx context.Context) {
	r := p.rollout
	log := p.m.log
	if len(p.states) == 0 {
		return
	}

	recipe, ok := Recipes[r.process]
	if !ok {
		// Should be impossible — rolloutValidate checked this — but
		// fail closed: every per-deploy upload will fall back to its
		// own inline upload path, which will produce the real error.
		p.failAll(fmt.Errorf("prefetch: unknown process %q", r.process))
		return
	}

	// Build the artifact up front. ensureArtifact is dedup'd by
	// (process, sha); per-deploy build steps will hit the on-disk cache.
	// We pass a stub *deploy so the existing build pipeline can call
	// setStepOutput without nil checks — its empty step list makes
	// every setStepOutput a no-op.
	stub := &deploy{sha: r.sha}
	artifact, _, err := p.m.ensureArtifact(ctx, stub, r.process, r.sha, recipe)
	if err != nil {
		p.failAll(fmt.Errorf("prefetch build: %w", err))
		log.Warn("rollout prefetch build failed", "id", r.id, "err", err)
		return
	}

	info, err := os.Stat(artifact)
	if err != nil {
		p.failAll(fmt.Errorf("prefetch stat artifact: %w", err))
		log.Warn("rollout prefetch stat failed", "id", r.id, "err", err)
		return
	}
	size := info.Size()

	var wg sync.WaitGroup
	for dns, st := range p.states {
		dns, st := dns, st
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.runOne(ctx, recipe, artifact, size, dns, st)
		}()
	}
	wg.Wait()
}

// runOne uploads the artifact to one target. It always closes st.done
// before returning, with st.err set on failure.
func (p *prefetcher) runOne(ctx context.Context, recipe Recipe, artifact string, size int64, dns string, st *prefetchState) {
	defer close(st.done)

	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		st.err = ctx.Err()
		return
	}
	defer func() { <-p.sem }()

	if err := ctx.Err(); err != nil {
		st.err = err
		return
	}

	tmpPath, n, dur, err := p.m.streamToTmp(ctx, nil, recipe, p.rollout.sha, dns, artifact, size)
	st.tmpPath = tmpPath
	st.bytes = n
	st.dur = dur
	st.err = err
	if err != nil {
		p.m.log.Warn("rollout prefetch failed",
			"id", p.rollout.id, "host", dns, "err", err)
		return
	}
	mbps := 0.0
	if dur > 0 {
		mbps = float64(n) / 1024 / 1024 / dur.Seconds()
	}
	p.m.log.Info("rollout prefetch ok",
		"id", p.rollout.id, "host", dns,
		"bytes", n, "duration", dur.Round(time.Millisecond),
		"mbps", fmt.Sprintf("%.1f", mbps))
}

// failAll closes any prefetch state that has not yet been settled,
// recording err on each. Used when the up-front build fails so that
// per-deploy upload steps fall back to their inline upload path
// (which will surface the real error).
func (p *prefetcher) failAll(err error) {
	for _, st := range p.states {
		select {
		case <-st.done:
		default:
			st.err = err
			close(st.done)
		}
	}
}
