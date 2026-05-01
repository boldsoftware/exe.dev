package plan

import "testing"

// TestAllPlansHaveLLMUse verifies all plans except Restricted grant llm:use.
func TestAllPlansHaveLLMUse(t *testing.T) {
	for version, p := range plans {
		if version == CategoryRestricted {
			// Restricted grants nothing — explicitly should NOT have LLMUse.
			if p.Entitlements[LLMUse] || p.Entitlements[All] {
				t.Errorf("plan %q should not grant llm:use", version)
			}
			continue
		}
		if !p.Entitlements[LLMUse] && !p.Entitlements[All] {
			t.Errorf("plan %q does not grant llm:use", version)
		}
	}
}

// TestRestrictedPlanEntitlements verifies the Restricted plan grants only account:delete.
func TestRestrictedPlanEntitlements(t *testing.T) {
	p, ok := plans[CategoryRestricted]
	if !ok {
		t.Fatal("CategoryRestricted not found in plans")
	}
	if !p.Entitlements[AccountDelete] {
		t.Error("CategoryRestricted should grant account:delete")
	}
	for ent, granted := range p.Entitlements {
		if granted && ent != AccountDelete {
			t.Errorf("CategoryRestricted grants %q, want only account:delete", ent.ID)
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
		"llm:use":             true,
		"credit:purchase":     true,
		"invite:request":      true,
		"invite:claim":        true,
		"team:create":         true,
		"vm:create":           true,
		"vm:run":              true,
		"vm:resize":           true,
		"billing:selfserve":   true,
		"billing:seats":       true,
		"billing:trialaccess": true,
		"account:delete":      true,
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

// TestVMResizeEntitlementByPlan verifies which plans grant VMResize.
// Plans with MaxDisk > 0 should have it; basic and restricted should not.
func TestVMResizeEntitlementByPlan(t *testing.T) {
	wantVMResize := map[Category]bool{
		CategoryBusiness:      true,
		CategoryTeam:          true,
		CategoryIndividual:    true,
		CategoryFriend:        true,
		CategoryGrandfathered: true,
		CategoryTrial:         true,
		CategoryBasic:         false,
		CategoryRestricted:    false,
	}

	for cat, want := range wantVMResize {
		p, ok := plans[cat]
		if !ok {
			t.Errorf("plan %q not found in plans map", cat)
			continue
		}
		got := Grants(p.ID, VMResize)
		if got != want {
			t.Errorf("plan %q: Grants(VMResize) = %v, want %v", cat, got, want)
		}
	}
}

// TestBillingSelfServeEntitlementByPlan verifies which plans grant BillingSelfServe.
// Individual, Trial, and Basic can access the billing/update flow; others cannot.
func TestBillingSelfServeEntitlementByPlan(t *testing.T) {
	wantSelfServe := map[Category]bool{
		CategoryBusiness:      false,
		CategoryTeam:          false,
		CategoryIndividual:    true,
		CategoryFriend:        false,
		CategoryGrandfathered: false,
		CategoryTrial:         true,
		CategoryBasic:         true,
		CategoryRestricted:    false,
	}

	for cat, want := range wantSelfServe {
		p, ok := plans[cat]
		if !ok {
			t.Errorf("plan %q not found in plans map", cat)
			continue
		}
		got := Grants(p.ID, BillingSelfServe)
		if got != want {
			t.Errorf("plan %q: Grants(BillingSelfServe) = %v, want %v", cat, got, want)
		}
	}
}

// TestAccountDeleteEntitlementByPlan verifies only Basic and Restricted grant account:delete.
func TestAccountDeleteEntitlementByPlan(t *testing.T) {
	wantDelete := map[Category]bool{
		CategoryBusiness:      false,
		CategoryTeam:          false,
		CategoryIndividual:    false,
		CategoryFriend:        false,
		CategoryGrandfathered: false,
		CategoryTrial:         true,
		CategoryBasic:         true,
		CategoryRestricted:    true,
	}

	for cat, want := range wantDelete {
		p, ok := plans[cat]
		if !ok {
			t.Errorf("plan %q not found in plans map", cat)
			continue
		}
		got := Grants(p.ID, AccountDelete)
		if got != want {
			t.Errorf("plan %q: Grants(AccountDelete) = %v, want %v", cat, got, want)
		}
	}
}

// TestInviteClaimEntitlementByPlan verifies Basic and Grandfathered grant invite:claim.
func TestInviteClaimEntitlementByPlan(t *testing.T) {
	wantClaim := map[Category]bool{
		CategoryBusiness:      false,
		CategoryTeam:          false,
		CategoryIndividual:    false,
		CategoryFriend:        false,
		CategoryGrandfathered: true,
		CategoryTrial:         false,
		CategoryBasic:         true,
		CategoryRestricted:    false,
	}

	for cat, want := range wantClaim {
		p, ok := plans[cat]
		if !ok {
			t.Errorf("plan %q not found in plans map", cat)
			continue
		}
		got := Grants(p.ID, InviteClaim)
		if got != want {
			t.Errorf("plan %q: Grants(InviteClaim) = %v, want %v", cat, got, want)
		}
	}
}
