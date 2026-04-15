package plan

import "testing"

// mustgetTierByID is a test helper that fatals if the tier ID is unknown.
func mustgetTierByID(t *testing.T, id string) Tier {
	t.Helper()
	tier, err := getTierByID(id)
	if err != nil {
		t.Fatalf("getTierByID(%q): %v", id, err)
	}
	return tier
}

func TestParseTierID(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantCategory Category
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
		wantCat  Category
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
			tier, err := getTierByID(tt.id)
			if err != nil {
				t.Fatalf("getTierByID(%q): %v", tt.id, err)
			}
			if tier.ID != tt.wantID {
				t.Errorf("getTierByID(%q).ID = %q, want %q", tt.id, tier.ID, tt.wantID)
			}
			if tier.Name != tt.wantName {
				t.Errorf("getTierByID(%q).Name = %q, want %q", tt.id, tier.Name, tt.wantName)
			}
			if tier.Category != tt.wantCat {
				t.Errorf("getTierByID(%q).Category = %q, want %q", tt.id, tier.Category, tt.wantCat)
			}
		})
	}
}

func TestGetTierByIDUnknown(t *testing.T) {
	_, err := getTierByID("totally:bogus:id")
	if err == nil {
		t.Error("getTierByID with unknown ID should return an error")
	}
}

func TestGrantsUnknownPlanID(t *testing.T) {
	if Grants("totally:bogus:id", VMCreate) {
		t.Error("Grants with unknown plan ID should return false")
	}
}

func TestTierGrantsInheritance(t *testing.T) {
	// nil Entitlements → inherit from plan.
	small := mustgetTierByID(t, "individual:small:monthly:20260601")
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
		Category:     CategoryIndividual,
		Name:         "Custom",
		StripePrices: map[string]stripePriceInfo{},

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
	small := mustgetTierByID(t, "individual:small:monthly:20260601")
	if !tierGrants(small, VMCreate) {
		t.Error("individual:small should grant VMCreate")
	}
	if !tierGrants(small, LLMUse) {
		t.Error("individual:small should grant LLMUse")
	}

	vip := mustgetTierByID(t, "vip:default:monthly:20260601")
	// VIP plan has All wildcard — tierGrants should respect it.
	if !tierGrants(vip, VMCreate) {
		t.Error("vip:default should grant VMCreate via wildcard")
	}
	if !tierGrants(vip, Entitlement{"anything:new", "New"}) {
		t.Error("vip:default should grant unknown entitlements via wildcard")
	}
}

func TestGrants(t *testing.T) {
	// 4-part individual tier ID
	if !Grants("individual:small:monthly:20260601", VMCreate) {
		t.Error("individual:small should grant VMCreate")
	}
	if !Grants("individual:xlarge:monthly:20260601", TeamCreate) {
		t.Error("individual:xlarge should grant TeamCreate (inherited from plan)")
	}

	// 3-part legacy individual ID → falls back to small tier → individual plan entitlements
	if !Grants("individual:monthly:20260106", VMCreate) {
		t.Error("legacy individual plan ID should grant VMCreate")
	}

	// Bare "individual" (test helpers and syncAccountPlan insert this)
	if !Grants("individual", VMCreate) {
		t.Error("bare 'individual' should grant VMCreate")
	}

	// VIP wildcard
	if !Grants("vip:default:monthly:20260601", Entitlement{"anything:new", "New"}) {
		t.Error("vip should grant unknown entitlements via wildcard")
	}

	// Basic plan should not grant VMCreate
	if Grants("basic:default:monthly:20260601", VMCreate) {
		t.Error("basic plan should not grant VMCreate")
	}

	// Restricted plan grants nothing
	if Grants("restricted", VMCreate) {
		t.Error("restricted plan should not grant VMCreate")
	}
}

func TestIndividualTierQuotas(t *testing.T) {
	expected := []struct {
		id          string
		compute     computeClass
		defaultDisk uint64
		maxDisk     uint64
		maxVMs      int
	}{
		{"individual:small:monthly:20260601", computeSmall, 25 * gb, 75 * gb, 50},
		{"individual:medium:monthly:20260601", computeMedium, 25 * gb, 75 * gb, 50},
		{"individual:large:monthly:20260601", computeLarge, 25 * gb, 75 * gb, 50},
		{"individual:xlarge:monthly:20260601", computeXLarge, 25 * gb, 75 * gb, 50},
	}
	for _, e := range expected {
		t.Run(e.id, func(t *testing.T) {
			tier := mustgetTierByID(t, e.id)
			if tier.Quotas.ComputeClass != e.compute {
				t.Errorf("ComputeClass = %+v, want %+v", tier.Quotas.ComputeClass, e.compute)
			}
			if tier.Quotas.DefaultDisk != e.defaultDisk {
				t.Errorf("DefaultDisk = %d, want %d", tier.Quotas.DefaultDisk, e.defaultDisk)
			}
			if tier.Quotas.MaxDisk != e.maxDisk {
				t.Errorf("MaxDisk = %d, want %d", tier.Quotas.MaxDisk, e.maxDisk)
			}
			if tier.Quotas.MaxUserVMs != e.maxVMs {
				t.Errorf("MaxUserVMs = %d, want %d", tier.Quotas.MaxUserVMs, e.maxVMs)
			}
		})
	}
}

func TestComputeClasses(t *testing.T) {
	if computeSmall.MaxMemory != 8*gb {
		t.Errorf("computeSmall.MaxMemory = %d, want %d", computeSmall.MaxMemory, 8*gb)
	}
	if computeSmall.MaxCPUs != 2 {
		t.Errorf("computeSmall.MaxCPUs = %d, want 2", computeSmall.MaxCPUs)
	}
	if computeMedium.MaxMemory != 16*gb {
		t.Errorf("computeMedium.MaxMemory = %d, want %d", computeMedium.MaxMemory, 16*gb)
	}
	if computeMedium.MaxCPUs != 4 {
		t.Errorf("computeMedium.MaxCPUs = %d, want 4", computeMedium.MaxCPUs)
	}
	if computeLarge.MaxMemory != 32*gb {
		t.Errorf("computeLarge.MaxMemory = %d, want %d", computeLarge.MaxMemory, 32*gb)
	}
	if computeLarge.MaxCPUs != 8 {
		t.Errorf("computeLarge.MaxCPUs = %d, want 8", computeLarge.MaxCPUs)
	}
	if computeXLarge.MaxMemory != 64*gb {
		t.Errorf("computeXLarge.MaxMemory = %d, want %d", computeXLarge.MaxMemory, 64*gb)
	}
	if computeXLarge.MaxCPUs != 16 {
		t.Errorf("computeXLarge.MaxCPUs = %d, want 16", computeXLarge.MaxCPUs)
	}
}

func TestTiersByCategory(t *testing.T) {
	individualTiers := TiersByCategory(CategoryIndividual)
	if len(individualTiers) != 4 {
		t.Errorf("TiersByCategory(Individual) len = %d, want 4", len(individualTiers))
	}

	// All tiers should be individual category.
	for _, tier := range individualTiers {
		if tier.Category != CategoryIndividual {
			t.Errorf("tier %q has category %q, want Individual", tier.ID, tier.Category)
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
	// Base must correctly extract the category from a 4-part tier ID.
	tests := []struct {
		id   string
		want Category
	}{
		{"individual:small:monthly:20260601", CategoryIndividual},
		{"individual:xlarge:monthly:20260601", CategoryIndividual},
		{"vip:default:monthly:20260601", CategoryVIP},
		{"team:default:monthly:20260601", CategoryTeam},
	}
	for _, tt := range tests {
		got := Base(tt.id)
		if got != tt.want {
			t.Errorf("Base(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestDiskResizeAllowance(t *testing.T) {
	tests := []struct {
		name        string
		planID      string
		currentDisk uint64
		want        uint64
	}{
		{
			name:        "individual small at default",
			planID:      "individual:small:monthly:20260601",
			currentDisk: 25 * gb,
			want:        50 * gb, // 75 - 25 = 50 GB headroom
		},
		{
			name:        "individual small at max",
			planID:      "individual:small:monthly:20260601",
			currentDisk: 75 * gb,
			want:        0,
		},
		{
			name:        "individual small over max",
			planID:      "individual:small:monthly:20260601",
			currentDisk: 80 * gb,
			want:        0,
		},
		{
			name:        "individual small at 10GB",
			planID:      "individual:small:monthly:20260601",
			currentDisk: 10 * gb,
			want:        65 * gb,
		},
		{
			name:        "basic plan no resize",
			planID:      "basic",
			currentDisk: 10 * gb,
			want:        0,
		},
		{
			name:        "vip plan no resize quota",
			planID:      "vip",
			currentDisk: 10 * gb,
			want:        0,
		},
		{
			name:        "unknown plan",
			planID:      "totally:bogus",
			currentDisk: 10 * gb,
			want:        0,
		},
		{
			name:        "legacy individual ID",
			planID:      "individual:monthly:20260106",
			currentDisk: 25 * gb,
			want:        50 * gb, // falls back to small tier, 75 - 25 = 50
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DiskResizeAllowance(tt.planID, tt.currentDisk)
			if got != tt.want {
				t.Errorf("DiskResizeAllowance(%q, %d) = %d, want %d", tt.planID, tt.currentDisk, got, tt.want)
			}
		})
	}
}

func TestMaxDiskForPlan(t *testing.T) {
	if got := MaxDiskForPlan("individual:small:monthly:20260601"); got != 75*gb {
		t.Errorf("MaxDiskForPlan(individual:small) = %d, want %d", got, 75*gb)
	}
	if got := MaxDiskForPlan("basic"); got != 0 {
		t.Errorf("MaxDiskForPlan(basic) = %d, want 0", got)
	}
	if got := MaxDiskForPlan("totally:bogus"); got != 0 {
		t.Errorf("MaxDiskForPlan(totally:bogus) = %d, want 0", got)
	}
}
