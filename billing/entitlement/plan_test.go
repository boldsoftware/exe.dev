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
		ComputeSpend, AdminOverride, Entitlement{"anything:else", "Made Up"},
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

func TestAllPlansHaveLLMUse(t *testing.T) {
	for version, plan := range plans {
		if !plan.Entitlements[LLMUse] && !plan.Entitlements[All] {
			t.Errorf("plan %q does not grant llm:use", version)
		}
	}
}
