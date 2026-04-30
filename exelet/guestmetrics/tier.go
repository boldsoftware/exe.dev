package guestmetrics

import (
	"sync/atomic"
	"time"
)

// Tier is a host-pressure level used to select scrape cadence. Three tiers
// with hysteresis: enter pressured when MemAvail<10% OR PSI full60>=5%;
// leave pressured only when MemAvail>20% AND PSI full60<1%.
type Tier int

const (
	TierCalm Tier = iota
	TierNormal
	TierPressured
)

func (t Tier) String() string {
	switch t {
	case TierCalm:
		return "calm"
	case TierNormal:
		return "normal"
	case TierPressured:
		return "pressured"
	}
	return "unknown"
}

// HostSample is the subset of host metrics the tier classifier needs.
type HostSample struct {
	MemTotalBytes     uint64
	MemAvailableBytes uint64
	PSIFullAvg60      float64 // percent, 0..100
	PSISomeAvg60      float64
}

// AvailFraction returns MemAvailable/MemTotal in [0,1]. Returns 1 (i.e.,
// "plenty") for malformed inputs so missing data fails open.
func (h HostSample) AvailFraction() float64 {
	if h.MemTotalBytes == 0 {
		return 1
	}
	return float64(h.MemAvailableBytes) / float64(h.MemTotalBytes)
}

// TierThresholds holds the hysteresis bounds for the classifier. Defaults
// match the synthesis plan.
type TierThresholds struct {
	EnterAvailFrac float64 // <= triggers pressured
	EnterPSIFull   float64 // >= triggers pressured
	ExitAvailFrac  float64 // > required to leave pressured
	ExitPSIFull    float64 // < required to leave pressured
}

// DefaultTierThresholds matches the merged plan: enter at 10%/5%, exit at
// 20%/1%.
var DefaultTierThresholds = TierThresholds{
	EnterAvailFrac: 0.10,
	EnterPSIFull:   5.0,
	ExitAvailFrac:  0.20,
	ExitPSIFull:    1.0,
}

// Cadences holds per-tier scrape intervals.
type Cadences struct {
	Calm      time.Duration
	Normal    time.Duration
	Pressured time.Duration
}

// DefaultCadences: 60s/15s/5s.
var DefaultCadences = Cadences{
	Calm:      60 * time.Second,
	Normal:    15 * time.Second,
	Pressured: 5 * time.Second,
}

// For returns the cadence for the given tier.
func (c Cadences) For(t Tier) time.Duration {
	switch t {
	case TierPressured:
		return c.Pressured
	case TierNormal:
		return c.Normal
	}
	return c.Calm
}

// Classifier maintains the current Tier with hysteresis.
//
// current is stored as an int32 inside an atomic so Tier() (called from
// the HTTP debug handler and the resource-manager poll) can race with
// Update() (called from the dispatcher) without data races. Update()
// itself is not internally serialised — callers must serialise
// concurrent Update() calls if they want strictly monotonic
// transitions. In practice only the dispatcher calls Update().
type Classifier struct {
	Thresh  TierThresholds
	current atomic.Int32 // Tier value
}

// NewClassifier returns a fresh classifier starting in TierCalm.
func NewClassifier(t TierThresholds) *Classifier {
	c := &Classifier{Thresh: t}
	c.current.Store(int32(TierCalm))
	return c
}

// Tier returns the current tier.
func (c *Classifier) Tier() Tier { return Tier(c.current.Load()) }

// Update applies a host sample and returns the (possibly unchanged) tier.
//
// Logic:
//   - If currently calm/normal and the host crosses the entry thresholds,
//     jump to pressured.
//   - If currently pressured, only step down to normal once *both* exit
//     thresholds are satisfied.
//   - Calm vs normal is decided directly off the entry thresholds (the
//     halfway point is fine; the tiers exist to gate cadence, and
//     conservatively choosing normal under any sign of pressure is
//     intentional).
func (c *Classifier) Update(h HostSample) Tier {
	avail := h.AvailFraction()
	prev := Tier(c.current.Load())
	next := prev
	switch prev {
	case TierPressured:
		if avail > c.Thresh.ExitAvailFrac && h.PSIFullAvg60 < c.Thresh.ExitPSIFull {
			next = TierNormal
		}
	default:
		switch {
		case avail <= c.Thresh.EnterAvailFrac || h.PSIFullAvg60 >= c.Thresh.EnterPSIFull:
			next = TierPressured
		case avail < c.Thresh.ExitAvailFrac || h.PSIFullAvg60 >= c.Thresh.ExitPSIFull:
			next = TierNormal
		default:
			next = TierCalm
		}
	}
	c.current.Store(int32(next))
	return next
}
