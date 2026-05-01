package guestmetrics

import (
	"testing"
	"time"
)

func TestVMTierString(t *testing.T) {
	cases := []struct {
		tier VMTier
		want string
	}{
		{VMTierActive, "active"},
		{VMTierFrozen, "frozen"},
		{VMTier(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.tier.String(); got != c.want {
			t.Errorf("VMTier(%d).String() = %q, want %q", c.tier, got, c.want)
		}
	}
}

func TestWakeReasonString(t *testing.T) {
	cases := []struct {
		r    WakeReason
		want string
	}{
		{WakeNone, "none"},
		{WakeCPU, "cpu"},
		{WakeHostPressure, "host_pressure"},
		{WakeRPC, "rpc"},
		{WakeAdmin, "admin"},
		{WakeHeartbeat, "heartbeat"},
		{WakeGuestPSI, "guest_psi"},
		{WakeReason(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.r.String(); got != c.want {
			t.Errorf("WakeReason(%d).String() = %q, want %q", c.r, got, c.want)
		}
	}
}

func TestDefaultFreezeConfig(t *testing.T) {
	cfg := DefaultFreezeConfig
	if cfg.Enabled {
		t.Error("DefaultFreezeConfig.Enabled should be false")
	}
	if cfg.CPUEnter != 1.0 {
		t.Errorf("CPUEnter = %v, want 1.0", cfg.CPUEnter)
	}
	if cfg.CPUExit != 2.0 {
		t.Errorf("CPUExit = %v, want 2.0", cfg.CPUExit)
	}
	if cfg.IdleWindow != 10*time.Minute {
		t.Errorf("IdleWindow = %v, want 10m", cfg.IdleWindow)
	}
	if cfg.MinUptime != 5*time.Minute {
		t.Errorf("MinUptime = %v, want 5m", cfg.MinUptime)
	}
	if cfg.RequireGuestPSIBelow != 1.0 {
		t.Errorf("RequireGuestPSIBelow = %v, want 1.0", cfg.RequireGuestPSIBelow)
	}
	if cfg.GuestPSIWake != 5.0 {
		t.Errorf("GuestPSIWake = %v, want 5.0", cfg.GuestPSIWake)
	}
}
