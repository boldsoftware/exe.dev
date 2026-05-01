package guestmetrics

import "time"

// VMTier is the per-VM activity tier, orthogonal to the host-level Tier.
// Active VMs are scraped at the host-tier cadence; Frozen VMs are scraped
// on a much longer heartbeat cadence (default 24h) and only wake on
// observed host-side activity.
type VMTier int8

const (
	VMTierActive VMTier = iota
	VMTierFrozen
)

func (t VMTier) String() string {
	switch t {
	case VMTierActive:
		return "active"
	case VMTierFrozen:
		return "frozen"
	}
	return "unknown"
}

// MarshalJSON encodes VMTier as a JSON string.
func (t VMTier) MarshalJSON() ([]byte, error) {
	return []byte(`"` + t.String() + `"`), nil
}

// WakeReason identifies what triggered a Frozen→Active transition.
type WakeReason uint8

const (
	WakeNone WakeReason = iota
	WakeCPU
	WakeHostPressure
	WakeRPC
	WakeAdmin
	WakeHeartbeat
	WakeGuestPSI
)

func (r WakeReason) String() string {
	switch r {
	case WakeNone:
		return "none"
	case WakeCPU:
		return "cpu"
	case WakeHostPressure:
		return "host_pressure"
	case WakeRPC:
		return "rpc"
	case WakeAdmin:
		return "admin"
	case WakeHeartbeat:
		return "heartbeat"
	case WakeGuestPSI:
		return "guest_psi"
	}
	return "unknown"
}

// ActivityWitness is one host-side observation about a VM. Carries no
// guest data on principle — the witness path must never be capable of
// waking the guest.
type ActivityWitness struct {
	Now        time.Time
	CPUPercent float64 // 0..100, host-side delta
	HostTier   Tier
	VMUptime   time.Duration
}

// FreezeConfig holds per-VM freeze/wake hysteresis parameters.
type FreezeConfig struct {
	Enabled              bool
	CPUEnter             float64       // cpu% below which idle streak accumulates (default 1.0)
	CPUExit              float64       // cpu% at or above which a frozen VM wakes (default 2.0)
	IdleWindow           time.Duration // consecutive idle time before freeze (default 10m)
	MinUptime            time.Duration // VM must be up this long before freeze (default 5m)
	RequireGuestPSIBelow float64       // guest PSI full avg60 must be below this to freeze (default 1.0)
	GuestPSIWake         float64       // guest PSI full avg60 at or above triggers wake (default 5.0)
}

// DefaultFreezeConfig is the production default. Freeze is disabled by
// default for one release of soak; set Enabled=true to activate.
var DefaultFreezeConfig = FreezeConfig{
	Enabled:              false,
	CPUEnter:             1.0,
	CPUExit:              2.0,
	IdleWindow:           10 * time.Minute,
	MinUptime:            5 * time.Minute,
	RequireGuestPSIBelow: 1.0,
	GuestPSIWake:         5.0,
}
