package entitlement

import (
	"fmt"
	"strings"
)

const (
	// GB is one gibibyte (2^30 bytes), used for expressing tier quota sizes.
	GB = 1024 * 1024 * 1024
)

// PoolSize represents the vCPU + memory bundle that defines a tier's
// compute capacity. Matches the "Pool size" dropdown on the pricing page.
type PoolSize struct {
	MaxMemory uint64 // bytes
	MaxCPUs   uint64
}

var (
	PoolSmall  = PoolSize{MaxMemory: 8 * GB, MaxCPUs: 2}
	PoolMedium = PoolSize{MaxMemory: 16 * GB, MaxCPUs: 4}
	PoolLarge  = PoolSize{MaxMemory: 32 * GB, MaxCPUs: 8}
	PoolXLarge = PoolSize{MaxMemory: 64 * GB, MaxCPUs: 16}
)

// TierQuotas holds the compute limits for a plan tier.
// Credit amounts are plan-level and live on Plan.
//
//exe:completeinit
type TierQuotas struct {
	PoolSize   PoolSize
	MaxUserVMs int
	MaxTeamVMs int
	MaxDisk    uint64 // bytes
}

// Tier represents a specific billing tier within a plan category.
// Tiers own quotas, Stripe prices, and optional per-tier entitlement overrides.
type Tier struct {
	// ID uses the 4-part format: "{category}:{tier}:{interval}:{version}"
	// e.g. "individual:medium:monthly:20260601"
	ID string

	PlanCategory PlanCategory
	Name         string // "Small", "Medium", "Default", etc.

	StripePrices map[string]StripePriceInfo
	Quotas       TierQuotas

	// Entitlements overrides the plan's base entitlements when non-nil.
	// nil = inherit from the parent Plan. Non-nil = use this set instead.
	Entitlements *map[Entitlement]bool
}

// tiers is the canonical tier catalog, keyed by tier ID (4-part format).
var tiers = map[string]Tier{
	// --- Individual tiers ---
	"individual:small:monthly:20260601": {
		ID:           "individual:small:monthly:20260601",
		PlanCategory: CategoryIndividual,
		Name:         "Small",
		StripePrices: map[string]StripePriceInfo{
			"monthly":         {LookupKey: "individual_small_monthly", Model: "subscription", Interval: "monthly"},
			"usage-disk":      {LookupKey: "individual:usage-disk:20260106", Model: "metered", Interval: ""},
			"usage-bandwidth": {LookupKey: "individual:usage-bandwidth:20260106", Model: "metered", Interval: ""},
		},
		Quotas: TierQuotas{
			PoolSize:   PoolSmall,
			MaxUserVMs: 50,
			MaxTeamVMs: 0,
			MaxDisk:    25 * GB,
		},
		Entitlements: nil,
	},
	"individual:medium:monthly:20260601": {
		ID:           "individual:medium:monthly:20260601",
		PlanCategory: CategoryIndividual,
		Name:         "Medium",
		StripePrices: map[string]StripePriceInfo{
			"monthly":         {LookupKey: "individual_medium_monthly", Model: "subscription", Interval: "monthly"},
			"usage-disk":      {LookupKey: "individual:usage-disk:20260106", Model: "metered", Interval: ""},
			"usage-bandwidth": {LookupKey: "individual:usage-bandwidth:20260106", Model: "metered", Interval: ""},
		},
		Quotas: TierQuotas{
			PoolSize:   PoolMedium,
			MaxUserVMs: 50,
			MaxTeamVMs: 0,
			MaxDisk:    25 * GB,
		},
		Entitlements: nil,
	},
	"individual:large:monthly:20260601": {
		ID:           "individual:large:monthly:20260601",
		PlanCategory: CategoryIndividual,
		Name:         "Large",
		StripePrices: map[string]StripePriceInfo{
			"monthly":         {LookupKey: "individual_large_monthly", Model: "subscription", Interval: "monthly"},
			"usage-disk":      {LookupKey: "individual:usage-disk:20260106", Model: "metered", Interval: ""},
			"usage-bandwidth": {LookupKey: "individual:usage-bandwidth:20260106", Model: "metered", Interval: ""},
		},
		Quotas: TierQuotas{
			PoolSize:   PoolLarge,
			MaxUserVMs: 50,
			MaxTeamVMs: 0,
			MaxDisk:    25 * GB,
		},
		Entitlements: nil,
	},
	"individual:xlarge:monthly:20260601": {
		ID:           "individual:xlarge:monthly:20260601",
		PlanCategory: CategoryIndividual,
		Name:         "XLarge",
		StripePrices: map[string]StripePriceInfo{
			"monthly":         {LookupKey: "individual_xlarge_monthly", Model: "subscription", Interval: "monthly"},
			"usage-disk":      {LookupKey: "individual:usage-disk:20260106", Model: "metered", Interval: ""},
			"usage-bandwidth": {LookupKey: "individual:usage-bandwidth:20260106", Model: "metered", Interval: ""},
		},
		Quotas: TierQuotas{
			PoolSize:   PoolXLarge,
			MaxUserVMs: 50,
			MaxTeamVMs: 0,
			MaxDisk:    25 * GB,
		},
		Entitlements: nil,
	},

	// --- Single-tier plans: one "default" tier each ---

	"vip:default:monthly:20260601": {
		ID:           "vip:default:monthly:20260601",
		PlanCategory: CategoryVIP,
		Name:         "Default",
		StripePrices: map[string]StripePriceInfo{},
		Quotas: TierQuotas{
			PoolSize:   PoolSize{},
			MaxUserVMs: 0,
			MaxTeamVMs: 0,
			MaxDisk:    0,
		},
		Entitlements: nil,
	},
	"enterprise:default:monthly:20260601": {
		ID:           "enterprise:default:monthly:20260601",
		PlanCategory: CategoryEnterprise,
		Name:         "Default",
		StripePrices: map[string]StripePriceInfo{},
		Quotas: TierQuotas{
			PoolSize:   PoolSize{},
			MaxUserVMs: 0,
			MaxTeamVMs: 0,
			MaxDisk:    0,
		},
		Entitlements: nil,
	},
	"team:default:monthly:20260601": {
		ID:           "team:default:monthly:20260601",
		PlanCategory: CategoryTeam,
		Name:         "Default",
		StripePrices: map[string]StripePriceInfo{},
		Quotas: TierQuotas{
			PoolSize:   PoolSize{},
			MaxUserVMs: 0,
			MaxTeamVMs: 0,
			MaxDisk:    0,
		},
		Entitlements: nil,
	},
	"friend:default:monthly:20260601": {
		ID:           "friend:default:monthly:20260601",
		PlanCategory: CategoryFriend,
		Name:         "Default",
		StripePrices: map[string]StripePriceInfo{},
		Quotas: TierQuotas{
			PoolSize:   PoolSize{},
			MaxUserVMs: 0,
			MaxTeamVMs: 0,
			MaxDisk:    0,
		},
		Entitlements: nil,
	},
	"grandfathered:default:monthly:20260601": {
		ID:           "grandfathered:default:monthly:20260601",
		PlanCategory: CategoryGrandfathered,
		Name:         "Default",
		StripePrices: map[string]StripePriceInfo{},
		Quotas: TierQuotas{
			PoolSize:   PoolSize{},
			MaxUserVMs: 0,
			MaxTeamVMs: 0,
			MaxDisk:    0,
		},
		Entitlements: nil,
	},
	"trial:default:monthly:20260601": {
		ID:           "trial:default:monthly:20260601",
		PlanCategory: CategoryTrial,
		Name:         "Default",
		StripePrices: map[string]StripePriceInfo{},
		Quotas: TierQuotas{
			PoolSize:   PoolSize{},
			MaxUserVMs: 0,
			MaxTeamVMs: 0,
			MaxDisk:    0,
		},
		Entitlements: nil,
	},
	"basic:default:monthly:20260601": {
		ID:           "basic:default:monthly:20260601",
		PlanCategory: CategoryBasic,
		Name:         "Default",
		StripePrices: map[string]StripePriceInfo{},
		Quotas: TierQuotas{
			PoolSize:   PoolSize{},
			MaxUserVMs: 0,
			MaxTeamVMs: 0,
			MaxDisk:    0,
		},
		Entitlements: nil,
	},
	"restricted:default:monthly:20260601": {
		ID:           "restricted:default:monthly:20260601",
		PlanCategory: CategoryRestricted,
		Name:         "Default",
		StripePrices: map[string]StripePriceInfo{},
		Quotas: TierQuotas{
			PoolSize:   PoolSize{},
			MaxUserVMs: 0,
			MaxTeamVMs: 0,
			MaxDisk:    0,
		},
		Entitlements: nil,
	},
}

// GetTierByID returns the Tier for a given tier ID.
// Handles 4-part tier IDs ("individual:medium:monthly:20260601") as well as
// 3-part legacy plan IDs ("individual:monthly:20260106").
// Returns an error for completely unknown IDs.
func GetTierByID(id string) (Tier, error) {
	if tier, ok := tiers[id]; ok {
		return tier, nil
	}
	// Extract the category from the ID and look up the plan's DefaultTier.
	// This handles:
	//   - 3-part legacy IDs: "individual:monthly:20260106"
	//   - Bare category strings: "individual", "friend", "basic"
	//   - Any other unknown format
	cat := BasePlan(id)
	if plan, ok := GetPlan(cat); ok && plan.DefaultTier != "" {
		if tier, ok := tiers[plan.DefaultTier]; ok {
			return tier, nil
		}
	}
	return Tier{}, fmt.Errorf("unknown tier ID %q", id)
}

// ParseTierID extracts the plan category, tier name, interval, and version
// from a 4-part tier ID (e.g. "individual:medium:monthly:20260601").
// Returns empty strings for any missing fields.
func parseTierID(id string) (category PlanCategory, tierName, interval, version string) {
	parts := strings.SplitN(id, ":", 4)
	switch len(parts) {
	case 4:
		return PlanCategory(parts[0]), parts[1], parts[2], parts[3]
	case 3:
		// 3-part legacy ID — treat as plan-level, no tier name
		return PlanCategory(parts[0]), "", parts[1], parts[2]
	case 2:
		return PlanCategory(parts[0]), "", parts[1], ""
	default:
		return PlanCategory(id), "", "", ""
	}
}

// TiersByCategory returns all tiers for a given plan category, sorted
// by pool size (MaxCPUs ascending) so tiers display small → large.
func TiersByCategory(cat PlanCategory) []Tier {
	var result []Tier
	for _, t := range tiers {
		if t.PlanCategory == cat {
			result = append(result, t)
		}
	}
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j].Quotas.PoolSize.MaxCPUs < result[j-1].Quotas.PoolSize.MaxCPUs; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

// effectiveEntitlements returns the entitlements for a tier, inheriting
// from the parent plan when the tier has no override.
func effectiveEntitlements(tier Tier) map[Entitlement]bool {
	if tier.Entitlements != nil {
		return *tier.Entitlements
	}
	plan, ok := GetPlan(tier.PlanCategory)
	if !ok {
		return map[Entitlement]bool{}
	}
	return plan.Entitlements
}

// tierGrants reports whether the given tier grants the specified entitlement.
// Tier-level entitlement overrides take precedence; otherwise the parent plan's
// entitlements are used.
func tierGrants(tier Tier, ent Entitlement) bool {
	ents := effectiveEntitlements(tier)
	if ents[All] {
		return true
	}
	return ents[ent]
}

// tierIDFromStripePriceKey returns the tier ID for a given Stripe price lookup key.
// Falls back to the individual Small tier for unknown keys.
func tierIDFromStripePriceKey(key string) string {
	for id, tier := range tiers {
		for _, price := range tier.StripePrices {
			if price.LookupKey == key {
				return id
			}
		}
	}
	// Legacy lookup key for individual (no tier suffix)
	if key == "individual" {
		return "individual:small:monthly:20260601"
	}
	return "individual:small:monthly:20260601"
}
