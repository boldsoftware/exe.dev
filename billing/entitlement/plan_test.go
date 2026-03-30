package entitlement

import (
	"context"
	"testing"
	"time"

	"exe.dev/exedb"
)

func TestPlanGrants(t *testing.T) {
	tests := []struct {
		version PlanCategory
		ent     Entitlement
		want    bool
	}{
		// Individual
		{CategoryIndividual, CreditPurchase, true},
		{CategoryIndividual, VMRun, true},
		{CategoryIndividual, VMCreate, true},
		{CategoryIndividual, VMConnect, true},
		{CategoryIndividual, LLMUse, true},

		// Friend
		{CategoryFriend, VMRun, true},
		{CategoryFriend, VMCreate, true},
		{CategoryFriend, VMConnect, true},
		{CategoryFriend, LLMUse, true},
		{CategoryFriend, CreditPurchase, false},

		// Grandfathered
		{CategoryGrandfathered, VMCreate, true},
		{CategoryGrandfathered, VMRun, true},
		{CategoryGrandfathered, CreditPurchase, false},

		// Invite
		{CategoryTrial, VMCreate, true},
		{CategoryTrial, VMRun, true},
		{CategoryTrial, CreditPurchase, false},

		// Basic
		{CategoryBasic, LLMUse, true},
		{CategoryBasic, VMCreate, false},
		{CategoryBasic, VMRun, false},
		{CategoryBasic, CreditPurchase, false},

		// Team
		{CategoryTeam, VMCreate, true},
		{CategoryTeam, VMConnect, true},
		{CategoryTeam, VMRun, true},
		{CategoryTeam, LLMUse, true},
		{CategoryTeam, CreditPurchase, true},

		// Enterprise
		{CategoryEnterprise, CreditPurchase, true},
		{CategoryEnterprise, VMRun, true},
		{CategoryEnterprise, TeamCreate, false},

		// CategoryRestricted — grants nothing
		{CategoryRestricted, LLMUse, false},
		{CategoryRestricted, VMCreate, false},
		{CategoryRestricted, VMRun, false},
		{CategoryRestricted, VMConnect, false},
		{CategoryRestricted, CreditPurchase, false},
	}
	for _, tt := range tests {
		got := PlanGrants(tt.version, tt.ent)
		if got != tt.want {
			t.Errorf("PlanGrants(%q, %q) = %v, want %v", tt.version, tt.ent, got, tt.want)
		}
	}
}

func TestPlanGrantsWildcard(t *testing.T) {
	for _, ent := range []Entitlement{
		LLMUse, CreditPurchase, VMCreate, VMRun, VMConnect,
		{"anything:else", "Made Up"},
	} {
		if !PlanGrants(CategoryVIP, ent) {
			t.Errorf("PlanGrants(%q, %q) = false, want true (wildcard)", CategoryVIP, ent)
		}
	}
}

func TestPlanGrantsUnknownPlan(t *testing.T) {
	if PlanGrants(PlanCategory("nonexistent"), LLMUse) {
		t.Error("PlanGrants(nonexistent, llm:use) = true, want false")
	}
}

func TestGetPlanCategory(t *testing.T) {
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)
	oldDate := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	newDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		inputs UserPlanInputs
		want   PlanCategory
	}{
		{
			name:   "canceled overrides grandfathered",
			inputs: UserPlanInputs{Category: "no_billing", BillingStatus: "canceled", CreatedAt: &oldDate},
			want:   CategoryBasic,
		},
		{
			name:   "canceled overrides trial",
			inputs: UserPlanInputs{Category: "no_billing", BillingStatus: "canceled", PlanID: strPtr("trial:monthly:20260106"), TrialExpiresAt: &future},
			want:   CategoryBasic,
		},
		{
			name:   "friend with overrides is VIP",
			inputs: UserPlanInputs{Category: "friend", HasExplicitOverrides: true},
			want:   CategoryVIP,
		},
		{
			name:   "friend without overrides",
			inputs: UserPlanInputs{Category: "friend"},
			want:   CategoryFriend,
		},
		{
			name:   "has_billing is individual",
			inputs: UserPlanInputs{Category: "has_billing", BillingStatus: "active"},
			want:   CategoryIndividual,
		},
		{
			name:   "trial not expired is trial",
			inputs: UserPlanInputs{Category: "no_billing", PlanID: strPtr("trial:monthly:20260106"), TrialExpiresAt: &future},
			want:   CategoryTrial,
		},
		{
			name:   "trial expired falls through",
			inputs: UserPlanInputs{Category: "no_billing", PlanID: strPtr("trial:monthly:20260106"), TrialExpiresAt: &past, CreatedAt: &newDate},
			want:   CategoryBasic,
		},
		{
			name:   "old user is grandfathered",
			inputs: UserPlanInputs{Category: "no_billing", CreatedAt: &oldDate},
			want:   CategoryGrandfathered,
		},
		{
			name:   "new user with nothing is basic",
			inputs: UserPlanInputs{Category: "no_billing", CreatedAt: &newDate},
			want:   CategoryBasic,
		},
		{
			name:   "team member covered by billing owner",
			inputs: UserPlanInputs{Category: "no_billing", CreatedAt: &newDate, TeamBillingActive: true},
			want:   CategoryTeam,
		},
		{
			name:   "canceled user on team still basic",
			inputs: UserPlanInputs{Category: "no_billing", BillingStatus: "canceled", TeamBillingActive: true},
			want:   CategoryBasic,
		},
		{
			name:   "individual with own billing on team resolves to team",
			inputs: UserPlanInputs{Category: "has_billing", BillingStatus: "active", TeamBillingActive: true},
			want:   CategoryTeam,
		},
		{
			name:   "grandfathered user on team resolves to team",
			inputs: UserPlanInputs{Category: "no_billing", CreatedAt: &oldDate, TeamBillingActive: true},
			want:   CategoryTeam,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetPlanCategory(tt.inputs)
			if got != tt.want {
				t.Errorf("GetPlanCategory() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTeamMemberCanCreateVM exercises the exact bug scenario:
// a team member with no personal billing whose team billing owner covers them
// should resolve to CategoryTeam and be granted VMCreate.
func TestTeamMemberCanCreateVM(t *testing.T) {
	newDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	inputs := UserPlanInputs{
		Category:          "no_billing",
		CreatedAt:         &newDate,
		TeamBillingActive: true,
	}
	version := GetPlanCategory(inputs)
	if version != CategoryTeam {
		t.Fatalf("GetPlanCategory() = %q, want %q", version, CategoryTeam)
	}
	if !PlanGrants(version, VMCreate) {
		t.Errorf("PlanGrants(%q, VMCreate) = false, want true", version)
	}
}

// TestTeamMemberDeniedWithoutBillingOwner verifies that a team member
// without billing owner coverage falls through to Basic and is denied VMCreate.
func TestTeamMemberDeniedWithoutBillingOwner(t *testing.T) {
	newDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	inputs := UserPlanInputs{
		Category:          "no_billing",
		CreatedAt:         &newDate,
		TeamBillingActive: false,
	}
	version := GetPlanCategory(inputs)
	if version != CategoryBasic {
		t.Fatalf("GetPlanCategory() = %q, want %q", version, CategoryBasic)
	}
	if PlanGrants(version, VMCreate) {
		t.Errorf("PlanGrants(%q, VMCreate) = true, want false", version)
	}
}

func TestSignupBonusCreditUSD(t *testing.T) {
	tests := []struct {
		version PlanCategory
		want    float64
	}{
		{CategoryIndividual, 100.0},
		{CategoryVIP, 0},
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
		if p.Quotas.SignupBonusCreditUSD != tt.want {
			t.Errorf("plan %q SignupBonusCreditUSD = %v, want %v", tt.version, p.Quotas.SignupBonusCreditUSD, tt.want)
		}
	}
}

// TestAllPlansHaveLLMUse verifies all plans except Restricted grant llm:use.
func TestAllPlansHaveLLMUse(t *testing.T) {
	for version, plan := range plans {
		if version == CategoryRestricted {
			// Restricted grants nothing — explicitly should NOT have LLMUse.
			if plan.Entitlements[LLMUse] || plan.Entitlements[All] {
				t.Errorf("plan %q should not grant llm:use", version)
			}
			continue
		}
		if !plan.Entitlements[LLMUse] && !plan.Entitlements[All] {
			t.Errorf("plan %q does not grant llm:use", version)
		}
	}
}

// TestRestrictedPlanGrantsNothing verifies the Restricted plan has an empty entitlements map.
func TestRestrictedPlanGrantsNothing(t *testing.T) {
	p, ok := plans[CategoryRestricted]
	if !ok {
		t.Fatal("CategoryRestricted not found in plans")
	}
	for ent, granted := range p.Entitlements {
		if granted {
			t.Errorf("CategoryRestricted grants %q, want nothing", ent.ID)
		}
	}
}

// TestVMRunGranted verifies VMRun is granted to the right plans.
func TestVMRunGranted(t *testing.T) {
	shouldGrant := []PlanCategory{CategoryVIP, CategoryTeam, CategoryIndividual, CategoryFriend, CategoryGrandfathered, CategoryTrial}
	shouldDeny := []PlanCategory{CategoryBasic, CategoryRestricted}

	for _, v := range shouldGrant {
		if !PlanGrants(v, VMRun) {
			t.Errorf("PlanGrants(%q, VMRun) = false, want true", v)
		}
	}
	for _, v := range shouldDeny {
		if PlanGrants(v, VMRun) {
			t.Errorf("PlanGrants(%q, VMRun) = true, want false", v)
		}
	}
}

// TestAllEntitlements verifies AllEntitlements returns all concrete entitlements
// (excluding the All wildcard) and that the list is stable.
func TestAllEntitlements(t *testing.T) {
	all := AllEntitlements()
	if len(all) == 0 {
		t.Fatal("AllEntitlements() returned empty slice")
	}

	// Should not contain the wildcard.
	for _, e := range all {
		if e.ID == "*" {
			t.Error("AllEntitlements() should not contain the All wildcard")
		}
	}

	// Should contain all known concrete entitlements.
	want := map[string]bool{
		"llm:use":         true,
		"credit:purchase": true,
		"invite:request":  true,
		"team:create":     true,
		"vm:create":       true,
		"vm:connect":      true,
		"vm:run":          true,
	}
	got := make(map[string]bool)
	for _, e := range all {
		got[e.ID] = true
	}
	for id := range want {
		if !got[id] {
			t.Errorf("AllEntitlements() missing %q", id)
		}
	}
	for id := range got {
		if !want[id] {
			t.Errorf("AllEntitlements() has unexpected %q", id)
		}
	}
}

func TestParsePlanID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantPlan PlanCategory
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
			input:    "team:yearly:20260601",
			wantPlan: CategoryTeam,
			wantInt:  "yearly",
			wantVer:  "20260601",
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
			name:     "bare vip",
			input:    "vip",
			wantPlan: CategoryVIP,
			wantInt:  "",
			wantVer:  "",
		},
		{
			name:     "empty string",
			input:    "",
			wantPlan: PlanCategory(""),
			wantInt:  "",
			wantVer:  "",
		},
		{
			name:     "two parts treated as bare",
			input:    "individual:monthly",
			wantPlan: PlanCategory("individual:monthly"),
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
			plan, interval, ver := ParsePlanID(tt.input)
			if plan != tt.wantPlan {
				t.Errorf("ParsePlanID(%q) plan = %q, want %q", tt.input, plan, tt.wantPlan)
			}
			if interval != tt.wantInt {
				t.Errorf("ParsePlanID(%q) interval = %q, want %q", tt.input, interval, tt.wantInt)
			}
			if ver != tt.wantVer {
				t.Errorf("ParsePlanID(%q) version = %q, want %q", tt.input, ver, tt.wantVer)
			}
		})
	}
}

func TestBasePlan(t *testing.T) {
	tests := []struct {
		input string
		want  PlanCategory
	}{
		{"individual:monthly:20260325", CategoryIndividual},
		{"basic:monthly:20260101", CategoryBasic},
		{"individual", CategoryIndividual},
		{"basic", CategoryBasic},
		{"friend", CategoryFriend},
		{"vip", CategoryVIP},
	}
	for _, tt := range tests {
		got := BasePlan(tt.input)
		if got != tt.want {
			t.Errorf("BasePlan(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGetPlanByIDVersioned(t *testing.T) {
	// Versioned ID should resolve to the base plan.
	p, ok := GetPlanByID("individual:monthly:20260325")
	if !ok {
		t.Fatal("GetPlanByID(\"individual:monthly:20260325\") = _, false; want true")
	}
	if p.Category != CategoryIndividual {
		t.Errorf("GetPlanByID versioned got category %q, want %q", p.Category, CategoryIndividual)
	}
	if p.LLMGatewayCategory != "has_billing" {
		t.Errorf("GetPlanByID versioned got category %q, want %q", p.LLMGatewayCategory, "has_billing")
	}

	// Bare ID should still work.
	p2, ok2 := GetPlanByID("individual")
	if !ok2 {
		t.Fatal("GetPlanByID(\"individual\") = _, false; want true")
	}
	if p2.Category != CategoryIndividual {
		t.Errorf("GetPlanByID bare got category %q, want %q", p2.Category, CategoryIndividual)
	}
}

func TestPlanID(t *testing.T) {
	got := PlanID(CategoryIndividual)
	want := "individual:monthly:20260106"
	if got != want {
		t.Errorf("PlanID(CategoryIndividual) = %q, want %q", got, want)
	}
}

func TestPlanGrantsWithVersionedID(t *testing.T) {
	// Verify that using BasePlan on a versioned ID gives correct entitlements.
	version := BasePlan("individual:monthly:20260325")
	if !PlanGrants(version, VMCreate) {
		t.Error("PlanGrants with versioned individual should grant VMCreate")
	}
	if !PlanGrants(version, CreditPurchase) {
		t.Error("PlanGrants with versioned individual should grant CreditPurchase")
	}

	version2 := BasePlan("basic:monthly:20260325")
	if PlanGrants(version2, VMCreate) {
		t.Error("PlanGrants with versioned basic should not grant VMCreate")
	}
	if !PlanGrants(version2, LLMUse) {
		t.Error("PlanGrants with versioned basic should grant LLMUse")
	}
}

func TestEnterprisePlanExists(t *testing.T) {
	p, ok := GetPlan(CategoryEnterprise)
	if !ok {
		t.Fatal("CategoryEnterprise not found in plans")
	}
	if p.ID != "enterprise:monthly:20260106" {
		t.Errorf("Enterprise plan ID = %q, want %q", p.ID, "enterprise:monthly:20260106")
	}
	if p.Name != "Enterprise" {
		t.Errorf("Enterprise plan Name = %q, want %q", p.Name, "Enterprise")
	}
	if !p.Paid {
		t.Error("Enterprise plan should be Paid=true")
	}
	if p.LLMGatewayCategory != "has_billing" {
		t.Errorf("Enterprise LLMGatewayCategory = %q, want %q", p.LLMGatewayCategory, "has_billing")
	}
}

func TestEnterprisePlanGrants(t *testing.T) {
	shouldGrant := []Entitlement{LLMUse, CreditPurchase, InviteRequest, VMCreate, VMConnect, VMRun}
	for _, ent := range shouldGrant {
		if !PlanGrants(CategoryEnterprise, ent) {
			t.Errorf("PlanGrants(CategoryEnterprise, %q) = false, want true", ent.ID)
		}
	}
	if PlanGrants(CategoryEnterprise, TeamCreate) {
		t.Error("PlanGrants(CategoryEnterprise, TeamCreate) = true, want false")
	}
}

func TestAllPlansIncludesEnterprise(t *testing.T) {
	all := AllPlans()
	found := false
	vipIdx, entIdx, teamIdx := -1, -1, -1
	for i, p := range all {
		switch p.Category {
		case CategoryVIP:
			vipIdx = i
		case CategoryEnterprise:
			entIdx = i
			found = true
		case CategoryTeam:
			teamIdx = i
		}
	}
	if !found {
		t.Fatal("CategoryEnterprise not found in AllPlans()")
	}
	if vipIdx >= entIdx {
		t.Errorf("VIP (idx=%d) should come before Enterprise (idx=%d)", vipIdx, entIdx)
	}
	if entIdx >= teamIdx {
		t.Errorf("Enterprise (idx=%d) should come before Team (idx=%d)", entIdx, teamIdx)
	}
}

// TestGetPlanForUser verifies the GetPlanForUser function.
func TestGetPlanForUser(t *testing.T) {
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)
	oldDate := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	newDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		row  exedb.GetUserPlanDataRow
		want PlanCategory
	}{
		{
			name: "friend plan",
			row: exedb.GetUserPlanDataRow{
				Category:  "friend",
				PlanID:    strPtr("friend"),
				CreatedAt: &newDate,
			},
			want: CategoryFriend,
		},
		{
			name: "vip plan",
			row: exedb.GetUserPlanDataRow{
				Category:             "friend",
				PlanID:               strPtr("vip:monthly:20260106"),
				HasExplicitOverrides: 1,
				CreatedAt:            &newDate,
			},
			want: CategoryVIP,
		},
		{
			name: "active trial",
			row: exedb.GetUserPlanDataRow{
				Category:       "no_billing",
				PlanID:         strPtr("trial:monthly:20260106"),
				TrialExpiresAt: &future,
				CreatedAt:      &newDate,
			},
			want: CategoryTrial,
		},
		{
			name: "expired trial",
			row: exedb.GetUserPlanDataRow{
				Category:       "no_billing",
				PlanID:         strPtr("trial:monthly:20260106"),
				TrialExpiresAt: &past,
				CreatedAt:      &newDate,
			},
			want: CategoryBasic,
		},
		{
			name: "individual plan",
			row: exedb.GetUserPlanDataRow{
				Category:      "has_billing",
				BillingStatus: "active",
				PlanID:        strPtr("individual:monthly:20260106"),
				CreatedAt:     &newDate,
			},
			want: CategoryIndividual,
		},
		{
			name: "team member",
			row: exedb.GetUserPlanDataRow{
				Category:          "no_billing",
				TeamBillingActive: 1,
				CreatedAt:         &newDate,
			},
			want: CategoryTeam,
		},
		{
			name: "grandfathered user",
			row: exedb.GetUserPlanDataRow{
				Category:  "no_billing",
				CreatedAt: &oldDate,
			},
			want: CategoryGrandfathered,
		},
		{
			name: "basic user",
			row: exedb.GetUserPlanDataRow{
				Category:  "no_billing",
				CreatedAt: &newDate,
			},
			want: CategoryBasic,
		},
		{
			name: "canceled overrides all",
			row: exedb.GetUserPlanDataRow{
				Category:       "has_billing",
				BillingStatus:  "canceled",
				PlanID:         strPtr("trial:monthly:20260106"),
				TrialExpiresAt: &future,
				CreatedAt:      &oldDate,
			},
			want: CategoryBasic,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockQueries{row: tt.row}
			got, err := GetPlanForUser(context.Background(), mock, "test-user")
			if err != nil {
				t.Fatalf("GetPlanForUser() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("GetPlanForUser() = %q, want %q", got, tt.want)
			}
		})
	}
}

// mockQueries implements PlanDataQuerier for testing.
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

func strPtr(s string) *string {
	return &s
}

func TestPlanStripePriceInfo(t *testing.T) {
	tests := []struct {
		name          string
		plan          PlanCategory
		billingOption string
		want          StripePriceInfo
	}{
		// Individual plan tests
		{
			name:          "individual monthly",
			plan:          CategoryIndividual,
			billingOption: "monthly",
			want: StripePriceInfo{
				LookupKey: "individual",
				Model:     "subscription",
				Interval:  "monthly",
			},
		},
		{
			name:          "individual annual",
			plan:          CategoryIndividual,
			billingOption: "annual",
			want: StripePriceInfo{
				LookupKey: "individual:annual:20260106",
				Model:     "subscription",
				Interval:  "annual",
			},
		},
		{
			name:          "individual usage-disk",
			plan:          CategoryIndividual,
			billingOption: "usage-disk",
			want: StripePriceInfo{
				LookupKey: "individual:usage-disk:20260106",
				Model:     "metered",
				Interval:  "",
			},
		},
		{
			name:          "individual usage-bandwidth",
			plan:          CategoryIndividual,
			billingOption: "usage-bandwidth",
			want: StripePriceInfo{
				LookupKey: "individual:usage-bandwidth:20260106",
				Model:     "metered",
				Interval:  "",
			},
		},
		// Team plan tests
		{
			name:          "team monthly",
			plan:          CategoryTeam,
			billingOption: "monthly",
			want: StripePriceInfo{
				LookupKey: "team:monthly:20260106",
				Model:     "subscription",
				Interval:  "monthly",
			},
		},
		{
			name:          "team annual",
			plan:          CategoryTeam,
			billingOption: "annual",
			want: StripePriceInfo{
				LookupKey: "team:annual:20260106",
				Model:     "subscription",
				Interval:  "annual",
			},
		},
		{
			name:          "team usage-disk",
			plan:          CategoryTeam,
			billingOption: "usage-disk",
			want: StripePriceInfo{
				LookupKey: "team:usage-disk:20260106",
				Model:     "metered",
				Interval:  "",
			},
		},
		{
			name:          "team usage-bandwidth",
			plan:          CategoryTeam,
			billingOption: "usage-bandwidth",
			want: StripePriceInfo{
				LookupKey: "team:usage-bandwidth:20260106",
				Model:     "metered",
				Interval:  "",
			},
		},
		// Unknown plan category
		{
			name:          "unknown plan category",
			plan:          PlanCategory("nonexistent"),
			billingOption: "monthly",
			want:          StripePriceInfo{},
		},
		// Unknown billing option
		{
			name:          "individual unknown billing option",
			plan:          CategoryIndividual,
			billingOption: "nonexistent",
			want:          StripePriceInfo{},
		},
		{
			name:          "team unknown billing option",
			plan:          CategoryTeam,
			billingOption: "quarterly",
			want:          StripePriceInfo{},
		},
		// Plans without StripePrices
		{
			name:          "VIP plan has no stripe prices",
			plan:          CategoryVIP,
			billingOption: "monthly",
			want:          StripePriceInfo{},
		},
		{
			name:          "Friend plan has no stripe prices",
			plan:          CategoryFriend,
			billingOption: "monthly",
			want:          StripePriceInfo{},
		},
		{
			name:          "Grandfathered plan has no stripe prices",
			plan:          CategoryGrandfathered,
			billingOption: "monthly",
			want:          StripePriceInfo{},
		},
		{
			name:          "Trial plan has no stripe prices",
			plan:          CategoryTrial,
			billingOption: "monthly",
			want:          StripePriceInfo{},
		},
		{
			name:          "Basic plan has no stripe prices",
			plan:          CategoryBasic,
			billingOption: "monthly",
			want:          StripePriceInfo{},
		},
		{
			name:          "Restricted plan has no stripe prices",
			plan:          CategoryRestricted,
			billingOption: "monthly",
			want:          StripePriceInfo{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PlanStripePriceInfo(tt.plan, tt.billingOption)
			if got != tt.want {
				t.Errorf("PlanStripePriceInfo(%q, %q) = %+v, want %+v", tt.plan, tt.billingOption, got, tt.want)
			}
		})
	}
}
