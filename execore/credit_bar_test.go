package execore

import (
	"html/template"
	"math"
	"strings"
	"testing"
)

func closeTo(a, b, epsilon float64) bool {
	return math.Abs(a-b) < epsilon
}

// TestCreditBar_BonusUserSpentHalf verifies a paying user who received the $100
// bonus and spent $50 sees a unified bar that depletes against the full capacity.
func TestCreditBar_BonusUserSpentHalf(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 70,
		planMaxCredit:           20,
		bonusRemaining:          50,
		bonusGrantAmount:        100,
		extraCreditsUSD:         0,
	})

	// Capacity = 20 + 100 = 120
	if !closeTo(bar.totalCapacity, 120, 0.01) {
		t.Fatalf("totalCapacity = %.2f, want 120", bar.totalCapacity)
	}
	// 70 remaining of 120 ≈ 58.3%
	if !closeTo(bar.totalRemainingPct, (70.0/120.0)*100, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want %.2f", bar.totalRemainingPct, (70.0/120.0)*100)
	}
	if !closeTo(bar.usedCreditsUSD, 50, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 50", bar.usedCreditsUSD)
	}
	if !closeTo(bar.monthlyAvailable, 20, 0.01) {
		t.Fatalf("monthlyAvailable = %.2f, want 20", bar.monthlyAvailable)
	}
	if !closeTo(bar.bonusRemaining, 50, 0.01) {
		t.Fatalf("bonusRemaining = %.2f, want 50", bar.bonusRemaining)
	}
}

// TestCreditBar_BonusExhausted verifies that after the bonus is fully consumed,
// bar capacity still includes the grant and usage is reflected.
func TestCreditBar_BonusExhausted(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 20,
		planMaxCredit:           20,
		bonusRemaining:          0,
		bonusGrantAmount:        100,
		extraCreditsUSD:         0,
	})

	// Capacity stays at 120 (planMax + grant).
	if !closeTo(bar.totalCapacity, 120, 0.01) {
		t.Fatalf("totalCapacity = %.2f, want 120", bar.totalCapacity)
	}
	// 20/120 ≈ 16.67%
	if !closeTo(bar.totalRemainingPct, (20.0/120.0)*100, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want %.2f", bar.totalRemainingPct, (20.0/120.0)*100)
	}
	if !closeTo(bar.usedCreditsUSD, 100, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 100", bar.usedCreditsUSD)
	}
}

// TestCreditBar_BonusUserNoSpend verifies a fresh bonus user sees 100% bar.
func TestCreditBar_BonusUserNoSpend(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 120,
		planMaxCredit:           20,
		bonusRemaining:          100,
		bonusGrantAmount:        100,
		extraCreditsUSD:         0,
	})

	if !closeTo(bar.totalCapacity, 120, 0.01) {
		t.Fatalf("totalCapacity = %.2f, want 120", bar.totalCapacity)
	}
	if !closeTo(bar.totalRemainingPct, 100, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want 100", bar.totalRemainingPct)
	}
	if !closeTo(bar.usedCreditsUSD, 0, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 0", bar.usedCreditsUSD)
	}
}

// TestCreditBar_BonusUserWithExtraCredits verifies capacity includes all pools.
func TestCreditBar_BonusUserWithExtraCredits(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 70,
		planMaxCredit:           20,
		bonusRemaining:          50,
		bonusGrantAmount:        100,
		extraCreditsUSD:         30,
	})

	// totalCapacity = 20 + 100 + 30 = 150
	if !closeTo(bar.totalCapacity, 150, 0.01) {
		t.Fatalf("totalCapacity = %.2f, want 150", bar.totalCapacity)
	}
	// remaining = 20 + 50 + 30 = 100
	if !closeTo(bar.totalRemainingPct, (100.0/150.0)*100, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want %.2f", bar.totalRemainingPct, (100.0/150.0)*100)
	}
	if !closeTo(bar.usedCreditsUSD, 50, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 50", bar.usedCreditsUSD)
	}
}

// TestCreditBar_CapacityNotInflatedByAvailable verifies that when available
// exceeds planMax but no bonus is present, capacity stays at planMax.
func TestCreditBar_CapacityNotInflatedByAvailable(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 70,
		planMaxCredit:           20,
		bonusRemaining:          0,
		bonusGrantAmount:        0,
		extraCreditsUSD:         0,
	})

	if !closeTo(bar.totalCapacity, 20, 0.01) {
		t.Fatalf("totalCapacity = %.2f, want 20", bar.totalCapacity)
	}
	if !closeTo(bar.totalRemainingPct, 100, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want 100 (clamped)", bar.totalRemainingPct)
	}
	if !closeTo(bar.usedCreditsUSD, 0, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 0", bar.usedCreditsUSD)
	}
}

// TestCreditBar_AllCreditsUsed verifies a user who has spent all monthly credit.
func TestCreditBar_AllCreditsUsed(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 0,
		planMaxCredit:           20,
		bonusRemaining:          0,
		bonusGrantAmount:        0,
		extraCreditsUSD:         0,
	})

	if !closeTo(bar.totalRemainingPct, 0, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want 0", bar.totalRemainingPct)
	}
	if !closeTo(bar.usedCreditsUSD, 20, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 20", bar.usedCreditsUSD)
	}
	if !closeTo(bar.usedBarPct, 100, 0.1) {
		t.Fatalf("usedBarPct = %.2f, want 100", bar.usedBarPct)
	}
}

// TestCreditBar_FreeUserNoBonus verifies a free-tier user with partial usage.
func TestCreditBar_FreeUserNoBonus(t *testing.T) {
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 15,
		planMaxCredit:           20,
		bonusRemaining:          0,
		bonusGrantAmount:        0,
		extraCreditsUSD:         0,
	})

	wantPct := (15.0 / 20.0) * 100
	if !closeTo(bar.totalRemainingPct, wantPct, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want %.2f", bar.totalRemainingPct, wantPct)
	}
	if !closeTo(bar.usedCreditsUSD, 5, 0.01) {
		t.Fatalf("usedCreditsUSD = %.2f, want 5", bar.usedCreditsUSD)
	}
}

// TestCreditBar_RegularMode verifies normal (non-bonus) scenarios.
func TestCreditBar_RegularMode(t *testing.T) {
	cases := []struct {
		name          string
		available     float64
		planMax       float64
		extra         float64
		wantCapacity  float64
		wantRemainPct float64
		wantUsed      float64
		wantUsedPct   float64
	}{
		{
			name:      "full monthly no extra",
			available: 20, planMax: 20, extra: 0,
			wantCapacity: 20, wantRemainPct: 100, wantUsed: 0, wantUsedPct: 0,
		},
		{
			name:      "half monthly no extra",
			available: 10, planMax: 20, extra: 0,
			wantCapacity: 20, wantRemainPct: 50, wantUsed: 10, wantUsedPct: 50,
		},
		{
			name:      "empty monthly no extra",
			available: 0, planMax: 20, extra: 0,
			wantCapacity: 20, wantRemainPct: 0, wantUsed: 20, wantUsedPct: 100,
		},
		{
			name:      "full monthly with extra",
			available: 20, planMax: 20, extra: 50,
			wantCapacity: 70, wantRemainPct: 100, wantUsed: 0, wantUsedPct: 0,
		},
		{
			name:      "half monthly with extra",
			available: 10, planMax: 20, extra: 50,
			wantCapacity: 70, wantRemainPct: (60.0 / 70.0) * 100, wantUsed: 10, wantUsedPct: (10.0 / 70.0) * 100,
		},
		{
			name:      "quarter monthly no extra",
			available: 5, planMax: 20, extra: 0,
			wantCapacity: 20, wantRemainPct: 25, wantUsed: 15, wantUsedPct: 75,
		},
		{
			name:      "nearly empty monthly no extra",
			available: 1, planMax: 20, extra: 0,
			wantCapacity: 20, wantRemainPct: 5, wantUsed: 19, wantUsedPct: 95,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bar := computeCreditBar(creditBarInput{
				shelleyCreditsAvailable: tc.available,
				planMaxCredit:           tc.planMax,
				bonusRemaining:          0,
				bonusGrantAmount:        0,
				extraCreditsUSD:         tc.extra,
			})
			if !closeTo(bar.totalCapacity, tc.wantCapacity, 0.01) {
				t.Errorf("totalCapacity = %.2f, want %.2f", bar.totalCapacity, tc.wantCapacity)
			}
			if !closeTo(bar.totalRemainingPct, tc.wantRemainPct, 0.1) {
				t.Errorf("totalRemainingPct = %.2f, want %.2f", bar.totalRemainingPct, tc.wantRemainPct)
			}
			if !closeTo(bar.usedCreditsUSD, tc.wantUsed, 0.01) {
				t.Errorf("usedCreditsUSD = %.2f, want %.2f", bar.usedCreditsUSD, tc.wantUsed)
			}
			if !closeTo(bar.usedBarPct, tc.wantUsedPct, 0.1) {
				t.Errorf("usedBarPct = %.2f, want %.2f", bar.usedBarPct, tc.wantUsedPct)
			}
		})
	}
}

// TestCreditBar_BonusDrainsToZero walks through the full lifecycle.
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
			wantCapacity: 120, wantMonthly: 20, wantBonus: 50, wantUsed: 50, wantRemainPct: (70.0 / 120.0) * 100,
		},
		{
			name: "tiny bonus left", available: 21, bonus: 1,
			wantCapacity: 120, wantMonthly: 20, wantBonus: 1, wantUsed: 99, wantRemainPct: (21.0 / 120.0) * 100,
		},
		{
			name: "bonus just exhausted", available: 20, bonus: 0,
			wantCapacity: 120, wantMonthly: 20, wantBonus: 0, wantUsed: 100, wantRemainPct: (20.0 / 120.0) * 100,
		},
		{
			name: "monthly half used", available: 10, bonus: 0,
			wantCapacity: 120, wantMonthly: 10, wantBonus: 0, wantUsed: 110, wantRemainPct: (10.0 / 120.0) * 100,
		},
		{
			name: "monthly fully used", available: 0, bonus: 0,
			wantCapacity: 120, wantMonthly: 0, wantBonus: 0, wantUsed: 120, wantRemainPct: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bar := computeCreditBar(creditBarInput{
				shelleyCreditsAvailable: tc.available,
				planMaxCredit:           20,
				bonusRemaining:          tc.bonus,
				bonusGrantAmount:        100,
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

// TestCreditBar_TemplateLabels verifies the template renders correct labels.
func TestCreditBar_TemplateLabels(t *testing.T) {
	const tmpl = `<span class="credit-hero-number">{{printf "%.0f" .TotalCreditsUSD}}</span>` +
		`<span class="credit-hero-label">credits remaining</span>` +
		`<span class="used">{{printf "%.0f" .UsedCreditsUSD}} used</span>` +
		`<span class="total">{{printf "%.0f" .TotalCapacityUSD}} total</span>`

	type data struct {
		TotalCreditsUSD  float64
		UsedCreditsUSD   float64
		TotalCapacityUSD float64
	}

	cases := []struct {
		name        string
		data        data
		wantContain []string
	}{
		{
			name: "bonus user with usage",
			data: data{TotalCreditsUSD: 70, UsedCreditsUSD: 50, TotalCapacityUSD: 120},
			wantContain: []string{
				`>70</span>`,
				`credits remaining`,
				`50 used`,
				`120 total`,
			},
		},
		{
			name: "free user partial usage",
			data: data{TotalCreditsUSD: 15, UsedCreditsUSD: 5, TotalCapacityUSD: 20},
			wantContain: []string{
				`>15</span>`,
				`credits remaining`,
				`5 used`,
				`20 total`,
			},
		},
		{
			name: "all used",
			data: data{TotalCreditsUSD: 0, UsedCreditsUSD: 20, TotalCapacityUSD: 20},
			wantContain: []string{
				`>0</span>`,
				`20 used`,
				`20 total`,
			},
		},
	}

	parsed := template.Must(template.New("bar").Parse(tmpl))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf strings.Builder
			if err := parsed.Execute(&buf, tc.data); err != nil {
				t.Fatal(err)
			}
			html := buf.String()
			for _, want := range tc.wantContain {
				if !strings.Contains(html, want) {
					t.Errorf("output should contain %q\ngot: %s", want, html)
				}
			}
		})
	}
}

func TestGiftsForUser(t *testing.T) {
	t.Run("no bonus grant returns nil", func(t *testing.T) {
		gifts := giftsForUser(0)
		if gifts != nil {
			t.Fatalf("expected nil, got %v", gifts)
		}
	})

	t.Run("bonus grant returns gift row", func(t *testing.T) {
		gifts := giftsForUser(100)
		if len(gifts) != 1 {
			t.Fatalf("expected 1 gift, got %d", len(gifts))
		}
		if gifts[0].Amount != "100" {
			t.Errorf("amount = %q, want 100", gifts[0].Amount)
		}
		if gifts[0].Reason == "" {
			t.Error("reason should not be empty")
		}
	})

	t.Run("negative grant returns nil", func(t *testing.T) {
		gifts := giftsForUser(-5)
		if gifts != nil {
			t.Fatalf("expected nil, got %v", gifts)
		}
	})
}

func TestGiftsTemplateRendering(t *testing.T) {
	const tmpl = `{{if .Gifts}}` +
		`<table>` +
		`{{range .Gifts}}<tr><td>{{.Amount}}</td><td>{{.Reason}}</td></tr>{{end}}` +
		`</table>` +
		`{{end}}`

	parsed := template.Must(template.New("gifts").Parse(tmpl))

	t.Run("gifts table renders when gifts present", func(t *testing.T) {
		var buf strings.Builder
		data := struct{ Gifts []GiftRow }{
			Gifts: []GiftRow{{Amount: "100", Reason: "Welcome bonus for upgrading to a paid plan"}},
		}
		if err := parsed.Execute(&buf, data); err != nil {
			t.Fatal(err)
		}
		html := buf.String()
		if !strings.Contains(html, "<table>") {
			t.Error("expected table to render")
		}
		if !strings.Contains(html, "100") {
			t.Error("expected amount 100")
		}
		if !strings.Contains(html, "Welcome bonus") {
			t.Error("expected reason text")
		}
	})

	t.Run("gifts table hidden when no gifts", func(t *testing.T) {
		var buf strings.Builder
		data := struct{ Gifts []GiftRow }{}
		if err := parsed.Execute(&buf, data); err != nil {
			t.Fatal(err)
		}
		if buf.String() != "" {
			t.Errorf("expected empty output, got %q", buf.String())
		}
	})
}
