package entitlement

import (
	"testing"
	"time"
)

func TestPlanGrants(t *testing.T) {
	tests := []struct {
		version PlanVersion
		ent     Entitlement
		want    bool
	}{
		{VersionIndividual, CreditPurchase, true},
		{VersionIndividual, AdminOverride, false},
		{VersionFriend, CreditRefresh, true},
		{VersionFriend, CreditPurchase, false},
		{VersionGrandfathered, VMCreate, true},
		{VersionGrandfathered, ComputePurchase, false},
		{VersionInvite, ComputeSpend, true},
		{VersionInvite, ComputeDebt, false},
		{VersionBasic, LLMUse, true},
		{VersionBasic, VMCreate, false},
		{VersionTeam, ComputeDebt, true},
		{VersionTeam, VMCreate, true},
		{VersionTeam, VMConnect, true},
		{VersionTeam, LLMUse, true},
		{VersionTeam, CreditRenew, true},
		{VersionTeam, CreditPurchase, true},
		{VersionTeam, CreditRefresh, true},
		{VersionTeam, ComputeSpend, true},
		{VersionTeam, ComputePurchase, true},
		{VersionTeam, AdminOverride, false},
		{VersionTeam, ComputeOnDemand, false},
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
		LLMUse, CreditPurchase, VMCreate,
		ComputeSpend, AdminOverride,
		{"anything:else", "Made Up"},
	} {
		if !PlanGrants(VersionVIP, ent) {
			t.Errorf("PlanGrants(%q, %q) = false, want true (wildcard)", VersionVIP, ent)
		}
	}
}

func TestPlanGrantsUnknownPlan(t *testing.T) {
	if PlanGrants(PlanVersion("nonexistent"), LLMUse) {
		t.Error("PlanGrants(nonexistent, llm:use) = true, want false")
	}
}

func TestGetPlanVersion(t *testing.T) {
	trial := "trial"
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)
	oldDate := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	newDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		inputs UserPlanInputs
		want   PlanVersion
	}{
		{
			name:   "canceled overrides grandfathered",
			inputs: UserPlanInputs{Category: "no_billing", BillingStatus: "canceled", CreatedAt: &oldDate},
			want:   VersionBasic,
		},
		{
			name:   "canceled overrides trial",
			inputs: UserPlanInputs{Category: "no_billing", BillingStatus: "canceled", BillingExemption: &trial, BillingTrialEndsAt: &future},
			want:   VersionBasic,
		},
		{
			name:   "friend with overrides is VIP",
			inputs: UserPlanInputs{Category: "friend", HasExplicitOverrides: true},
			want:   VersionVIP,
		},
		{
			name:   "friend without overrides",
			inputs: UserPlanInputs{Category: "friend"},
			want:   VersionFriend,
		},
		{
			name:   "has_billing is individual",
			inputs: UserPlanInputs{Category: "has_billing", BillingStatus: "active"},
			want:   VersionIndividual,
		},
		{
			name:   "trial not expired is invite",
			inputs: UserPlanInputs{Category: "no_billing", BillingExemption: &trial, BillingTrialEndsAt: &future},
			want:   VersionInvite,
		},
		{
			name:   "trial expired falls through",
			inputs: UserPlanInputs{Category: "no_billing", BillingExemption: &trial, BillingTrialEndsAt: &past, CreatedAt: &newDate},
			want:   VersionBasic,
		},
		{
			name:   "old user is grandfathered",
			inputs: UserPlanInputs{Category: "no_billing", CreatedAt: &oldDate},
			want:   VersionGrandfathered,
		},
		{
			name:   "new user with nothing is basic",
			inputs: UserPlanInputs{Category: "no_billing", CreatedAt: &newDate},
			want:   VersionBasic,
		},
		{
			name:   "team member covered by billing owner",
			inputs: UserPlanInputs{Category: "no_billing", CreatedAt: &newDate, TeamBillingActive: true},
			want:   VersionTeam,
		},
		{
			name:   "canceled user on team still basic",
			inputs: UserPlanInputs{Category: "no_billing", BillingStatus: "canceled", TeamBillingActive: true},
			want:   VersionBasic,
		},
		{
			name:   "individual with own billing ignores team",
			inputs: UserPlanInputs{Category: "has_billing", BillingStatus: "active", TeamBillingActive: true},
			want:   VersionIndividual,
		},
		{
			name:   "grandfathered user on team stays grandfathered",
			inputs: UserPlanInputs{Category: "no_billing", CreatedAt: &oldDate, TeamBillingActive: true},
			want:   VersionGrandfathered,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetPlanVersion(tt.inputs)
			if got != tt.want {
				t.Errorf("GetPlanVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTeamMemberCanCreateVM exercises the exact bug scenario:
// a team member with no personal billing whose team billing owner covers them
// should resolve to VersionTeam and be granted VMCreate.
func TestTeamMemberCanCreateVM(t *testing.T) {
	newDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	inputs := UserPlanInputs{
		Category:          "no_billing",
		CreatedAt:         &newDate,
		TeamBillingActive: true,
	}
	version := GetPlanVersion(inputs)
	if version != VersionTeam {
		t.Fatalf("GetPlanVersion() = %q, want %q", version, VersionTeam)
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
	version := GetPlanVersion(inputs)
	if version != VersionBasic {
		t.Fatalf("GetPlanVersion() = %q, want %q", version, VersionBasic)
	}
	if PlanGrants(version, VMCreate) {
		t.Errorf("PlanGrants(%q, VMCreate) = true, want false", version)
	}
}

func TestSignupBonusCreditUSD(t *testing.T) {
	tests := []struct {
		version PlanVersion
		want    float64
	}{
		{VersionIndividual, 100.0},
		{VersionVIP, 0},
		{VersionTeam, 0},
		{VersionFriend, 0},
		{VersionGrandfathered, 0},
		{VersionInvite, 0},
		{VersionBasic, 0},
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

func TestAllPlansHaveLLMUse(t *testing.T) {
	for version, plan := range plans {
		if !plan.Entitlements[LLMUse] && !plan.Entitlements[All] {
			t.Errorf("plan %q does not grant llm:use", version)
		}
	}
}
