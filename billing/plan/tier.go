package plan

import (
	"fmt"
	"strings"
)

// Tier represents a specific billing tier within a plan category.
// Tiers own quotas, Stripe prices, and optional per-tier entitlement overrides.
type Tier struct {
	// ID uses the 4-part format: "{category}:{tier}:{interval}:{version}"
	// e.g. "individual:medium:monthly:20260106"
	ID string

	Category Category
	Name     string // "Small", "Medium", "Standard", etc.

	StripePrices      map[string]stripePriceInfo
	Quotas            tierQuotas
	MonthlyPriceCents int // base monthly subscription price in cents (e.g. 2000 = $20)

	// Entitlements overrides the plan's base entitlements when non-nil.
	// nil = inherit from the parent Plan. Non-nil = use this set instead.
	Entitlements *map[Entitlement]bool
}

// TiersByCategory returns all tiers for a given plan category, sorted
// by pool size (MaxCPUs ascending) so tiers display small → large.
func TiersByCategory(cat Category) []Tier {
	var result []Tier
	for _, t := range tiers {
		if t.Category == cat {
			result = append(result, t)
		}
	}
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j].Quotas.MaxCPUs < result[j-1].Quotas.MaxCPUs; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

// IncludedDisk returns the disk size (bytes) included with a plan.
// The tier catalog is the source of truth. When envDefaultDisk is non-zero
// and smaller than the tier value, it acts as an override (for local/test
// environments that don't need production-sized disks).
// Returns envDefaultDisk as a fallback for unknown tier IDs.
func IncludedDisk(tierID string, envDefaultDisk uint64) uint64 {
	tier, err := getTierByID(tierID)
	if err != nil {
		return envDefaultDisk
	}
	tierDefault := tier.Quotas.DefaultDisk
	if tierDefault == 0 {
		return envDefaultDisk
	}
	if envDefaultDisk > 0 && envDefaultDisk < tierDefault {
		return envDefaultDisk
	}
	return tierDefault
}

// IncludedBandwidth returns the bandwidth (bytes) included with a plan per billing period.
// Returns 0 for unknown tier IDs or tiers with no bandwidth quota.
func IncludedBandwidth(tierID string) uint64 {
	tier, err := getTierByID(tierID)
	if err != nil {
		return 0
	}
	return tier.Quotas.DefaultBandwidth
}

// EffectiveMaxDisk returns the disk ceiling for a user given their plan,
// any support-granted override, and the environment's default disk.
//
// Resolution order:
//  1. Support override (userMaxDisk > 0) always wins.
//  2. If envDefaultDisk > 0 and the tier has a non-zero MaxDisk, the env
//     caps the tier value. This keeps test/local environments from allowing
//     huge disks that the host can't back.
//  3. Otherwise the tier's MaxDisk is used (prod/staging path).
//
// Returns 0 when the plan has no disk resize quota (e.g. basic/restricted)
// and there's no support override.
func EffectiveMaxDisk(planID string, userMaxDisk, envDefaultDisk uint64) uint64 {
	if userMaxDisk > 0 {
		return userMaxDisk
	}
	maxDisk := MaxDiskForPlan(planID)
	if maxDisk > 0 && envDefaultDisk > 0 && envDefaultDisk < maxDisk {
		return envDefaultDisk
	}
	return maxDisk
}

// RemainingDiskQuota returns how many more bytes a VM's disk can grow
// given the current disk size and the user's plan. Returns 0 if the plan
// does not allow resize (MaxDisk == 0) or the disk is already at/above the ceiling.
func RemainingDiskQuota(tierID string, currentDiskSize uint64) uint64 {
	tier, err := getTierByID(tierID)
	if err != nil || tier.Quotas.MaxDisk == 0 {
		return 0
	}
	if currentDiskSize >= tier.Quotas.MaxDisk {
		return 0
	}
	return tier.Quotas.MaxDisk - currentDiskSize
}

// MaxDiskForPlan returns the absolute disk ceiling for a plan.
// Returns 0 if the plan is unknown or has no disk resize quota.
func MaxDiskForPlan(planID string) uint64 {
	tier, err := getTierByID(planID)
	if err != nil {
		return 0
	}
	return tier.Quotas.MaxDisk
}

// MaxCPUsForPlan returns the vCPU pool limit for a plan.
// Returns 0 if the plan is unknown or has no CPU pool quota.
func MaxCPUsForPlan(planID string) uint64 {
	tier, err := getTierByID(planID)
	if err != nil {
		return 0
	}
	return tier.Quotas.MaxCPUs
}

// MaxUserVMsForPlan returns the per-user VM cap for a plan.
// Returns 0 if the plan is unknown or the tier leaves the cap unset
// (in which case callers should fall back to stage.DefaultMaxBoxes).
func MaxUserVMsForPlan(planID string) int {
	tier, err := getTierByID(planID)
	if err != nil {
		return 0
	}
	return tier.Quotas.MaxUserVMs
}

// MaxTeamVMsForPlan returns the per-team VM cap for a plan.
// Returns 0 if the plan is unknown or the tier leaves the cap unset
// (in which case callers should fall back to stage.DefaultMaxTeamBoxes).
func MaxTeamVMsForPlan(planID string) int {
	tier, err := getTierByID(planID)
	if err != nil {
		return 0
	}
	return tier.Quotas.MaxTeamVMs
}

// MaxMemoryForPlan returns the memory pool limit (in bytes) for a plan.
// Returns 0 if the plan is unknown or has no memory pool quota.
func MaxMemoryForPlan(planID string) uint64 {
	tier, err := getTierByID(planID)
	if err != nil {
		return 0
	}
	return tier.Quotas.MaxMemory
}

// NextTier returns the next larger tier in the same category, or nil if
// the current tier is the largest. Tiers are ordered by MaxCPUs ascending.
func NextTier(currentID string) *Tier {
	current, err := getTierByID(currentID)
	if err != nil {
		return nil
	}
	all := TiersByCategory(current.Category)
	for i, t := range all {
		if t.ID == current.ID && i+1 < len(all) {
			next := all[i+1]
			return &next
		}
	}
	return nil
}

// --- unexported ---

const (
	// gb is one gibibyte (2^30 bytes), used for expressing tier quota sizes.
	gb = 1024 * 1024 * 1024
)

// tierQuotas holds the compute limits for a plan tier.
// Credit amounts are plan-level and live on Plan.
//
//exe:completeinit
type tierQuotas struct {
	MaxCPUs          uint64 // vCPUs
	MaxMemory        uint64 // bytes
	MaxUserVMs       int
	MaxTeamVMs       int
	DefaultDisk      uint64 // bytes — disk size for new VMs
	MaxDisk          uint64 // bytes — per-VM ceiling for disk resize; contact support above this (0 = no resize allowed)
	DefaultBandwidth uint64 // bytes — bandwidth included per billing period (0 = use env default)
}

// tiers is the canonical tier catalog, keyed by tier ID (4-part format).
var tiers = map[string]Tier{
	// --- Individual tiers ---
	"individual:small:monthly:20260106": {
		ID:       "individual:small:monthly:20260106",
		Category: CategoryIndividual,
		Name:     "Small",
		StripePrices: map[string]stripePriceInfo{
			"monthly":         {LookupKey: "individual", Model: "subscription", Interval: "monthly"},
			"usage-disk":      {LookupKey: "individual:usage-disk:20260106", Model: "metered", Interval: ""},
			"usage-bandwidth": {LookupKey: "individual:usage-bandwidth:20260106", Model: "metered", Interval: ""},
		},
		Quotas: tierQuotas{
			MaxCPUs: 2, MaxMemory: 8 * gb,
			MaxUserVMs:       50,
			MaxTeamVMs:       0,
			DefaultDisk:      25 * gb,
			MaxDisk:          75 * gb,
			DefaultBandwidth: 100 * gb,
		},
		MonthlyPriceCents: 2000,
		Entitlements:      nil,
	},
	"individual:medium:monthly:20260106": {
		ID:       "individual:medium:monthly:20260106",
		Category: CategoryIndividual,
		Name:     "Medium",
		StripePrices: map[string]stripePriceInfo{
			"monthly":         {LookupKey: "individual:medium:monthly:20160102", Model: "subscription", Interval: "monthly"},
			"usage-disk":      {LookupKey: "individual:usage-disk:20260106", Model: "metered", Interval: ""},
			"usage-bandwidth": {LookupKey: "individual:usage-bandwidth:20260106", Model: "metered", Interval: ""},
		},
		Quotas: tierQuotas{
			MaxCPUs: 4, MaxMemory: 16 * gb,
			MaxUserVMs:       50,
			MaxTeamVMs:       0,
			DefaultDisk:      25 * gb,
			MaxDisk:          75 * gb,
			DefaultBandwidth: 100 * gb,
		},
		MonthlyPriceCents: 4000,
		Entitlements:      nil,
	},
	"individual:large:monthly:20260106": {
		ID:       "individual:large:monthly:20260106",
		Category: CategoryIndividual,
		Name:     "Large",
		StripePrices: map[string]stripePriceInfo{
			"monthly":         {LookupKey: "individual:large:monthly:20160102", Model: "subscription", Interval: "monthly"},
			"usage-disk":      {LookupKey: "individual:usage-disk:20260106", Model: "metered", Interval: ""},
			"usage-bandwidth": {LookupKey: "individual:usage-bandwidth:20260106", Model: "metered", Interval: ""},
		},
		Quotas: tierQuotas{
			MaxCPUs: 8, MaxMemory: 32 * gb,
			MaxUserVMs:       50,
			MaxTeamVMs:       0,
			DefaultDisk:      25 * gb,
			MaxDisk:          75 * gb,
			DefaultBandwidth: 100 * gb,
		},
		MonthlyPriceCents: 8000,
		Entitlements:      nil,
	},
	"individual:xlarge:monthly:20260106": {
		ID:       "individual:xlarge:monthly:20260106",
		Category: CategoryIndividual,
		Name:     "XLarge",
		StripePrices: map[string]stripePriceInfo{
			"monthly":         {LookupKey: "individual:xlarge:monthly:20160102", Model: "subscription", Interval: "monthly"},
			"usage-disk":      {LookupKey: "individual:usage-disk:20260106", Model: "metered", Interval: ""},
			"usage-bandwidth": {LookupKey: "individual:usage-bandwidth:20260106", Model: "metered", Interval: ""},
		},
		Quotas: tierQuotas{
			MaxCPUs: 16, MaxMemory: 64 * gb,
			MaxUserVMs:       50,
			MaxTeamVMs:       0,
			DefaultDisk:      25 * gb,
			MaxDisk:          75 * gb,
			DefaultBandwidth: 100 * gb,
		},
		MonthlyPriceCents: 16000,
		Entitlements:      nil,
	},

	// --- Single-tier plans: one "default" tier each ---

	"enterprise:default:monthly:20260106": {
		ID:           "enterprise:default:monthly:20260106",
		Category:     CategoryEnterprise,
		Name:         "Standard",
		StripePrices: map[string]stripePriceInfo{},
		Quotas: tierQuotas{
			MaxCPUs: 0, MaxMemory: 0,
			MaxUserVMs:       50,
			MaxTeamVMs:       0,
			DefaultDisk:      25 * gb,
			MaxDisk:          75 * gb,
			DefaultBandwidth: 100 * gb,
		},
		Entitlements: nil,
	},
	"team:default:monthly:20260106": {
		ID:           "team:default:monthly:20260106",
		Category:     CategoryTeam,
		Name:         "Standard",
		StripePrices: map[string]stripePriceInfo{},
		Quotas: tierQuotas{
			MaxCPUs: 0, MaxMemory: 0,
			MaxUserVMs:       50,
			MaxTeamVMs:       0,
			DefaultDisk:      25 * gb,
			MaxDisk:          75 * gb,
			DefaultBandwidth: 100 * gb,
		},
		Entitlements: nil,
	},
	"friend:default:monthly:20260106": {
		ID:           "friend:default:monthly:20260106",
		Category:     CategoryFriend,
		Name:         "Standard",
		StripePrices: map[string]stripePriceInfo{},
		Quotas: tierQuotas{
			MaxCPUs: 0, MaxMemory: 0,
			MaxUserVMs:       50,
			MaxTeamVMs:       0,
			DefaultDisk:      25 * gb,
			MaxDisk:          75 * gb,
			DefaultBandwidth: 100 * gb,
		},
		Entitlements: nil,
	},
	"grandfathered:default:monthly:20260106": {
		ID:           "grandfathered:default:monthly:20260106",
		Category:     CategoryGrandfathered,
		Name:         "Standard",
		StripePrices: map[string]stripePriceInfo{},
		Quotas: tierQuotas{
			MaxCPUs: 0, MaxMemory: 0,
			MaxUserVMs:       50,
			MaxTeamVMs:       0,
			DefaultDisk:      25 * gb,
			MaxDisk:          75 * gb,
			DefaultBandwidth: 100 * gb,
		},
		Entitlements: nil,
	},
	"trial:default:monthly:20260106": {
		ID:           "trial:default:monthly:20260106",
		Category:     CategoryTrial,
		Name:         "Standard",
		StripePrices: map[string]stripePriceInfo{},
		Quotas: tierQuotas{
			MaxCPUs: 0, MaxMemory: 0,
			MaxUserVMs:       50,
			MaxTeamVMs:       0,
			DefaultDisk:      25 * gb,
			MaxDisk:          75 * gb,
			DefaultBandwidth: 100 * gb,
		},
		Entitlements: nil,
	},
	"basic:default:monthly:20260106": {
		ID:           "basic:default:monthly:20260106",
		Category:     CategoryBasic,
		Name:         "Standard",
		StripePrices: map[string]stripePriceInfo{},
		Quotas: tierQuotas{
			MaxCPUs: 0, MaxMemory: 0,
			MaxUserVMs:       0,
			MaxTeamVMs:       0,
			DefaultDisk:      25 * gb,
			MaxDisk:          0,
			DefaultBandwidth: 100 * gb,
		},
		Entitlements: nil,
	},
	"restricted:default:monthly:20260106": {
		ID:           "restricted:default:monthly:20260106",
		Category:     CategoryRestricted,
		Name:         "Standard",
		StripePrices: map[string]stripePriceInfo{},
		Quotas: tierQuotas{
			MaxCPUs: 0, MaxMemory: 0,
			MaxUserVMs:       0,
			MaxTeamVMs:       0,
			DefaultDisk:      25 * gb,
			MaxDisk:          0,
			DefaultBandwidth: 0,
		},
		Entitlements: nil,
	},
}

// GetTierByID returns the Tier for a given tier ID.
// Handles 4-part tier IDs ("individual:medium:monthly:20260106") as well as
// 3-part legacy plan IDs ("individual:monthly:20260106").
// Returns an error for completely unknown IDs.
func GetTierByID(id string) (Tier, error) {
	return getTierByID(id)
}

func getTierByID(id string) (Tier, error) {
	if tier, ok := tiers[id]; ok {
		return tier, nil
	}
	// Extract the category from the ID and look up the plan's DefaultTier.
	// This handles:
	//   - 3-part legacy IDs: "individual:monthly:20260106"
	//   - Bare category strings: "individual", "friend", "basic"
	//   - Any other unknown format
	cat := Base(id)
	if plan, ok := Get(cat); ok && plan.DefaultTier != "" {
		if tier, ok := tiers[plan.DefaultTier]; ok {
			return tier, nil
		}
	}
	return Tier{}, fmt.Errorf("unknown tier ID %q", id)
}

// parseTierID extracts the plan category, tier name, interval, and version
// from a 4-part tier ID (e.g. "individual:medium:monthly:20260106").
// Returns empty strings for any missing fields.
func parseTierID(id string) (category Category, tierName, interval, version string) {
	parts := strings.SplitN(id, ":", 4)
	switch len(parts) {
	case 4:
		return Category(parts[0]), parts[1], parts[2], parts[3]
	case 3:
		// 3-part legacy ID — treat as plan-level, no tier name
		return Category(parts[0]), "", parts[1], parts[2]
	case 2:
		return Category(parts[0]), "", parts[1], ""
	default:
		return Category(id), "", "", ""
	}
}

// effectiveEntitlements returns the entitlements for a tier, inheriting
// from the parent plan when the tier has no override.
func effectiveEntitlements(tier Tier) map[Entitlement]bool {
	if tier.Entitlements != nil {
		return *tier.Entitlements
	}
	plan, ok := Get(tier.Category)
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

// TierIDFromStripePriceKey returns the tier ID for a given Stripe price lookup key.
// Falls back to the individual Small tier for unknown keys.
func TierIDFromStripePriceKey(key string) string {
	for id, tier := range tiers {
		for _, price := range tier.StripePrices {
			if price.LookupKey == key {
				return id
			}
		}
	}
	return "individual:small:monthly:20260106"
}
