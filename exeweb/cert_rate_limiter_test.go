package exeweb

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestCertRateLimiter(t *testing.T) {
	t.Parallel()

	t.Run("allows up to limit", func(t *testing.T) {
		t.Parallel()
		rl := NewCertRateLimiter(3)
		reg := prometheus.NewRegistry()
		rl.RegisterMetrics(reg)
		for i := range 3 {
			if err := rl.Allow("mybox"); err != nil {
				t.Fatalf("attempt %d should be allowed: %v", i+1, err)
			}
		}
		if err := rl.Allow("mybox"); err == nil {
			t.Fatal("attempt 4 should be rejected")
		}
		// Verify metrics.
		if got := counterValue(t, reg, "cert_ratelimit_allowed_total", "mybox"); got != 3 {
			t.Fatalf("allowed_total: got %v, want 3", got)
		}
		if got := counterValue(t, reg, "cert_ratelimit_rejected_total", "mybox"); got != 1 {
			t.Fatalf("rejected_total: got %v, want 1", got)
		}
	})

	t.Run("independent per VM", func(t *testing.T) {
		t.Parallel()
		rl := NewCertRateLimiter(2)
		for range 2 {
			rl.Allow("box-a")
		}
		// box-a is exhausted
		if err := rl.Allow("box-a"); err == nil {
			t.Fatal("box-a should be rate limited")
		}
		// box-b should be fine
		if err := rl.Allow("box-b"); err != nil {
			t.Fatalf("box-b should be allowed: %v", err)
		}
	})

	t.Run("refills over time", func(t *testing.T) {
		t.Parallel()
		now := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
		rl := NewCertRateLimiter(2)
		rl.nowFunc = func() time.Time { return now }

		rl.Allow("mybox")
		rl.Allow("mybox")
		if err := rl.Allow("mybox"); err == nil {
			t.Fatal("should be rate limited")
		}

		// Advance half a day: should refill 1 token (rate is 2/day).
		now = now.Add(12 * time.Hour)
		if err := rl.Allow("mybox"); err != nil {
			t.Fatalf("should be allowed after half-day refill: %v", err)
		}
		if err := rl.Allow("mybox"); err == nil {
			t.Fatal("should be rate limited again after using refilled token")
		}

		// Advance a full day: should be fully refilled.
		now = now.Add(24 * time.Hour)
		for i := range 2 {
			if err := rl.Allow("mybox"); err != nil {
				t.Fatalf("attempt %d after full refill should be allowed: %v", i+1, err)
			}
		}
	})

	t.Run("tokens cap at limit", func(t *testing.T) {
		t.Parallel()
		now := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
		rl := NewCertRateLimiter(3)
		rl.nowFunc = func() time.Time { return now }

		// Use one token.
		rl.Allow("mybox")

		// Wait 10 days — more than enough to refill, but should cap at 3.
		now = now.Add(10 * 24 * time.Hour)
		for i := range 3 {
			if err := rl.Allow("mybox"); err != nil {
				t.Fatalf("attempt %d should be allowed: %v", i+1, err)
			}
		}
		if err := rl.Allow("mybox"); err == nil {
			t.Fatal("attempt 4 should be rejected (cap at limit)")
		}
	})

	t.Run("error message includes VM name", func(t *testing.T) {
		t.Parallel()
		rl := NewCertRateLimiter(1)
		rl.Allow("special-box")
		err := rl.Allow("special-box")
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "special-box") {
			t.Fatalf("error should mention VM name: %v", err)
		}
		if !strings.Contains(err.Error(), "rate limit") {
			t.Fatalf("error should mention rate limit: %v", err)
		}
	})

	t.Run("limit zero always denies", func(t *testing.T) {
		t.Parallel()
		rl := NewCertRateLimiter(0)
		if err := rl.Allow("anybox"); err == nil {
			t.Fatal("should be denied with limit=0")
		}
		if err := rl.Allow("anotherbox"); err == nil {
			t.Fatal("should be denied with limit=0")
		}
	})

	t.Run("cleanup removes idle entries", func(t *testing.T) {
		t.Parallel()
		now := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
		rl := NewCertRateLimiter(10)
		rl.nowFunc = func() time.Time { return now }

		rl.Allow("old-box")
		rl.Allow("active-box")

		// Advance more than a day so old-box would fully refill.
		now = now.Add(25 * time.Hour)
		// Touch active-box so it stays fresh.
		rl.Allow("active-box")

		rl.Cleanup()

		rl.mu.Lock()
		if _, ok := rl.buckets["old-box"]; ok {
			t.Fatal("old-box should have been cleaned up")
		}
		if _, ok := rl.buckets["active-box"]; !ok {
			t.Fatal("active-box should still exist")
		}
		rl.mu.Unlock()
	})
}

// counterValue reads the value of a counter metric with a specific vm label.
func counterValue(t *testing.T, reg *prometheus.Registry, name, vm string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "vm" && lp.GetValue() == vm {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}
