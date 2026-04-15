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
		"vm:run":          true,
		"disk:resize":     true,
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
