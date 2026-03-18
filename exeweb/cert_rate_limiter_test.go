package exeweb

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestCertRateLimiterAllow(t *testing.T) {
	t.Parallel()
	rl := NewCertRateLimiter(3)
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	rl.nowFunc = func() time.Time { return now }

	// First 3 calls should succeed (full bucket).
	for i := range 3 {
		if err := rl.Allow("vm1"); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
	}

	// 4th call should be rate-limited.
	if err := rl.Allow("vm1"); err == nil {
		t.Fatal("expected rate limit error, got nil")
	}

	// Different VM should have its own bucket.
	if err := rl.Allow("vm2"); err != nil {
		t.Fatalf("vm2 should be allowed: %v", err)
	}
}

func TestCertRateLimiterTokenRefill(t *testing.T) {
	t.Parallel()
	rl := NewCertRateLimiter(3)
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	rl.nowFunc = func() time.Time { return now }

	// Drain all tokens.
	for range 3 {
		rl.Allow("vm1")
	}
	if err := rl.Allow("vm1"); err == nil {
		t.Fatal("expected rate limit")
	}

	// Advance time by 8 hours (1/3 of 24h), which should refill 1 token.
	now = now.Add(8 * time.Hour)
	if err := rl.Allow("vm1"); err != nil {
		t.Fatalf("expected allow after refill: %v", err)
	}
	// Should be rate-limited again.
	if err := rl.Allow("vm1"); err == nil {
		t.Fatal("expected rate limit after consuming refilled token")
	}
}

func TestCertRateLimiterTokenCap(t *testing.T) {
	t.Parallel()
	rl := NewCertRateLimiter(3)
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	rl.nowFunc = func() time.Time { return now }

	// Use one token.
	rl.Allow("vm1")

	// Advance 48h — tokens should cap at limit (3), not exceed it.
	now = now.Add(48 * time.Hour)

	for i := range 3 {
		if err := rl.Allow("vm1"); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
	}
	if err := rl.Allow("vm1"); err == nil {
		t.Fatal("expected rate limit at cap")
	}
}

func TestCertRateLimiterZeroLimit(t *testing.T) {
	t.Parallel()
	rl := NewCertRateLimiter(0)
	if err := rl.Allow("vm1"); err == nil {
		t.Fatal("expected error for zero limit")
	}
}

func TestCertRateLimiterCleanup(t *testing.T) {
	t.Parallel()
	rl := NewCertRateLimiter(3)
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	rl.nowFunc = func() time.Time { return now }

	// Use tokens for two VMs.
	rl.Allow("vm1")
	rl.Allow("vm2")
	rl.Allow("vm2")
	rl.Allow("vm2")

	// Advance 24 hours — vm1 should be fully refilled, vm2 should not.
	now = now.Add(24 * time.Hour)
	rl.Cleanup()

	rl.mu.Lock()
	defer rl.mu.Unlock()
	if _, ok := rl.buckets["vm1"]; ok {
		t.Error("vm1 bucket should have been cleaned up")
	}
	// vm2 used 3 tokens; 24h refills exactly 3, so it should also be cleaned up.
	if _, ok := rl.buckets["vm2"]; ok {
		t.Error("vm2 bucket should have been cleaned up (fully refilled after 24h)")
	}
}

func TestCertRateLimiterMetrics(t *testing.T) {
	t.Parallel()
	rl := NewCertRateLimiter(1)
	reg := prometheus.NewRegistry()
	rl.RegisterMetrics(reg)

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	rl.nowFunc = func() time.Time { return now }

	// One allowed, one rejected.
	rl.Allow("vm1")
	rl.Allow("vm1")

	metrics, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	found := map[string]float64{}
	for _, mf := range metrics {
		for _, m := range mf.GetMetric() {
			found[mf.GetName()] = m.GetCounter().GetValue()
		}
	}

	if v := found["cert_ratelimit_allowed_total"]; v != 1 {
		t.Errorf("allowed_total = %v, want 1", v)
	}
	if v := found["cert_ratelimit_rejected_total"]; v != 1 {
		t.Errorf("rejected_total = %v, want 1", v)
	}
}
