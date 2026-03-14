package execore

import (
	"math"
	"testing"
)

func closeTo(a, b, epsilon float64) bool {
	return math.Abs(a-b) < epsilon
}

// TestCreditBar_BonusUserSpentHalf verifies that a paying user who received the
// $100 upgrade bonus and has spent $50 of their $120 total sees the bar reflect
// actual usage. Before the fix the bar denominator tracked the numerator so the
// bar always showed ~100%.
func TestCreditBar_BonusUserSpentHalf(t *testing.T) {
	// User started with $120 (20 plan + 100 bonus), spent $50, has $70 left.
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 70,
		shelleyCreditsMax:       120, // plan.MaxCredit(20) + UpgradeBonusCreditUSD(100)
		extraCreditsUSD:         0,
	})

	// totalRemainingPct should be ~58.3%, NOT 100%.
	if closeTo(bar.totalRemainingPct, 100, 1) {
		t.Fatalf("totalRemainingPct = %.1f%%, should NOT be ~100%% (bug: denominator tracking numerator)", bar.totalRemainingPct)
	}
	wantPct := (70.0 / 120.0) * 100
	if !closeTo(bar.totalRemainingPct, wantPct, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want %.2f", bar.totalRemainingPct, wantPct)
	}

	// usedCreditsUSD should be $50, NOT $0.
	if closeTo(bar.usedCreditsUSD, 0, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, should NOT be $0 (bug: capacity == available)", bar.usedCreditsUSD)
	}
	if !closeTo(bar.usedCreditsUSD, 50, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 50.00", bar.usedCreditsUSD)
	}

	// totalCapacity should be fixed at 120, not shrinking with available.
	if !closeTo(bar.totalCapacity, 120, 0.01) {
		t.Fatalf("totalCapacity = %.2f, want 120.00", bar.totalCapacity)
	}
}

// TestCreditBar_BonusExhaustedNextMonth verifies that after the bonus is fully
// consumed and the user gets their normal $20 monthly top-up, the bar shows
// 100% (20/20).
func TestCreditBar_BonusExhaustedNextMonth(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 20,
		shelleyCreditsMax:       20, // no bonus anymore
		extraCreditsUSD:         0,
	})

	if !closeTo(bar.totalRemainingPct, 100, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want 100", bar.totalRemainingPct)
	}
	if !closeTo(bar.usedCreditsUSD, 0, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 0", bar.usedCreditsUSD)
	}
}

// TestCreditBar_BonusUserNoSpend verifies a fresh bonus user with $120
// available and $120 capacity shows 100%.
func TestCreditBar_BonusUserNoSpend(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 120,
		shelleyCreditsMax:       120,
		extraCreditsUSD:         0,
	})

	if !closeTo(bar.totalRemainingPct, 100, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want 100", bar.totalRemainingPct)
	}
	if !closeTo(bar.usedCreditsUSD, 0, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 0", bar.usedCreditsUSD)
	}
}

// TestCreditBar_BonusUserWithExtraCredits verifies the stacked bar when a bonus
// user also has purchased extra credits.
func TestCreditBar_BonusUserWithExtraCredits(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 70,
		shelleyCreditsMax:       120,
		extraCreditsUSD:         30,
	})

	// totalCapacity = 120 + 30 = 150
	if !closeTo(bar.totalCapacity, 150, 0.01) {
		t.Fatalf("totalCapacity = %.2f, want 150", bar.totalCapacity)
	}
	// totalRemainingPct = (70+30)/150 * 100 = 66.67%
	wantPct := (100.0 / 150.0) * 100
	if !closeTo(bar.totalRemainingPct, wantPct, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want %.2f", bar.totalRemainingPct, wantPct)
	}
	// monthlyBarPct = 70/150 * 100 = 46.67%
	wantMonthly := (70.0 / 150.0) * 100
	if !closeTo(bar.monthlyBarPct, wantMonthly, 0.1) {
		t.Fatalf("monthlyBarPct = %.2f, want %.2f", bar.monthlyBarPct, wantMonthly)
	}
	// extraBarPct = 30/150 * 100 = 20%
	wantExtra := (30.0 / 150.0) * 100
	if !closeTo(bar.extraBarPct, wantExtra, 0.1) {
		t.Fatalf("extraBarPct = %.2f, want %.2f", bar.extraBarPct, wantExtra)
	}
}

// TestCreditBar_CapacityNotInflatedByAvailable verifies that when available
// exceeds max (e.g. because the caller forgot to include the bonus in max),
// the bar still uses the stated max as capacity — it does NOT inflate capacity
// to match available. This is the core invariant that the old buggy code
// violated: it had `if available > capacity { capacity = available }` which
// made the denominator track the numerator, producing ~100% always.
func TestCreditBar_CapacityNotInflatedByAvailable(t *testing.T) {
	// Simulate the old bug scenario: caller passes max=20 but available=70
	// (bonus user who spent $50). With the old dynamic adjustment this would
	// produce capacity=70, pct=100%, used=0 — all wrong.
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 70,
		shelleyCreditsMax:       20, // deliberately wrong — simulates old handler bug
		extraCreditsUSD:         0,
	})

	// The bar should use 20 as capacity, NOT inflate to 70.
	if !closeTo(bar.totalCapacity, 20, 0.01) {
		t.Fatalf("totalCapacity = %.2f, want 20 (must not inflate to match available)", bar.totalCapacity)
	}
	// With capacity=20 and available=70, remaining is clamped to 100%.
	if !closeTo(bar.totalRemainingPct, 100, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want 100 (clamped)", bar.totalRemainingPct)
	}
	// usedCreditsUSD should be 0 (can't be negative — available > capacity).
	if !closeTo(bar.usedCreditsUSD, 0, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 0", bar.usedCreditsUSD)
	}
}

// TestCreditBar_AllCreditsUsed verifies that a user who has spent all $20 of
// their monthly credit sees 0% remaining and $20 used.
func TestCreditBar_AllCreditsUsed(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 0,
		shelleyCreditsMax:       20,
		extraCreditsUSD:         0,
	})

	if !closeTo(bar.totalRemainingPct, 0, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want 0", bar.totalRemainingPct)
	}
	if !closeTo(bar.usedCreditsUSD, 20, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 20.00", bar.usedCreditsUSD)
	}
	if !closeTo(bar.usedBarPct, 100, 0.1) {
		t.Fatalf("usedBarPct = %.2f, want 100", bar.usedBarPct)
	}
	if !closeTo(bar.monthlyBarPct, 0, 0.1) {
		t.Fatalf("monthlyBarPct = %.2f, want 0", bar.monthlyBarPct)
	}
}

// TestCreditBar_FreeUserNoBonus verifies a free-tier user with $20 credit and
// no bonus sees correct percentages.
func TestCreditBar_FreeUserNoBonus(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 15,
		shelleyCreditsMax:       20,
		extraCreditsUSD:         0,
	})

	wantPct := (15.0 / 20.0) * 100
	if !closeTo(bar.totalRemainingPct, wantPct, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want %.2f", bar.totalRemainingPct, wantPct)
	}
	if !closeTo(bar.usedCreditsUSD, 5, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 5.00", bar.usedCreditsUSD)
	}
}
