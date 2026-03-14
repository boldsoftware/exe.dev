package execore

import (
	"math"
	"testing"
)

func closeTo(a, b, epsilon float64) bool {
	return math.Abs(a-b) < epsilon
}

// TestCreditBar_BonusUserSpentHalf verifies that a paying user who received the
// $100 upgrade bonus and has spent $50 of their $120 total sees the bonus and
// monthly portions split correctly. With the smooth denominator the bar stays
// full while bonus credit remains, but the dollar amounts reflect real usage.
func TestCreditBar_BonusUserSpentHalf(t *testing.T) {
	// User started with $120 (20 plan + 100 bonus), spent $50, has $70 left.
	// The handler computes bonusRemaining = 70 - 20 = 50.
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 70,
		planMaxCredit:           20,
		bonusRemaining:          50,
		extraCreditsUSD:         0,
	})

	// Capacity smooths: 20 + 50 = 70 (not fixed at 120).
	if !closeTo(bar.totalCapacity, 70, 0.01) {
		t.Fatalf("totalCapacity = %.2f, want 70", bar.totalCapacity)
	}
	// Bar is full — all credit accounted for.
	if !closeTo(bar.totalRemainingPct, 100, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want 100", bar.totalRemainingPct)
	}
	// Monthly portion is full ($20/$20), no monthly usage shown.
	if !closeTo(bar.monthlyAvailable, 20, 0.01) {
		t.Fatalf("monthlyAvailable = %.2f, want 20", bar.monthlyAvailable)
	}
	if !closeTo(bar.usedCreditsUSD, 0, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 0", bar.usedCreditsUSD)
	}
	// Bonus is visible as a separate segment.
	if !closeTo(bar.bonusRemaining, 50, 0.01) {
		t.Fatalf("bonusRemaining = %.2f, want 50", bar.bonusRemaining)
	}
	// monthlyBarPct = 20/70 ≈ 28.57%
	if !closeTo(bar.monthlyBarPct, (20.0/70.0)*100, 0.1) {
		t.Fatalf("monthlyBarPct = %.2f, want %.2f", bar.monthlyBarPct, (20.0/70.0)*100)
	}
	// bonusBarPct = 50/70 ≈ 71.43%
	if !closeTo(bar.bonusBarPct, (50.0/70.0)*100, 0.1) {
		t.Fatalf("bonusBarPct = %.2f, want %.2f", bar.bonusBarPct, (50.0/70.0)*100)
	}
}

// TestCreditBar_BonusExhaustedNextMonth verifies that after the bonus is fully
// consumed and the user gets their normal $20 monthly top-up, the bar shows
// 100% (20/20) with no bonus segment.
func TestCreditBar_BonusExhaustedNextMonth(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 20,
		planMaxCredit:           20,
		bonusRemaining:          0,
		extraCreditsUSD:         0,
	})

	if !closeTo(bar.totalRemainingPct, 100, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want 100", bar.totalRemainingPct)
	}
	if !closeTo(bar.usedCreditsUSD, 0, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 0", bar.usedCreditsUSD)
	}
	if !closeTo(bar.bonusRemaining, 0, 0.01) {
		t.Fatalf("bonusRemaining = %.2f, want 0", bar.bonusRemaining)
	}
	if !closeTo(bar.bonusBarPct, 0, 0.01) {
		t.Fatalf("bonusBarPct = %.2f, want 0", bar.bonusBarPct)
	}
}

// TestCreditBar_BonusUserNoSpend verifies a fresh bonus user with $120
// available sees 100% with bonus and monthly segments.
func TestCreditBar_BonusUserNoSpend(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 120,
		planMaxCredit:           20,
		bonusRemaining:          100,
		extraCreditsUSD:         0,
	})

	if !closeTo(bar.totalCapacity, 120, 0.01) {
		t.Fatalf("totalCapacity = %.2f, want 120", bar.totalCapacity)
	}
	if !closeTo(bar.totalRemainingPct, 100, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want 100", bar.totalRemainingPct)
	}
	if !closeTo(bar.monthlyAvailable, 20, 0.01) {
		t.Fatalf("monthlyAvailable = %.2f, want 20", bar.monthlyAvailable)
	}
	if !closeTo(bar.bonusRemaining, 100, 0.01) {
		t.Fatalf("bonusRemaining = %.2f, want 100", bar.bonusRemaining)
	}
	if !closeTo(bar.usedCreditsUSD, 0, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 0", bar.usedCreditsUSD)
	}
}

// TestCreditBar_BonusUserWithExtraCredits verifies the stacked bar when a bonus
// user also has purchased extra credits. Three segments should be visible.
func TestCreditBar_BonusUserWithExtraCredits(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 70,
		planMaxCredit:           20,
		bonusRemaining:          50,
		extraCreditsUSD:         30,
	})

	// totalCapacity = 20 + 50 + 30 = 100
	if !closeTo(bar.totalCapacity, 100, 0.01) {
		t.Fatalf("totalCapacity = %.2f, want 100", bar.totalCapacity)
	}
	// totalRemainingPct = (20+50+30)/100 = 100%
	if !closeTo(bar.totalRemainingPct, 100, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want 100", bar.totalRemainingPct)
	}
	// monthlyBarPct = 20/100 = 20%
	if !closeTo(bar.monthlyBarPct, 20, 0.1) {
		t.Fatalf("monthlyBarPct = %.2f, want 20", bar.monthlyBarPct)
	}
	// bonusBarPct = 50/100 = 50%
	if !closeTo(bar.bonusBarPct, 50, 0.1) {
		t.Fatalf("bonusBarPct = %.2f, want 50", bar.bonusBarPct)
	}
	// extraBarPct = 30/100 = 30%
	if !closeTo(bar.extraBarPct, 30, 0.1) {
		t.Fatalf("extraBarPct = %.2f, want 30", bar.extraBarPct)
	}
}

// TestCreditBar_CapacityNotInflatedByAvailable verifies that when available
// exceeds planMax but no bonus is present (bonusRemaining=0), the bar uses
// planMax as capacity and clamps remaining to 100%.
func TestCreditBar_CapacityNotInflatedByAvailable(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 70,
		planMaxCredit:           20,
		bonusRemaining:          0,
		extraCreditsUSD:         0,
	})

	// Capacity stays at planMax — no inflation.
	if !closeTo(bar.totalCapacity, 20, 0.01) {
		t.Fatalf("totalCapacity = %.2f, want 20 (must not inflate to match available)", bar.totalCapacity)
	}
	if !closeTo(bar.totalRemainingPct, 100, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want 100 (clamped)", bar.totalRemainingPct)
	}
	if !closeTo(bar.usedCreditsUSD, 0, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 0", bar.usedCreditsUSD)
	}
}

// TestCreditBar_AllCreditsUsed verifies that a user who has spent all $20 of
// their monthly credit sees 0% remaining and $20 used.
func TestCreditBar_AllCreditsUsed(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 0,
		planMaxCredit:           20,
		bonusRemaining:          0,
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

// TestCreditBar_FreeUserNoBonus verifies a free-tier user with $15 of $20
// credit remaining sees correct percentages.
func TestCreditBar_FreeUserNoBonus(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 15,
		planMaxCredit:           20,
		bonusRemaining:          0,
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

// TestCreditBar_BonusDrainsToZero walks through the full lifecycle: bonus
// drains smoothly, then monthly credit starts showing usage. No cliff.
func TestCreditBar_BonusDrainsToZero(t *testing.T) {
	cases := []struct {
		name          string
		available     float64
		bonus         float64
		wantCapacity  float64
		wantMonthly   float64
		wantBonus     float64
		wantUsed      float64
		wantRemainPct float64
	}{
		{
			name: "full bonus", available: 120, bonus: 100,
			wantCapacity: 120, wantMonthly: 20, wantBonus: 100, wantUsed: 0, wantRemainPct: 100,
		},
		{
			name: "half bonus", available: 70, bonus: 50,
			wantCapacity: 70, wantMonthly: 20, wantBonus: 50, wantUsed: 0, wantRemainPct: 100,
		},
		{
			name: "tiny bonus left", available: 21, bonus: 1,
			wantCapacity: 21, wantMonthly: 20, wantBonus: 1, wantUsed: 0, wantRemainPct: 100,
		},
		{
			name: "bonus just exhausted", available: 20, bonus: 0,
			wantCapacity: 20, wantMonthly: 20, wantBonus: 0, wantUsed: 0, wantRemainPct: 100,
		},
		{
			name: "monthly half used", available: 10, bonus: 0,
			wantCapacity: 20, wantMonthly: 10, wantBonus: 0, wantUsed: 10, wantRemainPct: 50,
		},
		{
			name: "monthly fully used", available: 0, bonus: 0,
			wantCapacity: 20, wantMonthly: 0, wantBonus: 0, wantUsed: 20, wantRemainPct: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bar := computeCreditBar(creditBarInput{
				shelleyCreditsAvailable: tc.available,
				planMaxCredit:           20,
				bonusRemaining:          tc.bonus,
				extraCreditsUSD:         0,
			})
			if !closeTo(bar.totalCapacity, tc.wantCapacity, 0.01) {
				t.Errorf("totalCapacity = %.2f, want %.2f", bar.totalCapacity, tc.wantCapacity)
			}
			if !closeTo(bar.monthlyAvailable, tc.wantMonthly, 0.01) {
				t.Errorf("monthlyAvailable = %.2f, want %.2f", bar.monthlyAvailable, tc.wantMonthly)
			}
			if !closeTo(bar.bonusRemaining, tc.wantBonus, 0.01) {
				t.Errorf("bonusRemaining = %.2f, want %.2f", bar.bonusRemaining, tc.wantBonus)
			}
			if !closeTo(bar.usedCreditsUSD, tc.wantUsed, 0.01) {
				t.Errorf("usedCreditsUSD = %.2f, want %.2f", bar.usedCreditsUSD, tc.wantUsed)
			}
			if !closeTo(bar.totalRemainingPct, tc.wantRemainPct, 0.1) {
				t.Errorf("totalRemainingPct = %.2f, want %.2f", bar.totalRemainingPct, tc.wantRemainPct)
			}
		})
	}
}
