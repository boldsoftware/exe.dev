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
	if !tierGrants(vip, VMCreate) {
		t.Error("vip:default should grant VMCreate")
	}
	if tierGrants(vip, Entitlement{"anything:new", "New"}) {
		t.Error("vip:default should not grant unknown entitlements")
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

	// VIP explicit entitlements (no wildcard)
	if Grants("vip:default:monthly:20260601", Entitlement{"anything:new", "New"}) {
		t.Error("vip should not grant unknown entitlements")
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
		maxCPUs     uint64
		maxMemory   uint64
		defaultDisk uint64
		maxDisk     uint64
		maxVMs      int
	}{
		{"individual:small:monthly:20260601", 2, 8 * gb, 25 * gb, 75 * gb, 50},
		{"individual:medium:monthly:20260601", 4, 16 * gb, 25 * gb, 75 * gb, 50},
		{"individual:large:monthly:20260601", 8, 32 * gb, 25 * gb, 75 * gb, 50},
		{"individual:xlarge:monthly:20260601", 16, 64 * gb, 25 * gb, 75 * gb, 50},
	}
	for _, e := range expected {
		t.Run(e.id, func(t *testing.T) {
			tier := mustgetTierByID(t, e.id)
			if tier.Quotas.MaxCPUs != e.maxCPUs {
				t.Errorf("MaxCPUs = %d, want %d", tier.Quotas.MaxCPUs, e.maxCPUs)
			}
			if tier.Quotas.MaxMemory != e.maxMemory {
				t.Errorf("MaxMemory = %d, want %d", tier.Quotas.MaxMemory, e.maxMemory)
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
		// "individual" lookup key maps to small tier (the current Stripe price).
		{"individual", "individual:small:monthly:20260601"},
		{"individual:medium:monthly:20160102", "individual:medium:monthly:20260601"},
		{"individual:large:monthly:20160102", "individual:large:monthly:20260601"},
		{"individual:xlarge:monthly:20160102", "individual:xlarge:monthly:20260601"},
		// Unknown key → small tier fallback.
		{"unknown_key", "individual:small:monthly:20260601"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := TierIDFromStripePriceKey(tt.key)
			if got != tt.wantID {
				t.Errorf("TierIDFromStripePriceKey(%q) = %q, want %q", tt.key, got, tt.wantID)
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

func TestRemainingDiskQuota_Legacy(t *testing.T) {
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
			name:        "vip plan",
			planID:      "vip",
			currentDisk: 10 * gb,
			want:        65 * gb, // 75 - 10 = 65 GB
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
			got := RemainingDiskQuota(tt.planID, tt.currentDisk)
			if got != tt.want {
				t.Errorf("RemainingDiskQuota(%q, %d) = %d, want %d", tt.planID, tt.currentDisk, got, tt.want)
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

func TestIncludedDisk(t *testing.T) {
	tests := []struct {
		name       string
		tierID     string
		envDefault uint64 // stage.Env.DefaultDisk
		want       uint64
	}{
		// Prod/staging: env.DefaultDisk=0 → defer to tier.
		{
			name:       "individual small prod",
			tierID:     "individual:small:monthly:20260601",
			envDefault: 0,
			want:       25 * gb,
		},
		{
			name:       "individual xlarge prod",
			tierID:     "individual:xlarge:monthly:20260601",
			envDefault: 0,
			want:       25 * gb,
		},
		{
			name:       "trial prod",
			tierID:     "trial",
			envDefault: 0,
			want:       25 * gb,
		},
		{
			name:       "basic prod",
			tierID:     "basic",
			envDefault: 0,
			want:       25 * gb,
		},
		{
			name:       "vip prod",
			tierID:     "vip",
			envDefault: 0,
			want:       25 * gb,
		},
		// Local: env.DefaultDisk=10GB < tier → use env.
		{
			name:       "individual small local",
			tierID:     "individual:small:monthly:20260601",
			envDefault: 10 * gb,
			want:       10 * gb,
		},
		{
			name:       "trial local",
			tierID:     "trial",
			envDefault: 10 * gb,
			want:       10 * gb,
		},
		// Test: env.DefaultDisk=11GB < tier → use env.
		{
			name:       "individual small test",
			tierID:     "individual:small:monthly:20260601",
			envDefault: 11 * gb,
			want:       11 * gb,
		},
		// Legacy 3-part ID.
		{
			name:       "legacy individual prod",
			tierID:     "individual:monthly:20260106",
			envDefault: 0,
			want:       25 * gb,
		},
		// Unknown plan falls back to env default.
		{
			name:       "unknown plan local",
			tierID:     "totally:bogus",
			envDefault: 10 * gb,
			want:       10 * gb,
		},
		// Unknown plan with env=0 → 0 (caller should handle).
		{
			name:       "unknown plan prod",
			tierID:     "totally:bogus",
			envDefault: 0,
			want:       0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IncludedDisk(tt.tierID, tt.envDefault)
			if got != tt.want {
				t.Errorf("IncludedDisk(%q, %d) = %d, want %d", tt.tierID, tt.envDefault, got, tt.want)
			}
		})
	}
}

func TestMaxDiskForPlanWithEnv(t *testing.T) {
	tests := []struct {
		name   string
		tierID string
		want   uint64
	}{
		{"individual small", "individual:small:monthly:20260601", 75 * gb},
		{"individual xlarge", "individual:xlarge:monthly:20260601", 75 * gb},
		{"trial", "trial", 75 * gb},
		{"vip", "vip", 75 * gb},
		{"friend", "friend", 75 * gb},
		{"basic", "basic", 0},
		{"restricted", "restricted", 0},
		{"unknown", "totally:bogus", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaxDiskForPlan(tt.tierID)
			if got != tt.want {
				t.Errorf("MaxDiskForPlan(%q) = %d, want %d", tt.tierID, got, tt.want)
			}
		})
	}
}

func TestRemainingDiskQuota(t *testing.T) {
	tests := []struct {
		name        string
		tierID      string
		currentDisk uint64
		want        uint64
	}{
		// Started at 20GB (prod default), can grow to 75GB.
		{
			name:        "individual small from 20GB",
			tierID:      "individual:small:monthly:20260601",
			currentDisk: 20 * gb,
			want:        55 * gb,
		},
		// Started at 25GB (tier included), can grow to 75GB.
		{
			name:        "individual small from 25GB",
			tierID:      "individual:small:monthly:20260601",
			currentDisk: 25 * gb,
			want:        50 * gb,
		},
		// Already at max.
		{
			name:        "individual small at max",
			tierID:      "individual:small:monthly:20260601",
			currentDisk: 75 * gb,
			want:        0,
		},
		// Over max.
		{
			name:        "individual small over max",
			tierID:      "individual:small:monthly:20260601",
			currentDisk: 80 * gb,
			want:        0,
		},
		// Basic: MaxDisk=0 → no resize.
		{
			name:        "basic no resize",
			tierID:      "basic",
			currentDisk: 10 * gb,
			want:        0,
		},
		// Restricted: MaxDisk=0 → no resize.
		{
			name:        "restricted no resize",
			tierID:      "restricted",
			currentDisk: 10 * gb,
			want:        0,
		},
		// Unknown plan.
		{
			name:        "unknown plan",
			tierID:      "totally:bogus",
			currentDisk: 10 * gb,
			want:        0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RemainingDiskQuota(tt.tierID, tt.currentDisk)
			if got != tt.want {
				t.Errorf("RemainingDiskQuota(%q, %d) = %d, want %d", tt.tierID, tt.currentDisk, got, tt.want)
			}
		})
	}
}

func TestEffectiveMaxDisk(t *testing.T) {
	tests := []struct {
		name           string
		planID         string
		userMaxDisk    uint64
		envDefaultDisk uint64
		want           uint64
	}{
		// --- Prod (envDefaultDisk=0): tier catalog is authoritative ---
		{
			name:   "individual prod no override",
			planID: "individual:small:monthly:20260601",
			want:   75 * gb,
		},
		{
			name:        "support granted 200GB prod",
			planID:      "individual:small:monthly:20260601",
			userMaxDisk: 200 * gb,
			want:        200 * gb,
		},
		{
			name:        "support set same as plan prod",
			planID:      "individual:small:monthly:20260601",
			userMaxDisk: 75 * gb,
			want:        75 * gb,
		},
		{
			name:        "support set below plan prod",
			planID:      "individual:small:monthly:20260601",
			userMaxDisk: 30 * gb,
			want:        30 * gb,
		},
		{
			name:        "support override above plan max prod",
			planID:      "individual:small:monthly:20260601",
			userMaxDisk: 80 * gb,
			want:        80 * gb,
		},
		{
			name:   "basic prod no override",
			planID: "basic",
			want:   0,
		},
		{
			name:        "basic prod with support override",
			planID:      "basic",
			userMaxDisk: 50 * gb,
			want:        50 * gb,
		},
		{
			name:        "restricted prod with support override",
			planID:      "restricted",
			userMaxDisk: 100 * gb,
			want:        100 * gb,
		},
		{
			name:        "unknown plan prod with support override",
			planID:      "totally:bogus",
			userMaxDisk: 60 * gb,
			want:        60 * gb,
		},
		{
			name:   "unknown plan prod no override",
			planID: "totally:bogus",
			want:   0,
		},
		{
			name:   "vip prod no override",
			planID: "vip",
			want:   75 * gb,
		},
		{
			name:        "trial prod with support override",
			planID:      "trial",
			userMaxDisk: 150 * gb,
			want:        150 * gb,
		},

		// --- Test env (envDefaultDisk=11GB): env caps the tier ceiling ---
		{
			name:           "individual test no override",
			planID:         "individual:small:monthly:20260601",
			envDefaultDisk: 11 * gb,
			want:           11 * gb,
		},
		{
			name:           "vip test no override",
			planID:         "vip",
			envDefaultDisk: 11 * gb,
			want:           11 * gb,
		},
		{
			name:           "basic test no override",
			planID:         "basic",
			envDefaultDisk: 11 * gb,
			want:           0, // MaxDisk=0 → no resize, env doesn't help
		},
		// Support override still wins over env cap.
		{
			name:           "individual test with support override",
			planID:         "individual:small:monthly:20260601",
			userMaxDisk:    30 * gb,
			envDefaultDisk: 11 * gb,
			want:           30 * gb,
		},

		// --- Local (envDefaultDisk=10GB) ---
		{
			name:           "individual local no override",
			planID:         "individual:small:monthly:20260601",
			envDefaultDisk: 10 * gb,
			want:           10 * gb,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveMaxDisk(tt.planID, tt.userMaxDisk, tt.envDefaultDisk)
			if got != tt.want {
				t.Errorf("EffectiveMaxDisk(%q, %d, %d) = %d, want %d",
					tt.planID, tt.userMaxDisk, tt.envDefaultDisk, got, tt.want)
			}
		})
	}
}

// TestDiskQuotasPlanTransitions verifies that disk quota functions behave
// correctly across plan changes. Disk is set at VM creation and never
// retroactively resized — these functions only affect what's *allowed*,
// not what exists.
func TestDiskQuotasPlanTransitions(t *testing.T) {
	const envDefault uint64 = 0 // prod

	// Scenario 1: User on Individual creates a VM (25GB), downgrades to Basic.
	// Their 25GB disk is untouched. They just can't resize anymore.
	t.Run("downgrade individual to basic", func(t *testing.T) {
		// At creation on Individual: 25GB included.
		creationDisk := IncludedDisk("individual:small:monthly:20260601", envDefault)
		if creationDisk != 25*gb {
			t.Fatalf("IncludedDisk(individual) = %d, want %d", creationDisk, 25*gb)
		}

		// After downgrade: Basic has MaxDisk=0, no resize allowed.
		if got := MaxDiskForPlan("basic"); got != 0 {
			t.Errorf("MaxDiskForPlan(basic) = %d, want 0", got)
		}
		if got := RemainingDiskQuota("basic", creationDisk); got != 0 {
			t.Errorf("RemainingDiskQuota(basic, %d) = %d, want 0", creationDisk, got)
		}
		// Key invariant: the 25GB disk still exists. Nothing shrinks it.
		// The user just can't grow past it on Basic.
	})

	// Scenario 2: User on Basic creates a VM (25GB), upgrades to Individual.
	// Their 25GB disk is untouched. They can now resize up to 75GB.
	t.Run("upgrade basic to individual", func(t *testing.T) {
		creationDisk := IncludedDisk("basic", envDefault)
		if creationDisk != 25*gb {
			t.Fatalf("IncludedDisk(basic) = %d, want %d", creationDisk, 25*gb)
		}

		// After upgrade: Individual allows resize up to 75GB.
		if got := MaxDiskForPlan("individual:small:monthly:20260601"); got != 75*gb {
			t.Errorf("MaxDiskForPlan(individual) = %d, want %d", got, 75*gb)
		}
		if got := RemainingDiskQuota("individual:small:monthly:20260601", creationDisk); got != 50*gb {
			t.Errorf("RemainingDiskQuota(individual, %d) = %d, want %d", creationDisk, got, 50*gb)
		}
	})

	// Scenario 3: User on Individual Small upgrades to XLarge.
	// Same disk quotas — tier size affects compute, not disk.
	t.Run("upgrade individual small to xlarge", func(t *testing.T) {
		smallDisk := IncludedDisk("individual:small:monthly:20260601", envDefault)
		xlargeDisk := IncludedDisk("individual:xlarge:monthly:20260601", envDefault)
		if smallDisk != xlargeDisk {
			t.Errorf("IncludedDisk small=%d vs xlarge=%d — should be equal", smallDisk, xlargeDisk)
		}

		smallMax := MaxDiskForPlan("individual:small:monthly:20260601")
		xlargeMax := MaxDiskForPlan("individual:xlarge:monthly:20260601")
		if smallMax != xlargeMax {
			t.Errorf("MaxDiskForPlan small=%d vs xlarge=%d — should be equal", smallMax, xlargeMax)
		}
	})

	// Scenario 4: User had support override (80GB), downgrades plan.
	// Support override is independent of plan — still 80GB.
	t.Run("support override survives plan change", func(t *testing.T) {
		var supportMaxDisk uint64 = 80 * gb

		// On Individual with override.
		if got := EffectiveMaxDisk("individual:small:monthly:20260601", supportMaxDisk, 0); got != 80*gb {
			t.Errorf("EffectiveMaxDisk(individual, 80GB) = %d, want %d", got, 80*gb)
		}
		// Downgrade to Basic — override still wins.
		if got := EffectiveMaxDisk("basic", supportMaxDisk, 0); got != 80*gb {
			t.Errorf("EffectiveMaxDisk(basic, 80GB) = %d, want %d", got, 80*gb)
		}
	})
}

func TestIncludedBandwidth(t *testing.T) {
	t.Run("individual tier has 100GB", func(t *testing.T) {
		if got := IncludedBandwidth("individual:small:monthly:20260601"); got != 100*gb {
			t.Errorf("IncludedBandwidth(individual:small) = %d, want %d", got, 100*gb)
		}
	})

	t.Run("team tier has 100GB", func(t *testing.T) {
		if got := IncludedBandwidth("team:default:monthly:20260601"); got != 100*gb {
			t.Errorf("IncludedBandwidth(team) = %d, want %d", got, 100*gb)
		}
	})

	t.Run("trial tier has 100GB", func(t *testing.T) {
		if got := IncludedBandwidth("trial:default:monthly:20260601"); got != 100*gb {
			t.Errorf("IncludedBandwidth(trial) = %d, want %d", got, 100*gb)
		}
	})

	t.Run("restricted tier has no bandwidth quota", func(t *testing.T) {
		if got := IncludedBandwidth("restricted:default:monthly:20260601"); got != 0 {
			t.Errorf("IncludedBandwidth(restricted) = %d, want 0", got)
		}
	})

	t.Run("unknown tier returns zero", func(t *testing.T) {
		if got := IncludedBandwidth("bogus:tier:id:x"); got != 0 {
			t.Errorf("IncludedBandwidth(bogus) = %d, want 0", got)
		}
	})
}

func TestNextTier(t *testing.T) {
	t.Run("small has medium as next", func(t *testing.T) {
		next := NextTier("individual:small:monthly:20260601")
		if next == nil {
			t.Fatal("expected next tier, got nil")
		}
		if next.Name != "Medium" {
			t.Errorf("next tier name = %q, want Medium", next.Name)
		}
		if next.Quotas.MaxCPUs != 4 {
			t.Errorf("next tier MaxCPUs = %d, want 4", next.Quotas.MaxCPUs)
		}
	})

	t.Run("medium has large as next", func(t *testing.T) {
		next := NextTier("individual:medium:monthly:20260601")
		if next == nil {
			t.Fatal("expected next tier, got nil")
		}
		if next.Name != "Large" {
			t.Errorf("next tier name = %q, want Large", next.Name)
		}
	})

	t.Run("large has xlarge as next", func(t *testing.T) {
		next := NextTier("individual:large:monthly:20260601")
		if next == nil {
			t.Fatal("expected next tier, got nil")
		}
		if next.Name != "XLarge" {
			t.Errorf("next tier name = %q, want XLarge", next.Name)
		}
	})

	t.Run("xlarge has no next tier", func(t *testing.T) {
		next := NextTier("individual:xlarge:monthly:20260601")
		if next != nil {
			t.Errorf("expected nil for largest tier, got %q", next.Name)
		}
	})

	t.Run("single-tier plan has no next", func(t *testing.T) {
		next := NextTier("team:default:monthly:20260601")
		if next != nil {
			t.Errorf("expected nil for single-tier plan, got %q", next.Name)
		}
	})

	t.Run("unknown tier returns nil", func(t *testing.T) {
		next := NextTier("bogus:tier:id:x")
		if next != nil {
			t.Errorf("expected nil for unknown tier, got %q", next.Name)
		}
	})
}

func TestTierMonthlyPriceCents(t *testing.T) {
	tests := []struct {
		tierID string
		want   int
	}{
		{"individual:small:monthly:20260601", 2000},
		{"individual:medium:monthly:20260601", 4000},
		{"individual:large:monthly:20260601", 8000},
		{"individual:xlarge:monthly:20260601", 16000},
		{"team:default:monthly:20260601", 0},
	}
	for _, tt := range tests {
		t.Run(tt.tierID, func(t *testing.T) {
			tier := mustgetTierByID(t, tt.tierID)
			if tier.MonthlyPriceCents != tt.want {
				t.Errorf("MonthlyPriceCents = %d, want %d", tier.MonthlyPriceCents, tt.want)
			}
		})
	}
}
