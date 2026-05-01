package plan

import (
	"context"
	"testing"
	"time"

	"exe.dev/exedb"
)

func TestGetPlanCategory(t *testing.T) {
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)
	oldDate := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	newDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		inputs userPlanInputs
		want   Category
	}{
		{
			name:   "canceled overrides grandfathered",
			inputs: userPlanInputs{BillingStatus: "canceled", CreatedAt: &oldDate},
			want:   CategoryBasic,
		},
		{
			name:   "canceled overrides trial",
			inputs: userPlanInputs{BillingStatus: "canceled", PlanID: new("trial:monthly:20260106"), TrialExpiresAt: &future},
			want:   CategoryBasic,
		},
		{
			name:   "friend without overrides",
			inputs: userPlanInputs{PlanID: new("friend")},
			want:   CategoryFriend,
		},
		{
			name:   "has_billing is individual",
			inputs: userPlanInputs{BillingStatus: "active"},
			want:   CategoryIndividual,
		},
		{
			name:   "trial not expired is trial",
			inputs: userPlanInputs{PlanID: new("trial:monthly:20260106"), TrialExpiresAt: &future},
			want:   CategoryTrial,
		},
		{
			name:   "trial expired falls through",
			inputs: userPlanInputs{PlanID: new("trial:monthly:20260106"), TrialExpiresAt: &past, CreatedAt: &newDate},
			want:   CategoryBasic,
		},
		{
			name:   "old user is grandfathered",
			inputs: userPlanInputs{CreatedAt: &oldDate},
			want:   CategoryGrandfathered,
		},
		{
			name:   "new user with nothing is basic",
			inputs: userPlanInputs{CreatedAt: &newDate},
			want:   CategoryBasic,
		},
		{
			name:   "team member covered by billing owner",
			inputs: userPlanInputs{CreatedAt: &newDate, TeamBillingActive: true},
			want:   CategoryTeam,
		},
		{
			name:   "canceled user on team still basic",
			inputs: userPlanInputs{BillingStatus: "canceled", TeamBillingActive: true},
			want:   CategoryBasic,
		},
		{
			name:   "individual with own billing on team resolves to team",
			inputs: userPlanInputs{BillingStatus: "active", TeamBillingActive: true},
			want:   CategoryTeam,
		},
		{
			name:   "grandfathered user on team resolves to team",
			inputs: userPlanInputs{CreatedAt: &oldDate, TeamBillingActive: true},
			want:   CategoryTeam,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getPlanCategory(tt.inputs)
			if got != tt.want {
				t.Errorf("getPlanCategory() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTeamMemberCanCreateVM exercises the exact bug scenario:
// a team member with no personal billing whose team billing owner covers them
// should resolve to CategoryTeam and be granted VMCreate.
func TestTeamMemberCanCreateVM(t *testing.T) {
	newDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	inputs := userPlanInputs{
		CreatedAt:         &newDate,
		TeamBillingActive: true,
	}
	version := getPlanCategory(inputs)
	if version != CategoryTeam {
		t.Fatalf("getPlanCategory() = %q, want %q", version, CategoryTeam)
	}
	if !Grants(ID(version), VMCreate) {
		t.Errorf("Grants(%q, VMCreate) = false, want true", version)
	}
}

// TestTeamMemberDeniedWithoutBillingOwner verifies that a team member
// without billing owner coverage falls through to Basic and is denied VMCreate.
func TestTeamMemberDeniedWithoutBillingOwner(t *testing.T) {
	newDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	inputs := userPlanInputs{
		CreatedAt:         &newDate,
		TeamBillingActive: false,
	}
	version := getPlanCategory(inputs)
	if version != CategoryBasic {
		t.Fatalf("getPlanCategory() = %q, want %q", version, CategoryBasic)
	}
	if Grants(ID(version), VMCreate) {
		t.Errorf("Grants(%q, VMCreate) = true, want false", version)
	}
}

func TestSignupBonusCreditUSD(t *testing.T) {
	tests := []struct {
		version Category
		want    float64
	}{
		{CategoryIndividual, 100.0},
		{CategoryTeam, 0},
		{CategoryFriend, 0},
		{CategoryGrandfathered, 0},
		{CategoryTrial, 0},
		{CategoryBasic, 0},
		{CategoryRestricted, 0},
	}
	for _, tt := range tests {
		p, ok := plans[tt.version]
		if !ok {
			t.Fatalf("plan %q not found", tt.version)
		}
		if p.SignupBonusCreditUSD != tt.want {
			t.Errorf("plan %q SignupBonusCreditUSD = %v, want %v", tt.version, p.SignupBonusCreditUSD, tt.want)
		}
	}
}

func TestParsePlanID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantPlan Category
		wantInt  string
		wantVer  string
	}{
		{
			name:     "versioned individual monthly",
			input:    "individual:monthly:20260325",
			wantPlan: CategoryIndividual,
			wantInt:  "monthly",
			wantVer:  "20260325",
		},
		{
			name:     "versioned basic monthly",
			input:    "basic:monthly:20260101",
			wantPlan: CategoryBasic,
			wantInt:  "monthly",
			wantVer:  "20260101",
		},
		{
			name:     "versioned team yearly",
			input:    "team:yearly:20260106",
			wantPlan: CategoryTeam,
			wantInt:  "yearly",
			wantVer:  "20260106",
		},
		{
			name:     "bare individual",
			input:    "individual",
			wantPlan: CategoryIndividual,
			wantInt:  "",
			wantVer:  "",
		},
		{
			name:     "bare basic",
			input:    "basic",
			wantPlan: CategoryBasic,
			wantInt:  "",
			wantVer:  "",
		},
		{
			name:     "bare friend",
			input:    "friend",
			wantPlan: CategoryFriend,
			wantInt:  "",
			wantVer:  "",
		},
		{
			name:     "empty string",
			input:    "",
			wantPlan: Category(""),
			wantInt:  "",
			wantVer:  "",
		},
		{
			name:     "two parts treated as bare",
			input:    "individual:monthly",
			wantPlan: Category("individual:monthly"),
			wantInt:  "",
			wantVer:  "",
		},
		{
			name:     "version with colons in timestamp",
			input:    "individual:monthly:2026:03:25",
			wantPlan: CategoryIndividual,
			wantInt:  "monthly",
			wantVer:  "2026:03:25",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, interval, ver := ParseID(tt.input)
			if plan != tt.wantPlan {
				t.Errorf("ParseID(%q) plan = %q, want %q", tt.input, plan, tt.wantPlan)
			}
			if interval != tt.wantInt {
				t.Errorf("ParseID(%q) interval = %q, want %q", tt.input, interval, tt.wantInt)
			}
			if ver != tt.wantVer {
				t.Errorf("ParseID(%q) version = %q, want %q", tt.input, ver, tt.wantVer)
			}
		})
	}
}

func TestBasePlan(t *testing.T) {
	tests := []struct {
		input string
		want  Category
	}{
		{"individual:monthly:20260325", CategoryIndividual},
		{"basic:monthly:20260101", CategoryBasic},
		{"individual", CategoryIndividual},
		{"basic", CategoryBasic},
		{"friend", CategoryFriend},
	}
	for _, tt := range tests {
		got := Base(tt.input)
		if got != tt.want {
			t.Errorf("Base(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGetPlanByIDVersioned(t *testing.T) {
	// Versioned ID should resolve to the base plan.
	p, ok := ByID("individual:monthly:20260325")
	if !ok {
		t.Fatal("ByID(\"individual:monthly:20260325\") = _, false; want true")
	}
	if p.Category != CategoryIndividual {
		t.Errorf("ByID versioned got category %q, want %q", p.Category, CategoryIndividual)
	}

	// Bare ID should still work.
	p2, ok2 := ByID("individual")
	if !ok2 {
		t.Fatal("ByID(\"individual\") = _, false; want true")
	}
	if p2.Category != CategoryIndividual {
		t.Errorf("ByID bare got category %q, want %q", p2.Category, CategoryIndividual)
	}
}

func TestID(t *testing.T) {
	got := ID(CategoryIndividual)
	want := "individual:small:monthly:20260106"
	if got != want {
		t.Errorf("ID(CategoryIndividual) = %q, want %q", got, want)
	}
}

func TestBusinessPlanExists(t *testing.T) {
	p, ok := Get(CategoryBusiness)
	if !ok {
		t.Fatal("CategoryBusiness not found in plans")
	}
	if p.ID != "business:monthly:20260106" {
		t.Errorf("Business plan ID = %q, want %q", p.ID, "business:monthly:20260106")
	}
	if p.Name != "Business" {
		t.Errorf("Business plan Name = %q, want %q", p.Name, "Business")
	}
	if !p.Paid {
		t.Error("Business plan should be Paid=true")
	}
	if p.MonthlyLLMCreditUSD != 500.0 {
		t.Errorf("Business plan MonthlyLLMCreditUSD = %f, want 500.0", p.MonthlyLLMCreditUSD)
	}
}

func TestAllPlansComplete(t *testing.T) {
	all := AllPlans()
	want := []Category{
		CategoryBasic, CategoryBusiness, CategoryFriend, CategoryGrandfathered,
		CategoryIndividual, CategoryRestricted, CategoryTeam, CategoryTrial,
	}
	if len(all) != len(want) {
		t.Fatalf("AllPlans() returned %d plans, want %d", len(all), len(want))
	}
	// Verify sorted alphabetically by name.
	for i := 1; i < len(all); i++ {
		if all[i].Name < all[i-1].Name {
			t.Errorf("AllPlans() not sorted: %q before %q", all[i-1].Name, all[i].Name)
		}
	}
	// Verify all expected categories are present.
	seen := make(map[Category]bool)
	for _, p := range all {
		seen[p.Category] = true
	}
	for _, cat := range want {
		if !seen[cat] {
			t.Errorf("AllPlans() missing category %q", cat)
		}
	}
}

// TestGetPlanForUser verifies the ForUser function.
func TestGetPlanForUser(t *testing.T) {
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)
	oldDate := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	newDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		row  exedb.GetUserPlanDataRow
		want Category
	}{
		{
			name: "friend plan",
			row: exedb.GetUserPlanDataRow{
				PlanID:    new("friend"),
				CreatedAt: &newDate,
			},
			want: CategoryFriend,
		},
		{
			name: "active trial",
			row: exedb.GetUserPlanDataRow{
				PlanID:         new("trial:monthly:20260106"),
				TrialExpiresAt: &future,
				CreatedAt:      &newDate,
			},
			want: CategoryTrial,
		},
		{
			name: "expired trial",
			row: exedb.GetUserPlanDataRow{
				PlanID:         new("trial:monthly:20260106"),
				TrialExpiresAt: &past,
				CreatedAt:      &newDate,
			},
			want: CategoryBasic,
		},
		{
			name: "individual plan",
			row: exedb.GetUserPlanDataRow{
				BillingStatus: "active",
				PlanID:        new("individual:monthly:20260106"),
				CreatedAt:     &newDate,
			},
			want: CategoryIndividual,
		},
		{
			name: "team member",
			row: exedb.GetUserPlanDataRow{
				TeamBillingActive: 1,
				CreatedAt:         &newDate,
			},
			want: CategoryTeam,
		},
		{
			name: "grandfathered user",
			row: exedb.GetUserPlanDataRow{
				CreatedAt: &oldDate,
			},
			want: CategoryGrandfathered,
		},
		{
			name: "basic user",
			row: exedb.GetUserPlanDataRow{
				CreatedAt: &newDate,
			},
			want: CategoryBasic,
		},
		{
			name: "canceled overrides all",
			row: exedb.GetUserPlanDataRow{
				BillingStatus:  "canceled",
				PlanID:         new("trial:monthly:20260106"),
				TrialExpiresAt: &future,
				CreatedAt:      &oldDate,
			},
			want: CategoryBasic,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockQueries{row: tt.row}
			got, err := ForUser(context.Background(), mock, "test-user")
			if err != nil {
				t.Fatalf("ForUser() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("ForUser() = %q, want %q", got, tt.want)
			}
		})
	}
}

// mockQueries implements DataQuerier for testing.
type mockQueries struct {
	row exedb.GetUserPlanDataRow
	err error
}

func (m *mockQueries) GetUserPlanData(ctx context.Context, userID string) (exedb.GetUserPlanDataRow, error) {
	if m.err != nil {
		return exedb.GetUserPlanDataRow{}, m.err
	}
	return m.row, nil
}

func TestCategoryFromProductName(t *testing.T) {
	tests := []struct {
		name    string
		wantCat Category
		wantOK  bool
	}{
		{"Individual", CategoryIndividual, true},
		{"individual", CategoryIndividual, true},
		{"INDIVIDUAL", CategoryIndividual, true},
		{"Team", CategoryTeam, true},
		{"team", CategoryTeam, true},
		{"Business", CategoryBusiness, true},
		{"business", CategoryBusiness, true},
		{"Unknown", Category(""), false},
		{"", Category(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := CategoryFromProductName(tt.name)
			if ok != tt.wantOK {
				t.Errorf("CategoryFromProductName(%q) ok = %v, want %v", tt.name, ok, tt.wantOK)
			}
			if got != tt.wantCat {
				t.Errorf("CategoryFromProductName(%q) = %q, want %q", tt.name, got, tt.wantCat)
			}
		})
	}
}

func TestTierStripePriceInfo(t *testing.T) {
	tests := []struct {
		name          string
		tierID        string
		billingOption string
		want          stripePriceInfo
	}{
		// Individual Small tier
		{
			name:          "individual small monthly",
			tierID:        "individual:small:monthly:20260106",
			billingOption: "monthly",
			want: stripePriceInfo{
				LookupKey: "individual",
				Model:     "subscription",
				Interval:  "monthly",
			},
		},
		{
			name:          "individual medium monthly",
			tierID:        "individual:medium:monthly:20260106",
			billingOption: "monthly",
			want: stripePriceInfo{
				LookupKey: "individual:medium:monthly:20160102",
				Model:     "subscription",
				Interval:  "monthly",
			},
		},
		{
			name:          "individual large monthly",
			tierID:        "individual:large:monthly:20260106",
			billingOption: "monthly",
			want: stripePriceInfo{
				LookupKey: "individual:large:monthly:20160102",
				Model:     "subscription",
				Interval:  "monthly",
			},
		},
		{
			name:          "individual xlarge monthly",
			tierID:        "individual:xlarge:monthly:20260106",
			billingOption: "monthly",
			want: stripePriceInfo{
				LookupKey: "individual:xlarge:monthly:20160102",
				Model:     "subscription",
				Interval:  "monthly",
			},
		},
		// Unknown billing option
		{
			name:          "unknown billing option",
			tierID:        "individual:small:monthly:20260106",
			billingOption: "nonexistent",
			want:          stripePriceInfo{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tier := mustgetTierByID(t, tt.tierID)
			got := tier.StripePrices[tt.billingOption]
			if got != tt.want {
				t.Errorf("tier %q StripePrices[%q] = %+v, want %+v", tt.tierID, tt.billingOption, got, tt.want)
			}
		})
	}
}
