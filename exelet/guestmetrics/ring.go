package guestmetrics

import (
	"sync"
	"time"
)

// RingCapacity is the number of samples retained per VM. 60 × 5s = 5 min of
// history at the pressured cadence; 60 × 60s = 1 h at the calm cadence.
const RingCapacity = 60

// Ring is a fixed-capacity ring buffer of samples. Safe for concurrent use.
type Ring struct {
	mu   sync.Mutex
	buf  []Sample
	next int // index where the next Push will write
	len  int // number of valid entries in buf
}

// NewRing returns an empty ring with capacity RingCapacity.
func NewRing() *Ring {
	return &Ring{buf: make([]Sample, RingCapacity)}
}

// Push appends a sample, dropping the oldest when full.
func (r *Ring) Push(s Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = s
	r.next = (r.next + 1) % RingCapacity
	if r.len < RingCapacity {
		r.len++
	}
}

// Latest returns the most recent sample and ok=true, or zero/false when
// the ring is empty.
func (r *Ring) Latest() (Sample, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.len == 0 {
		return Sample{}, false
	}
	idx := (r.next - 1 + RingCapacity) % RingCapacity
	return r.buf[idx], true
}

// Snapshot returns a copy of all retained samples in chronological order
// (oldest first).
func (r *Ring) Snapshot() []Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Sample, 0, r.len)
	start := (r.next - r.len + RingCapacity) % RingCapacity
	for i := 0; i < r.len; i++ {
		out = append(out, r.buf[(start+i)%RingCapacity])
	}
	return out
}

// Prev returns a copy of the second-most-recent sample and ok=true, or
// zero/false when fewer than two samples are retained. Used by the RM
// rollup to compute deltas between consecutive scrapes.
func (r *Ring) Prev() (Sample, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.len < 2 {
		return Sample{}, false
	}
	idx := (r.next - 2 + RingCapacity) % RingCapacity
	return r.buf[idx], true
}

// RefaultRate returns refault deltas per second over the available
// history, capped at the most recent `window` of wall-clock time.
// Returns 0 when fewer than 2 samples are within the window or when
// counters reset (snapshot/restore).
func (r *Ring) RefaultRate(window time.Duration) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.len < 2 {
		return 0
	}
	latestIdx := (r.next - 1 + RingCapacity) % RingCapacity
	latest := r.buf[latestIdx]
	cutoff := latest.CapturedAt.Add(-window)
	// Walk backwards from newest until we step past the window or run
	// out of samples; the last index that is still within (or exactly at)
	// the cutoff becomes our oldest.
	oldestIdx := -1
	for i := 1; i <= r.len; i++ {
		idx := (r.next - i + RingCapacity) % RingCapacity
		s := r.buf[idx]
		if s.CapturedAt.Before(cutoff) {
			break
		}
		oldestIdx = idx
	}
	if oldestIdx == -1 || oldestIdx == latestIdx {
		return 0
	}
	oldest := r.buf[oldestIdx]
	elapsed := latest.CapturedAt.Sub(oldest.CapturedAt).Seconds()
	if elapsed <= 0 {
		return 0
	}
	if latest.WorkingsetRefaultFile < oldest.WorkingsetRefaultFile {
		// Counter reset (snapshot+restore). Treat as no signal.
		return 0
	}
	delta := latest.WorkingsetRefaultFile - oldest.WorkingsetRefaultFile
	return float64(delta) / elapsed
}
