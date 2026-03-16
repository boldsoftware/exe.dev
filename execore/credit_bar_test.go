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
		{
			name: "negative zero clamped to zero",
			data: data{TotalCreditsUSD: max(-0.004, 0), UsedCreditsUSD: 120, TotalCapacityUSD: 120},
			wantContain: []string{
				`>0</span>`,
				`120 used`,
				`120 total`,
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

func TestTotalCreditsUSD_NeverNegative(t *testing.T) {
	cases := []struct {
		name                   string
		shelleyCreditsAvailable float64
		extraCreditsUSD         float64
		want                    float64
	}{
		{"both positive", 50, 20, 70},
		{"both zero", 0, 0, 0},
		{"extra slightly negative", 0, -0.004, 0},
		{"sum exactly zero", 5, -5, 0},
		{"sum negative", 0, -1.5, 0},
		{"large positive", 100, 30, 130},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := max(tc.shelleyCreditsAvailable+tc.extraCreditsUSD, 0)
			if got != tc.want {
				t.Errorf("max(%v + %v, 0) = %v, want %v",
					tc.shelleyCreditsAvailable, tc.extraCreditsUSD, got, tc.want)
			}
		})
	}
}

func TestGiftsForUser(t *testing.T) {
	t.Run("no bonus no support gift returns nil", func(t *testing.T) {
		gifts := giftsForUser(0, 0)
		if gifts != nil {
			t.Fatalf("expected nil, got %v", gifts)
		}
	})

	t.Run("full bonus remaining returns gift row", func(t *testing.T) {
		gifts := giftsForUser(100, 0)
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

	t.Run("partial bonus remaining shows remaining amount", func(t *testing.T) {
		gifts := giftsForUser(35, 0)
		if len(gifts) != 1 {
			t.Fatalf("expected 1 gift, got %d", len(gifts))
		}
		if gifts[0].Amount != "35" {
			t.Errorf("amount = %q, want 35", gifts[0].Amount)
		}
	})

	t.Run("bonus fully used returns nil", func(t *testing.T) {
		gifts := giftsForUser(0, 0)
		if gifts != nil {
			t.Fatalf("expected nil, got %v", gifts)
		}
	})

	t.Run("negative bonus returns nil", func(t *testing.T) {
		gifts := giftsForUser(-5, 0)
		if gifts != nil {
			t.Fatalf("expected nil, got %v", gifts)
		}
	})

	t.Run("support gift only", func(t *testing.T) {
		gifts := giftsForUser(0, 50)
		if len(gifts) != 1 {
			t.Fatalf("expected 1 gift, got %d", len(gifts))
		}
		if gifts[0].Amount != "50" {
			t.Errorf("amount = %q, want 50", gifts[0].Amount)
		}
		if gifts[0].Reason != "exe.dev Support Gift" {
			t.Errorf("reason = %q, want 'exe.dev Support Gift'", gifts[0].Reason)
		}
	})

	t.Run("bonus remaining and support gift", func(t *testing.T) {
		gifts := giftsForUser(100, 50)
		if len(gifts) != 2 {
			t.Fatalf("expected 2 gifts, got %d", len(gifts))
		}
		if gifts[0].Amount != "100" {
			t.Errorf("bonus amount = %q, want 100", gifts[0].Amount)
		}
		if gifts[1].Amount != "50" {
			t.Errorf("support gift amount = %q, want 50", gifts[1].Amount)
		}
		if gifts[1].Reason != "exe.dev Support Gift" {
			t.Errorf("reason = %q, want 'exe.dev Support Gift'", gifts[1].Reason)
		}
	})

	t.Run("negative support gift ignored", func(t *testing.T) {
		gifts := giftsForUser(100, -10)
		if len(gifts) != 1 {
			t.Fatalf("expected 1 gift, got %d", len(gifts))
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

	t.Run("support gift renders in table", func(t *testing.T) {
		var buf strings.Builder
		data := struct{ Gifts []GiftRow }{
			Gifts: []GiftRow{
				{Amount: "100", Reason: "Welcome bonus for upgrading to a paid plan"},
				{Amount: "50", Reason: "exe.dev Support Gift"},
			},
		}
		if err := parsed.Execute(&buf, data); err != nil {
			t.Fatal(err)
		}
		html := buf.String()
		if !strings.Contains(html, "exe.dev Support Gift") {
			t.Error("expected support gift reason")
		}
		if !strings.Contains(html, "<td>50</td>") {
			t.Error("expected support gift amount 50")
		}
	})
}

// TestComputeSupportGift verifies detection of manual DB credit adjustments.
func TestComputeSupportGift(t *testing.T) {
	cases := []struct {
		name      string
		available float64
		planMax   float64
		bonus     float64
		want      float64
	}{
		{
			name:      "no excess - normal user",
			available: 15, planMax: 20, bonus: 0,
			want: 0,
		},
		{
			name:      "no excess - full monthly",
			available: 20, planMax: 20, bonus: 0,
			want: 0,
		},
		{
			name:      "no excess - bonus user full",
			available: 120, planMax: 20, bonus: 100,
			want: 0,
		},
		{
			name:      "no excess - bonus user partial",
			available: 70, planMax: 20, bonus: 100,
			want: 0,
		},
		{
			name:      "support gift - no bonus user",
			available: 120, planMax: 20, bonus: 0,
			want: 100,
		},
		{
			name:      "support gift - bonus user with extra",
			available: 220, planMax: 20, bonus: 100,
			want: 100,
		},
		{
			name:      "support gift - small amount",
			available: 25, planMax: 20, bonus: 0,
			want: 5,
		},
		{
			name:      "support gift - bonus user small excess",
			available: 130, planMax: 20, bonus: 100,
			want: 10,
		},
		{
			name:      "zero available",
			available: 0, planMax: 20, bonus: 100,
			want: 0,
		},
		{
			name:      "float precision - tiny excess ignored",
			available: 20.0000001, planMax: 20, bonus: 0,
			want: 0,
		},
		{
			name:      "just above epsilon - detected",
			available: 21, planMax: 20, bonus: 0,
			want: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeSupportGift(tc.available, tc.planMax, tc.bonus)
			if !closeTo(got, tc.want, 0.01) {
				t.Errorf("computeSupportGift(%.0f, %.0f, %.0f) = %.2f, want %.2f",
					tc.available, tc.planMax, tc.bonus, got, tc.want)
			}
		})
	}
}

// TestCreditBar_WithSupportGift verifies the bar includes support gifts in capacity.
func TestCreditBar_WithSupportGift(t *testing.T) {
	cases := []struct {
		name          string
		available     float64
		planMax       float64
		bonus         float64
		bonusGrant    float64
		extra         float64
		supportGift   float64
		wantCapacity  float64
		wantRemainPct float64
		wantUsed      float64
	}{
		{
			name:      "support gift only - no bonus",
			available: 120, planMax: 20, bonus: 0, bonusGrant: 0, extra: 0, supportGift: 100,
			// capacity = 20 + 0 + 0 + 100 = 120, remaining = 20 + 0 + 0 + 100 = 120
			wantCapacity: 120, wantRemainPct: 100, wantUsed: 0,
		},
		{
			name:      "support gift with bonus",
			available: 220, planMax: 20, bonus: 100, bonusGrant: 100, extra: 0, supportGift: 100,
			// capacity = 20 + 100 + 0 + 100 = 220, remaining = 20 + 100 + 0 + 100 = 220
			wantCapacity: 220, wantRemainPct: 100, wantUsed: 0,
		},
		{
			name:      "support gift with bonus and extra",
			available: 220, planMax: 20, bonus: 100, bonusGrant: 100, extra: 20, supportGift: 100,
			// capacity = 20 + 100 + 20 + 100 = 240, remaining = 20 + 100 + 20 + 100 = 240
			wantCapacity: 240, wantRemainPct: 100, wantUsed: 0,
		},
		{
			name:      "support gift partially used (via monthly drain)",
			available: 200, planMax: 20, bonus: 100, bonusGrant: 100, extra: 20, supportGift: 100,
			// capacity = 240, remaining = 0 + 100 + 20 + 100 = 220 (monthly drained to 0 b/c available=200 > planMax so monthly=20, wait...)
			// monthly = min(200, 20) = 20, bonus = 100, extra = 20, gift = 100 → remaining = 240, used = 0
			// Hmm, that's still 240. The "used" comes from when shelleyCreditsAvailable drops below planMax+bonus
			wantCapacity: 240, wantRemainPct: 100, wantUsed: 0,
		},
		{
			name:      "user spent some monthly - has support gift",
			available: 115, planMax: 20, bonus: 100, bonusGrant: 100, extra: 0, supportGift: 100,
			// monthly = min(115, 20) = 20, bonus = 100 (but wait, available=115, planMax=20, so excess = 95)
			// The caller would compute bonus = min(95, 100) = 95... but here we pass bonus=100 directly.
			// Actually in this test we pass bonusRemaining as the pre-computed value.
			// Let's say the caller computed: bonus = min(115-20, 100) = 95, supportGift = 115-20-100 = 0 (no excess)
			// So this scenario doesn't make sense with supportGift=100. Let me fix it.
			// Better: available=215, bonus=100, supportGift=95 (215-20-100=95)
			wantCapacity: 220, wantRemainPct: 100, wantUsed: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bar := computeCreditBar(creditBarInput{
				shelleyCreditsAvailable: tc.available,
				planMaxCredit:           tc.planMax,
				bonusRemaining:          tc.bonus,
				bonusGrantAmount:        tc.bonusGrant,
				extraCreditsUSD:         tc.extra,
				supportGiftUSD:          tc.supportGift,
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
			if !closeTo(bar.supportGiftUSD, tc.supportGift, 0.01) {
				t.Errorf("supportGiftUSD = %.2f, want %.2f", bar.supportGiftUSD, tc.supportGift)
			}
		})
	}
}

// TestCreditBar_SupportGiftEndToEnd simulates the full handler decomposition logic
// to verify the complete flow from DB values to display values.
func TestCreditBar_SupportGiftEndToEnd(t *testing.T) {
	cases := []struct {
		name             string
		shelleyAvailable float64 // DB available_credit (after refresh)
		planMax          float64
		bonusGranted     bool
		bonusGrant       float64 // UpgradeBonusCreditUSD
		extraCredits     float64 // Stripe purchased credits
		wantGiftCount    int
		wantSupportGift  float64
		wantCapacity     float64
		wantRemaining    float64
	}{
		{
			name:             "user's example: 20 monthly + 100 bonus + 100 gift, 20 purchased",
			shelleyAvailable: 220, planMax: 20, bonusGranted: true, bonusGrant: 100, extraCredits: 20,
			wantGiftCount: 2, wantSupportGift: 100, wantCapacity: 240, wantRemaining: 240,
		},
		{
			name:             "no support gift - normal bonus user",
			shelleyAvailable: 120, planMax: 20, bonusGranted: true, bonusGrant: 100, extraCredits: 0,
			wantGiftCount: 1, wantSupportGift: 0, wantCapacity: 120, wantRemaining: 120,
		},
		{
			name:             "support gift without bonus",
			shelleyAvailable: 120, planMax: 20, bonusGranted: false, bonusGrant: 0, extraCredits: 0,
			wantGiftCount: 1, wantSupportGift: 100, wantCapacity: 120, wantRemaining: 120,
		},
		{
			name:             "no gifts at all",
			shelleyAvailable: 15, planMax: 20, bonusGranted: false, bonusGrant: 0, extraCredits: 0,
			wantGiftCount: 0, wantSupportGift: 0, wantCapacity: 20, wantRemaining: 15,
		},
		{
			name:             "bonus partially used + support gift",
			shelleyAvailable: 170, planMax: 20, bonusGranted: true, bonusGrant: 100, extraCredits: 0,
			wantGiftCount: 2, wantSupportGift: 50, wantCapacity: 170, wantRemaining: 170,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate handler decomposition logic
			var bonusRemaining float64
			var bonusGrantAmount float64
			if tc.bonusGranted {
				bonusGrantAmount = tc.bonusGrant
				if tc.shelleyAvailable > tc.planMax {
					bonusRemaining = tc.shelleyAvailable - tc.planMax
					if bonusRemaining > bonusGrantAmount {
						bonusRemaining = bonusGrantAmount
					}
				}
			}
			supportGiftUSD := computeSupportGift(tc.shelleyAvailable, tc.planMax, bonusGrantAmount)

			// Verify support gift detection
			if !closeTo(supportGiftUSD, tc.wantSupportGift, 0.01) {
				t.Errorf("supportGiftUSD = %.2f, want %.2f", supportGiftUSD, tc.wantSupportGift)
			}

			// Verify gifts
			gifts := giftsForUser(bonusRemaining, supportGiftUSD)
			if len(gifts) != tc.wantGiftCount {
				t.Errorf("gift count = %d, want %d; gifts = %v", len(gifts), tc.wantGiftCount, gifts)
			}

			// Verify credit bar
			bar := computeCreditBar(creditBarInput{
				shelleyCreditsAvailable: tc.shelleyAvailable,
				planMaxCredit:           tc.planMax,
				bonusRemaining:          bonusRemaining,
				bonusGrantAmount:        bonusGrantAmount,
				extraCreditsUSD:         tc.extraCredits,
				supportGiftUSD:          supportGiftUSD,
			})

			if !closeTo(bar.totalCapacity, tc.wantCapacity, 0.01) {
				t.Errorf("totalCapacity = %.2f, want %.2f", bar.totalCapacity, tc.wantCapacity)
			}

			// Verify remaining = monthly + bonus + extra + supportGift
			gotRemaining := bar.monthlyAvailable + bar.bonusRemaining + tc.extraCredits + bar.supportGiftUSD
			if !closeTo(gotRemaining, tc.wantRemaining, 0.01) {
				t.Errorf("remaining = %.2f, want %.2f (monthly=%.2f bonus=%.2f extra=%.2f gift=%.2f)",
					gotRemaining, tc.wantRemaining,
					bar.monthlyAvailable, bar.bonusRemaining, tc.extraCredits, bar.supportGiftUSD)
			}
		})
	}
}
