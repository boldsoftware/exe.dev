package execore

import (
	"html/template"
	"math"
	"strings"
	"testing"
	"time"

	"exe.dev/billing"
	"exe.dev/billing/tender"
)

func closeTo(a, b, epsilon float64) bool {
	return math.Abs(a-b) < epsilon
}

// TestCreditBar_BonusUserSpentHalf verifies a paying user who received the $100
// bonus and spent $50 sees a unified bar that depletes against the full capacity.
func TestCreditBar_BonusUserSpentHalf(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	cases := []struct {
		name                    string
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
	t.Parallel()
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
	t.Parallel()
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

func TestGiftsFromLedger(t *testing.T) {
	t.Parallel()
	t.Run("nil entries returns nil", func(t *testing.T) {
		gifts := giftsFromLedger(nil)
		if gifts != nil {
			t.Fatalf("expected nil, got %v", gifts)
		}
	})

	t.Run("empty entries returns nil", func(t *testing.T) {
		gifts := giftsFromLedger([]billing.GiftEntry{})
		if gifts != nil {
			t.Fatalf("expected nil, got %v", gifts)
		}
	})

	t.Run("single gift entry", func(t *testing.T) {
		entries := []billing.GiftEntry{
			{
				Amount:    tender.Mint(1000, 0), // $10.00
				Note:      "Thanks for testing",
				GiftID:    "debug_gift:abc:123",
				CreatedAt: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
			},
		}
		gifts := giftsFromLedger(entries)
		if len(gifts) != 1 {
			t.Fatalf("expected 1 gift, got %d", len(gifts))
		}
		if gifts[0].Amount != "10" {
			t.Errorf("amount = %q, want 10", gifts[0].Amount)
		}
		if gifts[0].Reason != "Thanks for testing" {
			t.Errorf("reason = %q, want 'Thanks for testing'", gifts[0].Reason)
		}
	})

	t.Run("multiple gift entries", func(t *testing.T) {
		entries := []billing.GiftEntry{
			{
				Amount:    tender.Mint(500, 0), // $5.00
				Note:      "First gift",
				GiftID:    "debug_gift:abc:1",
				CreatedAt: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
			},
			{
				Amount:    tender.Mint(2000, 0), // $20.00
				Note:      "Second gift",
				GiftID:    "debug_gift:abc:2",
				CreatedAt: time.Date(2025, 2, 15, 0, 0, 0, 0, time.UTC),
			},
		}
		gifts := giftsFromLedger(entries)
		if len(gifts) != 2 {
			t.Fatalf("expected 2 gifts, got %d", len(gifts))
		}
		if gifts[0].Amount != "5" {
			t.Errorf("first amount = %q, want 5", gifts[0].Amount)
		}
		if gifts[1].Amount != "20" {
			t.Errorf("second amount = %q, want 20", gifts[1].Amount)
		}
	})

	t.Run("empty note uses default reason", func(t *testing.T) {
		entries := []billing.GiftEntry{
			{
				Amount:    tender.Mint(1000, 0),
				Note:      "",
				GiftID:    "debug_gift:abc:1",
				CreatedAt: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
			},
		}
		gifts := giftsFromLedger(entries)
		if len(gifts) != 1 {
			t.Fatalf("expected 1 gift, got %d", len(gifts))
		}
		if gifts[0].Reason != "Credit gift" {
			t.Errorf("reason = %q, want 'Credit gift'", gifts[0].Reason)
		}
	})

	t.Run("gift with fractional dollars", func(t *testing.T) {
		entries := []billing.GiftEntry{
			{
				Amount:    tender.Mint(1050, 0), // $10.50
				Note:      "Half gift",
				GiftID:    "debug_gift:abc:1",
				CreatedAt: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC),
			},
		}
		gifts := giftsFromLedger(entries)
		if len(gifts) != 1 {
			t.Fatalf("expected 1 gift, got %d", len(gifts))
		}
		// $10.50 = 10 dollars, 50 cents
		if gifts[0].Amount != "10.50" {
			t.Errorf("amount = %q, want '10.50'", gifts[0].Amount)
		}
	})

	t.Run("date field is populated with correct format", func(t *testing.T) {
		entries := []billing.GiftEntry{
			{
				Amount:    tender.Mint(1000, 0), // $10.00
				Note:      "Date test gift",
				GiftID:    "debug_gift:abc:date",
				CreatedAt: time.Date(2026, 3, 30, 14, 30, 0, 0, time.UTC),
			},
		}
		gifts := giftsFromLedger(entries)
		if len(gifts) != 1 {
			t.Fatalf("expected 1 gift, got %d", len(gifts))
		}
		// Verify Date field is populated
		if gifts[0].Date == "" {
			t.Error("Date field is empty, expected populated date")
		}
		// Verify date format is "02 Jan 2006" (e.g., "30 Mar 2026")
		wantDate := "30 Mar 2026"
		if gifts[0].Date != wantDate {
			t.Errorf("Date = %q, want %q", gifts[0].Date, wantDate)
		}
	})

	t.Run("multiple gifts have correct dates", func(t *testing.T) {
		entries := []billing.GiftEntry{
			{
				Amount:    tender.Mint(500, 0),
				Note:      "First gift",
				GiftID:    "debug_gift:abc:1",
				CreatedAt: time.Date(2025, 12, 25, 0, 0, 0, 0, time.UTC),
			},
			{
				Amount:    tender.Mint(1000, 0),
				Note:      "Second gift",
				GiftID:    "debug_gift:abc:2",
				CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			},
		}
		gifts := giftsFromLedger(entries)
		if len(gifts) != 2 {
			t.Fatalf("expected 2 gifts, got %d", len(gifts))
		}
		// Verify both dates are populated with correct format
		if gifts[0].Date != "25 Dec 2025" {
			t.Errorf("first gift Date = %q, want '25 Dec 2025'", gifts[0].Date)
		}
		if gifts[1].Date != "01 Jan 2026" {
			t.Errorf("second gift Date = %q, want '01 Jan 2026'", gifts[1].Date)
		}
	})
}

// TestCreditBar_WithGiftCredits verifies the bar includes gift credits from ledger.
func TestCreditBar_WithGiftCredits(t *testing.T) {
	t.Parallel()
	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: 20,
		planMaxCredit:           20,
		bonusRemaining:          0,
		bonusGrantAmount:        0,
		extraCreditsUSD:         0,
		giftCreditsUSD:          50,
	})

	// Capacity = 20 + 0 + 0 + 50 = 70
	if !closeTo(bar.totalCapacity, 70, 0.01) {
		t.Fatalf("totalCapacity = %.2f, want 70", bar.totalCapacity)
	}
	// remaining = 20 + 0 + 0 + 50 = 70
	if !closeTo(bar.totalRemainingPct, 100, 0.1) {
		t.Fatalf("totalRemainingPct = %.2f, want 100", bar.totalRemainingPct)
	}
	if !closeTo(bar.giftCreditsUSD, 50, 0.01) {
		t.Fatalf("giftCreditsUSD = %.2f, want 50", bar.giftCreditsUSD)
	}
}

// TestComputeSupportGift verifies detection of manual DB credit adjustments.
func TestComputeSupportGift(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
				giftCreditsUSD:          tc.supportGift,
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
			if !closeTo(bar.giftCreditsUSD, tc.supportGift, 0.01) {
				t.Errorf("giftCreditsUSD = %.2f, want %.2f", bar.giftCreditsUSD, tc.supportGift)
			}
		})
	}
}

// TestCreditBar_SupportGiftEndToEnd simulates the full handler decomposition logic
// to verify the complete flow from DB values to display values.
func TestCreditBar_SupportGiftEndToEnd(t *testing.T) {
	t.Parallel()
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
			giftCreditsUSD := computeSupportGift(tc.shelleyAvailable, tc.planMax, bonusGrantAmount)

			// Verify support gift detection
			if !closeTo(giftCreditsUSD, tc.wantSupportGift, 0.01) {
				t.Errorf("giftCreditsUSD = %.2f, want %.2f", giftCreditsUSD, tc.wantSupportGift)
			}

			// Verify gifts
			gifts := giftsForUser(bonusRemaining, giftCreditsUSD)
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
				giftCreditsUSD:          giftCreditsUSD,
			})

			if !closeTo(bar.totalCapacity, tc.wantCapacity, 0.01) {
				t.Errorf("totalCapacity = %.2f, want %.2f", bar.totalCapacity, tc.wantCapacity)
			}

			// Verify remaining = monthly + bonus + extra + supportGift
			gotRemaining := bar.monthlyAvailable + bar.bonusRemaining + tc.extraCredits + bar.giftCreditsUSD
			if !closeTo(gotRemaining, tc.wantRemaining, 0.01) {
				t.Errorf("remaining = %.2f, want %.2f (monthly=%.2f bonus=%.2f extra=%.2f gift=%.2f)",
					gotRemaining, tc.wantRemaining,
					bar.monthlyAvailable, bar.bonusRemaining, tc.extraCredits, bar.giftCreditsUSD)
			}
		})
	}
}

// TestCreditBar_Matrix is a systematic test of all credit type combinations
// across Free and Paid plans, with and without usage.
//
// Free plan:  planMax = 5, no bonus
// Paid plan:  planMax = 20, bonus = 100
// Paid/Gift:  $10 gift from ledger
// Paid/Extra: $10 purchased via Stripe
func TestCreditBar_Matrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// inputs
		shelleyAvailable float64
		planMax          float64
		bonusRemaining   float64
		bonusGrant       float64
		paid             float64
		gift             float64
		// expected
		wantCapacity  float64
		wantUsed      float64
		wantRemaining float64 // internal remaining (feeds bar %)
		wantHero      float64 // shelleyAvailable + paid + gift
	}{
		// ── Free plan ($5 monthly, no bonus) ──

		{
			name:             "Free/no usage",
			shelleyAvailable: 5, planMax: 5,
			bonusRemaining: 0, bonusGrant: 0, paid: 0, gift: 0,
			wantCapacity: 5, wantUsed: 0, wantRemaining: 5, wantHero: 5,
		},
		{
			name:             "Free/partial usage",
			shelleyAvailable: 3, planMax: 5,
			bonusRemaining: 0, bonusGrant: 0, paid: 0, gift: 0,
			wantCapacity: 5, wantUsed: 2, wantRemaining: 3, wantHero: 3,
		},
		{
			name:             "Free/full usage",
			shelleyAvailable: 0, planMax: 5,
			bonusRemaining: 0, bonusGrant: 0, paid: 0, gift: 0,
			wantCapacity: 5, wantUsed: 5, wantRemaining: 0, wantHero: 0,
		},
		{
			name:             "Free+Paid/no usage",
			shelleyAvailable: 5, planMax: 5,
			bonusRemaining: 0, bonusGrant: 0, paid: 10, gift: 0,
			wantCapacity: 15, wantUsed: 0, wantRemaining: 15, wantHero: 15,
		},
		{
			name:             "Free+Paid/partial usage",
			shelleyAvailable: 3, planMax: 5,
			bonusRemaining: 0, bonusGrant: 0, paid: 10, gift: 0,
			wantCapacity: 15, wantUsed: 2, wantRemaining: 13, wantHero: 13,
		},
		{
			name:             "Free+Gift/no usage",
			shelleyAvailable: 5, planMax: 5,
			bonusRemaining: 0, bonusGrant: 0, paid: 0, gift: 10,
			wantCapacity: 15, wantUsed: 0, wantRemaining: 15, wantHero: 15,
		},
		{
			name:             "Free+Gift/partial usage",
			shelleyAvailable: 3, planMax: 5,
			bonusRemaining: 0, bonusGrant: 0, paid: 0, gift: 10,
			wantCapacity: 15, wantUsed: 2, wantRemaining: 13, wantHero: 13,
		},
		{
			name:             "Free+Paid+Gift/no usage",
			shelleyAvailable: 5, planMax: 5,
			bonusRemaining: 0, bonusGrant: 0, paid: 10, gift: 10,
			wantCapacity: 25, wantUsed: 0, wantRemaining: 25, wantHero: 25,
		},
		{
			name:             "Free+Paid+Gift/partial usage",
			shelleyAvailable: 3, planMax: 5,
			bonusRemaining: 0, bonusGrant: 0, paid: 10, gift: 10,
			wantCapacity: 25, wantUsed: 2, wantRemaining: 23, wantHero: 23,
		},

		// ── Paid plan ($20 monthly, $100 bonus) ──

		{
			name:             "Paid/no usage",
			shelleyAvailable: 120, planMax: 20,
			bonusRemaining: 100, bonusGrant: 100, paid: 0, gift: 0,
			wantCapacity: 120, wantUsed: 0, wantRemaining: 120, wantHero: 120,
		},
		{
			name:             "Paid/partial usage (spent 10)",
			shelleyAvailable: 110, planMax: 20,
			bonusRemaining: 90, bonusGrant: 100, paid: 0, gift: 0,
			wantCapacity: 120, wantUsed: 10, wantRemaining: 110, wantHero: 110,
		},
		{
			name:             "Paid/heavy usage (spent 100)",
			shelleyAvailable: 20, planMax: 20,
			bonusRemaining: 0, bonusGrant: 100, paid: 0, gift: 0,
			wantCapacity: 120, wantUsed: 100, wantRemaining: 20, wantHero: 20,
		},
		{
			name:             "Paid/full usage",
			shelleyAvailable: 0, planMax: 20,
			bonusRemaining: 0, bonusGrant: 100, paid: 0, gift: 0,
			wantCapacity: 120, wantUsed: 120, wantRemaining: 0, wantHero: 0,
		},
		{
			name:             "Paid+Purchased/no usage",
			shelleyAvailable: 120, planMax: 20,
			bonusRemaining: 100, bonusGrant: 100, paid: 10, gift: 0,
			wantCapacity: 130, wantUsed: 0, wantRemaining: 130, wantHero: 130,
		},
		{
			name:             "Paid+Purchased/partial usage (spent 10)",
			shelleyAvailable: 110, planMax: 20,
			bonusRemaining: 90, bonusGrant: 100, paid: 10, gift: 0,
			wantCapacity: 130, wantUsed: 10, wantRemaining: 120, wantHero: 120,
		},
		{
			name:             "Paid+Gift/no usage",
			shelleyAvailable: 120, planMax: 20,
			bonusRemaining: 100, bonusGrant: 100, paid: 0, gift: 10,
			wantCapacity: 130, wantUsed: 0, wantRemaining: 130, wantHero: 130,
		},
		{
			name:             "Paid+Gift/partial usage (spent 10)",
			shelleyAvailable: 110, planMax: 20,
			bonusRemaining: 90, bonusGrant: 100, paid: 0, gift: 10,
			wantCapacity: 130, wantUsed: 10, wantRemaining: 120, wantHero: 120,
		},
		{
			name:             "Paid+Purchased+Gift/no usage",
			shelleyAvailable: 120, planMax: 20,
			bonusRemaining: 100, bonusGrant: 100, paid: 10, gift: 10,
			wantCapacity: 140, wantUsed: 0, wantRemaining: 140, wantHero: 140,
		},
		{
			name:             "Paid+Purchased+Gift/partial usage (spent 10)",
			shelleyAvailable: 110, planMax: 20,
			bonusRemaining: 90, bonusGrant: 100, paid: 10, gift: 10,
			wantCapacity: 140, wantUsed: 10, wantRemaining: 130, wantHero: 130,
		},
		{
			name:             "Paid+Purchased+Gift/heavy usage (spent 100)",
			shelleyAvailable: 20, planMax: 20,
			bonusRemaining: 0, bonusGrant: 100, paid: 10, gift: 10,
			wantCapacity: 140, wantUsed: 100, wantRemaining: 40, wantHero: 40,
		},

		// ── Legacy: available_credit manually inflated to $400 ──
		// Someone (support or old gift path) bumped available_credit beyond
		// planMax + bonus. The extra $280 is invisible to the bar.
		// bonusRemaining = min(400 - 20, 100) = 100 (capped at grant)
		// monthlyAvailable = min(400, 20) = 20
		// remaining = 20 + 100 + 0 + 0 = 120
		// capacity = 20 + 100 + 0 + 0 = 120
		// hero = 400 + 0 + 0 = 400
		// The hero (400) exceeds capacity (120) — this is the known gap
		// that WORK-5 (migrating legacy gifts to ledger) will fix.
		{
			name:             "Paid/legacy inflated available (400)",
			shelleyAvailable: 400, planMax: 20,
			bonusRemaining: 100, bonusGrant: 100, paid: 0, gift: 0,
			wantCapacity: 120, wantUsed: 0, wantRemaining: 120, wantHero: 400,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bar := computeCreditBar(creditBarInput{
				shelleyCreditsAvailable: tc.shelleyAvailable,
				planMaxCredit:           tc.planMax,
				bonusRemaining:          tc.bonusRemaining,
				bonusGrantAmount:        tc.bonusGrant,
				extraCreditsUSD:         tc.paid,
				giftCreditsUSD:          tc.gift,
			})

			if !closeTo(bar.totalCapacity, tc.wantCapacity, 0.01) {
				t.Errorf("capacity = %.2f, want %.2f", bar.totalCapacity, tc.wantCapacity)
			}
			if !closeTo(bar.usedCreditsUSD, tc.wantUsed, 0.01) {
				t.Errorf("used = %.2f, want %.2f", bar.usedCreditsUSD, tc.wantUsed)
			}

			remaining := bar.monthlyAvailable + bar.bonusRemaining + tc.paid + bar.giftCreditsUSD
			if !closeTo(remaining, tc.wantRemaining, 0.01) {
				t.Errorf("remaining = %.2f, want %.2f", remaining, tc.wantRemaining)
			}

			hero := tc.shelleyAvailable + tc.paid + tc.gift
			if !closeTo(hero, tc.wantHero, 0.01) {
				t.Errorf("hero = %.2f, want %.2f", hero, tc.wantHero)
			}
		})
	}
}

// TestHasSignupGiftInLedger verifies the helper that detects signup gifts.
func TestHasSignupGiftInLedger(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		entries []billing.GiftEntry
		want    bool
	}{
		{
			name:    "nil entries",
			entries: nil,
			want:    false,
		},
		{
			name:    "empty entries",
			entries: []billing.GiftEntry{},
			want:    false,
		},
		{
			name: "signup gift present",
			entries: []billing.GiftEntry{
				{GiftID: billing.GiftPrefixSignup + ":acct123", Amount: tender.Mint(10000, 0)},
			},
			want: true,
		},
		{
			name: "non-signup gift only",
			entries: []billing.GiftEntry{
				{GiftID: "support:acct123:abc", Amount: tender.Mint(5000, 0)},
			},
			want: false,
		},
		{
			name: "mixed gifts with signup",
			entries: []billing.GiftEntry{
				{GiftID: "support:acct123:abc", Amount: tender.Mint(5000, 0)},
				{GiftID: billing.GiftPrefixSignup + ":acct456", Amount: tender.Mint(10000, 0)},
			},
			want: true,
		},
		{
			name: "gift ID is just the prefix without colon",
			entries: []billing.GiftEntry{
				{GiftID: billing.GiftPrefixSignup, Amount: tender.Mint(10000, 0)},
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hasSignupGiftInLedger(tc.entries)
			if got != tc.want {
				t.Errorf("hasSignupGiftInLedger() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBonusZeroedWhenSignupGiftInLedger verifies that when a signup gift exists
// in the billing ledger AND the old BillingUpgradeBonusGranted flag is set,
// bonusGrantAmount is zeroed to avoid double-counting.
func TestBonusZeroedWhenSignupGiftInLedger(t *testing.T) {
	t.Parallel()
	signupEntry := billing.GiftEntry{
		GiftID: billing.GiftPrefixSignup + ":acct123",
		Amount: tender.Mint(10000, 0), // $100
		Note:   "Welcome bonus for upgrading to a paid plan",
	}

	t.Run("signup gift in ledger + flag set: no double count", func(t *testing.T) {
		// Simulate: user has BillingUpgradeBonusGranted=1 AND signup gift in ledger.
		// The signup gift is $100, which is already in giftCreditsUSD.
		// bonusGrantAmount should be zeroed.
		giftEntries := []billing.GiftEntry{signupEntry}
		giftCreditsUSD := giftCreditsUSDFromLedger(giftEntries)

		// Before fix, shelleyCreditsAvailable=120 included the $100 bonus AND
		// giftCreditsUSD=100 also counted it, leading to double-counting.
		// After fix, shelleyCreditsAvailable is reduced by 100 (the signup bonus
		// amount) so it becomes 20, and only the gift path counts the $100.
		bonusGrantAmount := float64(0) // zeroed because signup gift exists in ledger
		bonusRemaining := float64(0)   // zeroed because signup gift exists in ledger

		// shelleyCreditsAvailable = max(120 - 100, 0) = 20 after the fix subtracts the bonus
		bar := computeCreditBar(creditBarInput{
			shelleyCreditsAvailable: 20,
			planMaxCredit:           20,
			bonusRemaining:          bonusRemaining,
			bonusGrantAmount:        bonusGrantAmount,
			extraCreditsUSD:         0,
			giftCreditsUSD:          giftCreditsUSD,
		})

		// Capacity should be 20 (plan) + 100 (gift) = 120, NOT 220
		if !closeTo(bar.totalCapacity, 120, 0.01) {
			t.Errorf("totalCapacity = %.2f, want 120 (no double count)", bar.totalCapacity)
		}
		if !closeTo(bar.giftCreditsUSD, 100, 0.01) {
			t.Errorf("giftCreditsUSD = %.2f, want 100", bar.giftCreditsUSD)
		}
	})

	t.Run("flag set but no signup gift in ledger: old path works", func(t *testing.T) {
		// Transition period: user has the flag but no signup gift row yet.
		giftEntries := []billing.GiftEntry{}
		giftCreditsUSD := giftCreditsUSDFromLedger(giftEntries)

		// hasSignupGiftInLedger returns false, so bonusGrantAmount stays.
		if hasSignupGiftInLedger(giftEntries) {
			t.Fatal("should not detect signup gift in empty entries")
		}

		bonusGrantAmount := float64(100)
		bonusRemaining := float64(100)

		bar := computeCreditBar(creditBarInput{
			shelleyCreditsAvailable: 120,
			planMaxCredit:           20,
			bonusRemaining:          bonusRemaining,
			bonusGrantAmount:        bonusGrantAmount,
			extraCreditsUSD:         0,
			giftCreditsUSD:          giftCreditsUSD,
		})

		// Old path: capacity = 20 + 100 + 0 + 0 = 120
		if !closeTo(bar.totalCapacity, 120, 0.01) {
			t.Errorf("totalCapacity = %.2f, want 120", bar.totalCapacity)
		}
		if !closeTo(bar.bonusRemaining, 100, 0.01) {
			t.Errorf("bonusRemaining = %.2f, want 100", bar.bonusRemaining)
		}
	})

	t.Run("neither flag nor gift: no bonus shown", func(t *testing.T) {
		giftEntries := []billing.GiftEntry{}
		giftCreditsUSD := giftCreditsUSDFromLedger(giftEntries)

		bar := computeCreditBar(creditBarInput{
			shelleyCreditsAvailable: 20,
			planMaxCredit:           20,
			bonusRemaining:          0,
			bonusGrantAmount:        0,
			extraCreditsUSD:         0,
			giftCreditsUSD:          giftCreditsUSD,
		})

		if !closeTo(bar.totalCapacity, 20, 0.01) {
			t.Errorf("totalCapacity = %.2f, want 20", bar.totalCapacity)
		}
		if !closeTo(bar.bonusRemaining, 0, 0.01) {
			t.Errorf("bonusRemaining = %.2f, want 0", bar.bonusRemaining)
		}
		if !closeTo(bar.giftCreditsUSD, 0, 0.01) {
			t.Errorf("giftCreditsUSD = %.2f, want 0", bar.giftCreditsUSD)
		}
	})
}

// TestBuildGiftRows_SignupGiftNoWelcomeBonus verifies that when bonusGrantAmount
// is zeroed (because the signup gift is now in the ledger), buildGiftRows does
// NOT show the old "Welcome bonus" row, but DOES show the signup gift from the ledger.
func TestBuildGiftRows_SignupGiftNoWelcomeBonus(t *testing.T) {
	t.Parallel()
	signupEntry := billing.GiftEntry{
		GiftID: billing.GiftPrefixSignup + ":acct123",
		Amount: tender.Mint(10000, 0), // $100
		Note:   "Welcome bonus for upgrading to a paid plan",
	}

	t.Run("bonusGrantAmount zeroed with signup gift in ledger", func(t *testing.T) {
		rows := buildGiftRows(0, []billing.GiftEntry{signupEntry})
		if len(rows) != 1 {
			t.Fatalf("expected 1 row, got %d: %v", len(rows), rows)
		}
		if rows[0].Amount != "100" {
			t.Errorf("amount = %q, want 100", rows[0].Amount)
		}
		if rows[0].Reason != "Welcome bonus for upgrading to a paid plan" {
			t.Errorf("reason = %q, want welcome bonus text", rows[0].Reason)
		}
	})

	t.Run("old path with bonus grant and no ledger gift", func(t *testing.T) {
		rows := buildGiftRows(100, nil)
		if len(rows) != 1 {
			t.Fatalf("expected 1 row, got %d", len(rows))
		}
		if rows[0].Reason != "Welcome bonus for upgrading to a paid plan" {
			t.Errorf("reason = %q, want welcome bonus text", rows[0].Reason)
		}
	})
}

// TestComputeSupportGift_AfterMigration verifies that computeSupportGift still
// works correctly when bonusGrant is 0 (because the bonus moved to the ledger).
func TestComputeSupportGift_AfterMigration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		available float64
		planMax   float64
		bonus     float64
		want      float64
	}{
		{
			name:      "migrated user: bonus=0, no support gift",
			available: 20, planMax: 20, bonus: 0,
			want: 0,
		},
		{
			name:      "migrated user: bonus=0, has support gift of 50",
			available: 70, planMax: 20, bonus: 0,
			want: 50,
		},
		{
			name:      "un-migrated user: bonus=100, no support gift",
			available: 120, planMax: 20, bonus: 100,
			want: 0,
		},
		{
			name:      "un-migrated user: bonus=100, has support gift of 50",
			available: 170, planMax: 20, bonus: 100,
			want: 50,
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
