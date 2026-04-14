package entitlement

import "testing"

// mustGetTierByID is a test helper that fatals if the tier ID is unknown.
func mustGetTierByID(t *testing.T, id string) Tier {
	t.Helper()
	tier, err := GetTierByID(id)
	if err != nil {
		t.Fatalf("GetTierByID(%q): %v", id, err)
	}
	return tier
}

func TestParseTierID(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantCategory PlanCategory
		wantTier     string
		wantInterval string
		wantVersion  string
	}{
		{
			name:         "4-part individual small",
			input:        "individual:small:monthly:20260601",
			wantCategory: CategoryIndividual,
			wantTier:     "small",
			wantInterval: "monthly",
			wantVersion:  "20260601",
		},
		{
			name:         "4-part individual xlarge",
			input:        "individual:xlarge:monthly:20260601",
			wantCategory: CategoryIndividual,
			wantTier:     "xlarge",
			wantInterval: "monthly",
			wantVersion:  "20260601",
		},
		{
			name:         "4-part vip default",
			input:        "vip:default:monthly:20260601",
			wantCategory: CategoryVIP,
			wantTier:     "default",
			wantInterval: "monthly",
			wantVersion:  "20260601",
		},
		{
			name:         "3-part legacy individual",
			input:        "individual:monthly:20260106",
			wantCategory: CategoryIndividual,
			wantTier:     "",
			wantInterval: "monthly",
			wantVersion:  "20260106",
		},
		{
			name:         "bare individual",
			input:        "individual",
			wantCategory: CategoryIndividual,
			wantTier:     "",
			wantInterval: "",
			wantVersion:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat, tier, interval, version := parseTierID(tt.input)
			if cat != tt.wantCategory {
				t.Errorf("parseTierID(%q) category = %q, want %q", tt.input, cat, tt.wantCategory)
			}
			if tier != tt.wantTier {
				t.Errorf("parseTierID(%q) tier = %q, want %q", tt.input, tier, tt.wantTier)
			}
			if interval != tt.wantInterval {
				t.Errorf("parseTierID(%q) interval = %q, want %q", tt.input, interval, tt.wantInterval)
			}
			if version != tt.wantVersion {
				t.Errorf("parseTierID(%q) version = %q, want %q", tt.input, version, tt.wantVersion)
			}
		})
	}
}

func TestGetTierByID(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		wantID   string
		wantName string
		wantCat  PlanCategory
	}{
		{
			name:     "individual small",
			id:       "individual:small:monthly:20260601",
			wantID:   "individual:small:monthly:20260601",
			wantName: "Small",
			wantCat:  CategoryIndividual,
		},
		{
			name:     "individual medium",
			id:       "individual:medium:monthly:20260601",
			wantID:   "individual:medium:monthly:20260601",
			wantName: "Medium",
			wantCat:  CategoryIndividual,
		},
		{
			name:     "individual large",
			id:       "individual:large:monthly:20260601",
			wantID:   "individual:large:monthly:20260601",
			wantName: "Large",
			wantCat:  CategoryIndividual,
		},
		{
			name:     "individual xlarge",
			id:       "individual:xlarge:monthly:20260601",
			wantID:   "individual:xlarge:monthly:20260601",
			wantName: "XLarge",
			wantCat:  CategoryIndividual,
		},
		{
			// Legacy 3-part individual IDs map to the Small tier.
			name:     "legacy individual monthly",
			id:       "individual:monthly:20260106",
			wantID:   "individual:small:monthly:20260601",
			wantName: "Small",
			wantCat:  CategoryIndividual,
		},
		{
			name:     "vip default",
			id:       "vip:default:monthly:20260601",
			wantID:   "vip:default:monthly:20260601",
			wantName: "Default",
			wantCat:  CategoryVIP,
		},
		{
			// Legacy bare vip ID maps to default vip tier.
			name:     "bare vip",
			id:       "vip",
			wantID:   "vip:default:monthly:20260601",
			wantName: "Default",
			wantCat:  CategoryVIP,
		},
		{
			// Bare "individual" (used by test helpers / syncAccountPlan) maps
			// to the plan's DefaultTier (small).
			name:     "bare individual",
			id:       "individual",
			wantID:   "individual:small:monthly:20260601",
			wantName: "Small",
			wantCat:  CategoryIndividual,
		},
		{
			// Bare "basic" maps to the basic default tier.
			name:     "bare basic",
			id:       "basic",
			wantID:   "basic:default:monthly:20260601",
			wantName: "Default",
			wantCat:  CategoryBasic,
		},
		{
			// Bare "friend" maps to the friend default tier.
			name:     "bare friend",
			id:       "friend",
			wantID:   "friend:default:monthly:20260601",
			wantName: "Default",
			wantCat:  CategoryFriend,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tier, err := GetTierByID(tt.id)
			if err != nil {
				t.Fatalf("GetTierByID(%q): %v", tt.id, err)
			}
			if tier.ID != tt.wantID {
				t.Errorf("GetTierByID(%q).ID = %q, want %q", tt.id, tier.ID, tt.wantID)
			}
			if tier.Name != tt.wantName {
				t.Errorf("GetTierByID(%q).Name = %q, want %q", tt.id, tier.Name, tt.wantName)
			}
			if tier.PlanCategory != tt.wantCat {
				t.Errorf("GetTierByID(%q).PlanCategory = %q, want %q", tt.id, tier.PlanCategory, tt.wantCat)
			}
		})
	}
}

func TestGetTierByIDUnknown(t *testing.T) {
	_, err := GetTierByID("totally:bogus:id")
	if err == nil {
		t.Error("GetTierByID with unknown ID should return an error")
	}
}

func TestGrantsEntitlementUnknownPlanID(t *testing.T) {
	if GrantsEntitlement("totally:bogus:id", VMCreate) {
		t.Error("GrantsEntitlement with unknown plan ID should return false")
	}
}

func TestTierGrantsInheritance(t *testing.T) {
	// nil Entitlements → inherit from plan.
	small := mustGetTierByID(t, "individual:small:monthly:20260601")
	if small.Entitlements != nil {
		t.Fatal("small tier should have nil Entitlements (inherit from plan)")
	}
	if !tierGrants(small, VMCreate) {
		t.Error("individual:small should inherit VMCreate from Individual plan")
	}
	if !tierGrants(small, TeamCreate) {
		t.Error("individual:small should inherit TeamCreate from Individual plan")
	}

	// Non-nil Entitlements → use override (ignores plan entitlements).
	override := map[Entitlement]bool{DiskResize: true}
	tierWithOverride := Tier{
		ID:           "test:custom:monthly:20260601",
		PlanCategory: CategoryIndividual,
		Name:         "Custom",
		StripePrices: map[string]StripePriceInfo{},

		Entitlements: &override,
	}
	if !tierGrants(tierWithOverride, DiskResize) {
		t.Error("overridden tier should grant DiskResize")
	}
	if tierGrants(tierWithOverride, VMCreate) {
		t.Error("overridden tier should not grant VMCreate (not in override set)")
	}
	if tierGrants(tierWithOverride, TeamCreate) {
		t.Error("overridden tier should not grant TeamCreate (not in override set)")
	}
}

func TestTierGrants(t *testing.T) {
	small := mustGetTierByID(t, "individual:small:monthly:20260601")
	if !tierGrants(small, VMCreate) {
		t.Error("individual:small should grant VMCreate")
	}
	if !tierGrants(small, LLMUse) {
		t.Error("individual:small should grant LLMUse")
	}

	vip := mustGetTierByID(t, "vip:default:monthly:20260601")
	// VIP plan has All wildcard — tierGrants should respect it.
	if !tierGrants(vip, VMCreate) {
		t.Error("vip:default should grant VMCreate via wildcard")
	}
	if !tierGrants(vip, Entitlement{"anything:new", "New"}) {
		t.Error("vip:default should grant unknown entitlements via wildcard")
	}
}

func TestGrantsEntitlement(t *testing.T) {
	// 4-part individual tier ID
	if !GrantsEntitlement("individual:small:monthly:20260601", VMCreate) {
		t.Error("individual:small should grant VMCreate")
	}
	if !GrantsEntitlement("individual:xlarge:monthly:20260601", TeamCreate) {
		t.Error("individual:xlarge should grant TeamCreate (inherited from plan)")
	}

	// 3-part legacy individual ID → falls back to small tier → individual plan entitlements
	if !GrantsEntitlement("individual:monthly:20260106", VMCreate) {
		t.Error("legacy individual plan ID should grant VMCreate")
	}

	// Bare "individual" (test helpers and syncAccountPlan insert this)
	if !GrantsEntitlement("individual", VMCreate) {
		t.Error("bare 'individual' should grant VMCreate")
	}

	// VIP wildcard
	if !GrantsEntitlement("vip:default:monthly:20260601", Entitlement{"anything:new", "New"}) {
		t.Error("vip should grant unknown entitlements via wildcard")
	}

	// Basic plan should not grant VMCreate
	if GrantsEntitlement("basic:default:monthly:20260601", VMCreate) {
		t.Error("basic plan should not grant VMCreate")
	}

	// Restricted plan grants nothing
	if GrantsEntitlement("restricted", VMCreate) {
		t.Error("restricted plan should not grant VMCreate")
	}
}

func TestIndividualTierQuotas(t *testing.T) {
	expected := []struct {
		id     string
		pool   PoolSize
		disk   uint64
		maxVMs int
	}{
		{"individual:small:monthly:20260601", PoolSmall, 25 * GB, 50},
		{"individual:medium:monthly:20260601", PoolMedium, 25 * GB, 50},
		{"individual:large:monthly:20260601", PoolLarge, 25 * GB, 50},
		{"individual:xlarge:monthly:20260601", PoolXLarge, 25 * GB, 50},
	}
	for _, e := range expected {
		t.Run(e.id, func(t *testing.T) {
			tier := mustGetTierByID(t, e.id)
			if tier.Quotas.PoolSize != e.pool {
				t.Errorf("PoolSize = %+v, want %+v", tier.Quotas.PoolSize, e.pool)
			}
			if tier.Quotas.MaxDisk != e.disk {
				t.Errorf("MaxDisk = %d, want %d", tier.Quotas.MaxDisk, e.disk)
			}
			if tier.Quotas.MaxUserVMs != e.maxVMs {
				t.Errorf("MaxUserVMs = %d, want %d", tier.Quotas.MaxUserVMs, e.maxVMs)
			}
		})
	}
}

func TestPoolSizes(t *testing.T) {
	if PoolSmall.MaxMemory != 8*GB {
		t.Errorf("PoolSmall.MaxMemory = %d, want %d", PoolSmall.MaxMemory, 8*GB)
	}
	if PoolSmall.MaxCPUs != 2 {
		t.Errorf("PoolSmall.MaxCPUs = %d, want 2", PoolSmall.MaxCPUs)
	}
	if PoolMedium.MaxMemory != 16*GB {
		t.Errorf("PoolMedium.MaxMemory = %d, want %d", PoolMedium.MaxMemory, 16*GB)
	}
	if PoolMedium.MaxCPUs != 4 {
		t.Errorf("PoolMedium.MaxCPUs = %d, want 4", PoolMedium.MaxCPUs)
	}
	if PoolLarge.MaxMemory != 32*GB {
		t.Errorf("PoolLarge.MaxMemory = %d, want %d", PoolLarge.MaxMemory, 32*GB)
	}
	if PoolLarge.MaxCPUs != 8 {
		t.Errorf("PoolLarge.MaxCPUs = %d, want 8", PoolLarge.MaxCPUs)
	}
	if PoolXLarge.MaxMemory != 64*GB {
		t.Errorf("PoolXLarge.MaxMemory = %d, want %d", PoolXLarge.MaxMemory, 64*GB)
	}
	if PoolXLarge.MaxCPUs != 16 {
		t.Errorf("PoolXLarge.MaxCPUs = %d, want 16", PoolXLarge.MaxCPUs)
	}
}

func TestTiersByCategory(t *testing.T) {
	individualTiers := TiersByCategory(CategoryIndividual)
	if len(individualTiers) != 4 {
		t.Errorf("TiersByCategory(Individual) len = %d, want 4", len(individualTiers))
	}

	// All tiers should be individual category.
	for _, tier := range individualTiers {
		if tier.PlanCategory != CategoryIndividual {
			t.Errorf("tier %q has category %q, want Individual", tier.ID, tier.PlanCategory)
		}
	}

	vipTiers := TiersByCategory(CategoryVIP)
	if len(vipTiers) != 1 {
		t.Errorf("TiersByCategory(VIP) len = %d, want 1", len(vipTiers))
	}
}

func TestTierIDFromStripePriceKey(t *testing.T) {
	tests := []struct {
		key    string
		wantID string
	}{
		{"individual_small_monthly", "individual:small:monthly:20260601"},
		{"individual_medium_monthly", "individual:medium:monthly:20260601"},
		{"individual_large_monthly", "individual:large:monthly:20260601"},
		{"individual_xlarge_monthly", "individual:xlarge:monthly:20260601"},
		// Legacy lookup key → small tier.
		{"individual", "individual:small:monthly:20260601"},
		// Unknown key → small tier fallback.
		{"unknown_key", "individual:small:monthly:20260601"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := tierIDFromStripePriceKey(tt.key)
			if got != tt.wantID {
				t.Errorf("tierIDFromStripePriceKey(%q) = %q, want %q", tt.key, got, tt.wantID)
			}
		})
	}
}

func TestBasePlanHandles4PartTierID(t *testing.T) {
	// BasePlan must correctly extract the category from a 4-part tier ID.
	tests := []struct {
		id   string
		want PlanCategory
	}{
		{"individual:small:monthly:20260601", CategoryIndividual},
		{"individual:xlarge:monthly:20260601", CategoryIndividual},
		{"vip:default:monthly:20260601", CategoryVIP},
		{"team:default:monthly:20260601", CategoryTeam},
	}
	for _, tt := range tests {
		got := BasePlan(tt.id)
		if got != tt.want {
			t.Errorf("BasePlan(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}
