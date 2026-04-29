package resourcemanager

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"time"
)

// idleCacheDropWindow is the lookback over which we compute average CPU
// usage to decide whether a VM is "idle".
const idleCacheDropWindow = 30 * time.Minute

// idleCacheDropCPUThreshold is the CPU percent below which the average
// over the lookback window must stay for a VM to be considered idle.
// CPU percent here is the same units we report elsewhere (100% = 1 core).
const idleCacheDropCPUThreshold = 0.1

// idleCacheDropProbability is the per-poll probability of probing an idle
// VM's page cache. With a 30s poll interval and 30-min eligibility window,
// 1/1000 yields roughly one probe every ~8 hours per idle VM. Note that
// the rate scales with ResourceManagerInterval — a slower poll yields
// proportionally fewer probes.
const idleCacheDropProbability = 1.0 / 1000.0

// idleCacheDropTimeout is the upper bound on the entire drop flow, including
// the HTTP call to exed and the SSH session into the VM. The whole thing
// runs in a background goroutine — we never block the poll loop on it.
const idleCacheDropTimeout = 60 * time.Second

// cpuSample is one observation of cumulative CPU seconds at a given time.
type cpuSample struct {
	t          time.Time
	cpuSeconds float64
}

// idleProbe holds in-memory bookkeeping the resource manager keeps for the
// idle-VM page-cache probe. It lives on vmUsageState.
//
// The state here is intentionally in-memory only — across exelet restarts we
// forget what we saw last time and may re-drop with a smaller delta than the
// 20% threshold would otherwise allow. That's harmless (drop_caches on idle
// data is cheap) and avoids needing a persistence path. If we ever want to
// suppress redundant drops across restarts, persist {lastDropMemoryBytes,
// lastDropTime} per container_id alongside other per-VM state.
type idleProbe struct {
	samples []cpuSample

	// lastDropMemoryBytes is the post-drop "memory.current minus the file
	// component" value from the most recent successful probe. We only re-fire
	// once the same metric has grown by ≥20%, so an idle VM whose working
	// set is genuinely small does not get dropped repeatedly. Zero means
	// "no successful drop yet", which always passes the gate.
	lastDropMemoryBytes uint64
}

// idleCacheDropMemoryGrowthThreshold is the minimum fractional growth in
// (memory.current - memory.file) required since the last successful drop
// before we'll probe again. With a 20% threshold an idle VM whose working
// set has not changed will never re-trigger.
const idleCacheDropMemoryGrowthThreshold = 0.20

// recordCPUSample appends a sample and trims old ones, leaving enough
// history to span the full lookback window. We keep at most
// (window + slack) of history; slack is sized to ride out reasonable
// poll-interval variation.
func (p *idleProbe) recordCPUSample(t time.Time, cpuSeconds float64) {
	// Defend against non-monotonic time (NTP step backwards). If the new
	// sample is older than the most recent sample we already have, drop
	// the prior history; the in-order invariant the rest of the code
	// relies on is otherwise corrupted.
	if n := len(p.samples); n > 0 && t.Before(p.samples[n-1].t) {
		p.samples = p.samples[:0]
	}
	p.samples = append(p.samples, cpuSample{t: t, cpuSeconds: cpuSeconds})
	// Slack of 2 windows is intentionally generous: even with poll
	// intervals an order of magnitude larger than the default we still
	// retain a sample old enough to compute the rolling average.
	cutoff := t.Add(-2 * idleCacheDropWindow)
	// Drop samples strictly older than cutoff while keeping the oldest
	// one that is still older than (t - window) so we can compute over
	// the full window.
	keepFrom := 0
	for i, s := range p.samples {
		if s.t.Before(cutoff) {
			keepFrom = i + 1
			continue
		}
		break
	}
	if keepFrom > 0 {
		p.samples = p.samples[keepFrom:]
	}
}

// avgCPUPercent returns the average CPU percent computed from the oldest
// retained sample (which is at most ~2 × window old) up to the most
// recent sample, provided that span covers at least idleCacheDropWindow.
// Returns (0, false) when we don't yet have enough history. The actual
// span can be wider than the window; that's fine — a wider window means
// any momentary CPU bursts get averaged in, which is what we want.
func (p *idleProbe) avgCPUPercent(now time.Time) (float64, bool) {
	if len(p.samples) < 2 {
		return 0, false
	}
	cutoff := now.Add(-idleCacheDropWindow)
	// Find the oldest sample at or before cutoff.
	var base *cpuSample
	for i := range p.samples {
		s := &p.samples[i]
		if !s.t.After(cutoff) {
			base = s
		} else {
			break
		}
	}
	if base == nil {
		return 0, false
	}
	last := p.samples[len(p.samples)-1]
	elapsed := last.t.Sub(base.t).Seconds()
	if elapsed < idleCacheDropWindow.Seconds() {
		// Not enough span yet.
		return 0, false
	}
	delta := last.cpuSeconds - base.cpuSeconds
	if delta < 0 {
		delta = 0
	}
	return (delta / elapsed) * 100.0, true
}

// maybeProbeIdleCacheDrop is called once per poll for each running VM. It
// records a CPU sample and, with low probability, kicks off a background
// goroutine that asks exed to SSH into the VM and drop its guest page
// cache. Logging captures the cgroup memory.current before and after so we
// can tell what (if anything) the drop reclaimed.
//
// It is safe for the goroutine to outlive the poll cycle. We never block
// the caller.
func (m *ResourceManager) maybeProbeIdleCacheDrop(ctx context.Context, id, name, groupID string, now time.Time, cpuSeconds float64) {
	if m.config == nil || m.config.ExedURL == "" {
		return // exed URL not configured; nothing to call.
	}
	// Singleflight: if a probe is already running for any VM on this
	// exelet, skip. We never queue — idle VMs are sampled often enough
	// that a missed roll is fine. CAS happens after the cheap gates so
	// we don't take and immediately drop the slot for skipped probes.
	m.usageMu.Lock()
	state := m.usageState[id]
	if state == nil {
		m.usageMu.Unlock()
		return
	}
	if state.idle == nil {
		state.idle = &idleProbe{}
	}
	state.idle.recordCPUSample(now, cpuSeconds)
	avg, ok := state.idle.avgCPUPercent(now)
	if !ok {
		m.usageMu.Unlock()
		return
	}
	if avg >= idleCacheDropCPUThreshold {
		m.usageMu.Unlock()
		return
	}
	// 20% memory-growth gate: skip unless (memory.current - memory.file) has
	// grown by at least 20% since the last successful drop. Uses cached
	// usage from the just-completed poll; readers of the same fields stay
	// inside usageMu.
	nonFileNow := nonFileMemoryBytes(state.memoryBytes, state.memoryFileBytes)
	lastDrop := state.idle.lastDropMemoryBytes
	if lastDrop > 0 && !memoryGrew(nonFileNow, lastDrop, idleCacheDropMemoryGrowthThreshold) {
		m.usageMu.Unlock()
		return
	}
	// Roll the dice. 1/1000 per poll for idle VMs.
	if m.idleCacheDropRand() >= idleCacheDropProbability {
		m.usageMu.Unlock()
		return
	}
	if !m.dropInflight.CompareAndSwap(false, true) {
		m.usageMu.Unlock()
		return // another probe already in flight; skip
	}
	cgroupPath := m.vmCgroupPath(id, groupID)
	swapBefore := state.swapBytes
	fileBefore := state.memoryFileBytes
	currentBefore := state.memoryBytes
	m.usageMu.Unlock()

	exedURL := m.config.ExedURL
	logger := m.log

	go func() {
		defer m.dropInflight.Store(false)
		bgCtx, cancel := context.WithTimeout(context.Background(), idleCacheDropTimeout)
		defer cancel()

		nonFileBefore := nonFileMemoryBytes(currentBefore, fileBefore)
		logger.InfoContext(bgCtx, "resource manager: probing idle VM page cache",
			"id", id,
			"name", name,
			"group_id", groupID,
			"avg_cpu_percent", avg,
			"memory_excl_file_before_bytes", nonFileBefore,
			"memory_swap_before_bytes", swapBefore,
			"last_drop_memory_excl_file_bytes", lastDrop,
		)

		err := requestExedDropPageCache(bgCtx, exedURL, id)

		// Re-read host cgroup figures directly: by the time the goroutine
		// returns, the next poll may not have run yet.
		currentAfter, _ := m.readCgroupMemory(cgroupPath)
		swapAfter, _ := m.readCgroupSwap(cgroupPath)
		var fileAfter uint64
		if b, statErr := m.readCgroupMemoryStat(cgroupPath); statErr == nil {
			fileAfter = b.file
		}
		nonFileAfter := nonFileMemoryBytes(currentAfter, fileAfter)

		if err != nil {
			logger.WarnContext(bgCtx, "resource manager: page cache drop probe failed",
				"id", id,
				"name", name,
				"memory_excl_file_before_bytes", nonFileBefore,
				"memory_excl_file_after_bytes", nonFileAfter,
				"memory_swap_before_bytes", swapBefore,
				"memory_swap_after_bytes", swapAfter,
				"error", err,
			)
			return
		}

		// Successful drop: stamp the new baseline so the 20%-growth gate
		// fires off the post-drop floor, not the pre-drop high-water mark.
		m.usageMu.Lock()
		if s := m.usageState[id]; s != nil && s.idle != nil {
			s.idle.lastDropMemoryBytes = nonFileAfter
		}
		m.usageMu.Unlock()

		logger.InfoContext(bgCtx, "resource manager: page cache drop probe done",
			"id", id,
			"name", name,
			"memory_excl_file_before_bytes", nonFileBefore,
			"memory_excl_file_after_bytes", nonFileAfter,
			"memory_excl_file_delta_bytes", signedDelta(nonFileBefore, nonFileAfter),
			"memory_swap_before_bytes", swapBefore,
			"memory_swap_after_bytes", swapAfter,
		)
	}()
}

// nonFileMemoryBytes returns memory.current minus the page-cache (file)
// component, clamped at zero. For a cloud-hypervisor VM this is the part of
// the cgroup charge that is *not* host-side disk page cache — i.e., what's
// most relevant when reasoning about whether the guest's working set is
// actually growing.
func nonFileMemoryBytes(current, file uint64) uint64 {
	if file >= current {
		return 0
	}
	return current - file
}

// memoryGrew reports whether cur exceeds prev by at least frac (0.20 = 20%).
func memoryGrew(cur, prev uint64, frac float64) bool {
	return float64(cur) >= float64(prev)*(1.0+frac)
}

// signedDelta returns after-before as a signed integer.
func signedDelta(before, after uint64) int64 {
	if before > after {
		return -int64(before - after)
	}
	return int64(after - before)
}

// idleCacheDropRand returns a uniform [0,1). Indirected so tests can hook in.
func (m *ResourceManager) idleCacheDropRand() float64 {
	if m.idleCacheDropRandFn != nil {
		return m.idleCacheDropRandFn()
	}
	return rand.Float64()
}

// requestExedDropPageCache POSTs to exed's /exelet-drop-page-cache endpoint.
// We hit it via HTTP because that's how the exelet already talks to exed
// (cf. desiredsync). It's expected to be a fast no-op or a slow SSH; we
// rely on bgCtx for the timeout.
func requestExedDropPageCache(ctx context.Context, exedURL, containerID string) error {
	form := url.Values{
		"container_id": {containerID},
	}
	endpoint := fmt.Sprintf("%s/exelet-drop-page-cache", exedURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("exed status %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	return nil
}
