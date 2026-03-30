package entitlement

import (
	"strings"
	"time"
)

// PlanCategory identifies a billing plan.
type PlanCategory string

// Plan category constants.
const (
	CategoryVIP           PlanCategory = "vip"
	CategoryEnterprise    PlanCategory = "enterprise"
	CategoryTeam          PlanCategory = "team"
	CategoryIndividual    PlanCategory = "individual"
	CategoryFriend        PlanCategory = "friend"
	CategoryGrandfathered PlanCategory = "grandfathered"
	CategoryTrial         PlanCategory = "trial"
	CategoryBasic         PlanCategory = "basic"
	CategoryRestricted    PlanCategory = "restricted"
)

// Plan describes a billing plan and the entitlements it grants.
type Plan struct {
	// ID is the stable identifier stored in account_plans.plan_id
	// (e.g. "individual:monthly:20260106"). For plans without a billing
	// interval this is the same as string(Category).
	ID string

	// Category is the base plan identifier (e.g., "individual", "vip").
	Category PlanCategory

	// Available indicates whether this plan can be assigned to new accounts.
	Available bool

	// Name is the human-readable display name.
	Name string

	// LLMGatewayCategory determines credit refresh behavior in the LLM gateway.
	// Values: "has_billing", "friend", "no_billing".
	LLMGatewayCategory string

	// Entitlements is the set of capabilities this plan grants.
	Entitlements map[Entitlement]bool

	// Paid indicates whether this plan represents active paid billing.
	Paid bool

	// Quotas contains numeric limits and amounts tied to this plan.
	Quotas PlanQuotas
}

// PlanQuotas holds numeric limits and one-time grants for a plan.
type PlanQuotas struct {
	// SignupBonusCreditUSD is the one-time credit (in USD) granted when a user
	// first signs up on this plan. Only Individual gets a bonus; VIP, Friend,
	// and Grandfathered intentionally have 0 because they use the "friend"
	// category and never hit the upgrade bonus code path.
	SignupBonusCreditUSD float64
}

var plans = map[PlanCategory]Plan{
	CategoryVIP: {
		ID:                 "vip",
		Available:          true,
		Category:           CategoryVIP,
		Name:               "VIP",
		LLMGatewayCategory: "friend",
		Entitlements: map[Entitlement]bool{
			All: true,
		},
	},
	CategoryEnterprise: {
		ID:                 "enterprise:monthly:20260106",
		Available:          true,
		Category:           CategoryEnterprise,
		Paid:               true,
		Name:               "Enterprise",
		LLMGatewayCategory: "has_billing",
		Entitlements: map[Entitlement]bool{
			LLMUse:         true,
			CreditPurchase: true,
			InviteRequest:  true,
			VMCreate:       true,
			VMConnect:      true,
			VMRun:          true,
		},
	},
	CategoryTeam: {
		ID:                 "team:monthly:20260106",
		Available:          true,
		Category:           CategoryTeam,
		Paid:               true,
		Name:               "Team",
		LLMGatewayCategory: "has_billing",
		Entitlements: map[Entitlement]bool{
			LLMUse:         true,
			CreditPurchase: true,
			InviteRequest:  true,
			VMCreate:       true,
			VMConnect:      true,
			VMRun:          true,
		},
	},
	CategoryIndividual: {
		ID:                 "individual:monthly:20260106",
		Available:          true,
		Category:           CategoryIndividual,
		Paid:               true,
		Name:               "Individual",
		LLMGatewayCategory: "has_billing",
		Entitlements: map[Entitlement]bool{
			LLMUse:         true,
			CreditPurchase: true,
			InviteRequest:  true,
			TeamCreate:     true,
			VMCreate:       true,
			VMConnect:      true,
			VMRun:          true,
		},
		Quotas: PlanQuotas{
			SignupBonusCreditUSD: 100.0,
		},
	},
	CategoryFriend: {
		ID:                 "friend",
		Available:          true,
		Category:           CategoryFriend,
		Name:               "Friend",
		LLMGatewayCategory: "friend",
		Entitlements: map[Entitlement]bool{
			LLMUse:    true,
			VMCreate:  true,
			VMConnect: true,
			VMRun:     true,
		},
	},
	CategoryGrandfathered: {
		ID:                 "grandfathered",
		Available:          true,
		Category:           CategoryGrandfathered,
		Name:               "Grandfathered",
		LLMGatewayCategory: "no_billing",
		Entitlements: map[Entitlement]bool{
			LLMUse:    true,
			VMCreate:  true,
			VMConnect: true,
			VMRun:     true,
		},
	},
	CategoryTrial: {
		ID:                 "trial:monthly:20260106",
		Available:          true,
		Category:           CategoryTrial,
		Name:               "Trial",
		LLMGatewayCategory: "no_billing",
		Entitlements: map[Entitlement]bool{
			LLMUse:    true,
			VMCreate:  true,
			VMConnect: true,
			VMRun:     true,
		},
	},
	CategoryBasic: {
		ID:                 "basic:monthly:20260106",
		Available:          true,
		Category:           CategoryBasic,
		Name:               "Basic",
		LLMGatewayCategory: "no_billing",
		Entitlements: map[Entitlement]bool{
			LLMUse:    true,
			VMConnect: true,
		},
	},
	CategoryRestricted: {
		ID:                 "restricted",
		Available:          true,
		Category:           CategoryRestricted,
		Name:               "Restricted",
		LLMGatewayCategory: "no_billing",
		Entitlements:       map[Entitlement]bool{},
	},
}

// AllPlans returns all plans in a stable display order.
func AllPlans() []Plan {
	order := []PlanCategory{
		CategoryVIP,
		CategoryEnterprise,
		CategoryTeam,
		CategoryIndividual,
		CategoryFriend,
		CategoryGrandfathered,
		CategoryTrial,
		CategoryBasic,
		CategoryRestricted,
	}
	result := make([]Plan, 0, len(order))
	for _, v := range order {
		if p, ok := plans[v]; ok {
			result = append(result, p)
		}
	}
	return result
}

// GetPlan returns the Plan for a given category.
// Returns false if the category is unknown or the plan is not active.
func GetPlan(cat PlanCategory) (Plan, bool) {
	p, ok := plans[cat]
	if !ok || !p.Available {
		return Plan{}, false
	}
	return p, true
}

// ParsePlanID extracts the base plan, interval, and version from a plan ID.
// Versioned IDs use the format "{plan}:{interval}:{YYYYMMDD}" (e.g.
// "individual:monthly:20260325"). Bare legacy IDs ("individual") are
// handled gracefully: the base plan is the ID itself, interval and version
// are empty strings.
//
// ParsePlanID is the single code path for extracting the base plan from
// any plan_id value stored in account_plans.
func ParsePlanID(id string) (plan PlanCategory, interval, version string) {
	parts := strings.SplitN(id, ":", 3)
	switch len(parts) {
	case 3:
		return PlanCategory(parts[0]), parts[1], parts[2]
	default:
		return PlanCategory(id), "", ""
	}
}

// PlanID returns the stable plan ID for a given version.
// Panics if the version is unknown — all valid versions are in the plan map.
func PlanID(v PlanCategory) string {
	p, ok := plans[v]
	if !ok {
		panic("unknown plan version: " + string(v))
	}
	return p.ID
}

// BasePlan extracts the base PlanCategory from a possibly-versioned plan ID.
// This is a convenience wrapper around ParsePlanID.
func BasePlan(id string) PlanCategory {
	plan, _, _ := ParsePlanID(id)
	return plan
}

// GetPlanByID returns the Plan for a given plan ID string and whether it exists.
// It handles both versioned IDs ("individual:monthly:20260325") and bare
// legacy IDs ("individual") by extracting the base plan via ParsePlanID.
func GetPlanByID(id string) (Plan, bool) {
	plan := BasePlan(id)
	return GetPlan(plan)
}

// PlanName returns the human-readable name for a plan version (e.g., "Individual").
// Returns empty string for unknown plan versions.
func PlanName(version PlanCategory) string {
	p, ok := plans[version]
	if !ok {
		return ""
	}
	return p.Name
}

// PlanIsPaid reports whether the plan represents active paid billing.
func PlanIsPaid(version PlanCategory) bool {
	p, ok := plans[version]
	return ok && p.Paid
}

// PlanGrants reports whether the given plan version grants the specified entitlement.
// Returns false for unknown plan versions.
func PlanGrants(version PlanCategory, ent Entitlement) bool {
	p, ok := plans[version]
	if !ok {
		return false
	}
	if p.Entitlements[All] {
		return true
	}
	return p.Entitlements[ent]
}

// billingRequiredDate is the cutoff: users created before this are grandfathered.
// Duplicated from execore/billing_status.go to avoid importing execore.
var billingRequiredDate = time.Date(2026, 1, 6, 23, 10, 0, 0, time.UTC)

// UserPlanInputs captures the billing state needed to resolve a user's plan version.
// Uses plain Go types to avoid importing exedb or execore.
type UserPlanInputs struct {
	// Category is the result of GetUserPlanCategory: "has_billing", "no_billing", "friend".
	Category string

	// BillingStatus is the subscription status: "active", "canceled", or "".
	BillingStatus string

	// BillingExemption is the exemption type: "free", "trial", or nil.
	BillingExemption *string

	// CreatedAt is when the user account was created.
	CreatedAt *time.Time

	// BillingTrialEndsAt is when the trial expires, if applicable.
	BillingTrialEndsAt *time.Time

	// HasExplicitOverrides indicates VIP-style per-user overrides exist.
	HasExplicitOverrides bool

	// TeamBillingActive is true when the user's team billing owner has active billing.
	TeamBillingActive bool
}

// GetPlanCategory maps existing billing state to a PlanCategory.
func GetPlanCategory(inputs UserPlanInputs) PlanCategory {
	// Canceled users go straight to Basic — canceling overrides
	// grandfathered status, exemptions, and trial access.
	if inputs.BillingStatus == "canceled" {
		return CategoryBasic
	}

	// VIP: friend category with explicit overrides.
	if inputs.Category == "friend" && inputs.HasExplicitOverrides {
		return CategoryVIP
	}

	// Friend: friend category without overrides.
	if inputs.Category == "friend" {
		return CategoryFriend
	}

	// Team: user is on a team whose billing owner has active billing.
	if inputs.TeamBillingActive {
		return CategoryTeam
	}

	// Individual: has active billing (not on a team).
	if inputs.Category == "has_billing" {
		return CategoryIndividual
	}

	// Trial: trial exemption that hasn't expired.
	if inputs.BillingExemption != nil && *inputs.BillingExemption == "trial" {
		if inputs.BillingTrialEndsAt != nil && time.Now().Before(*inputs.BillingTrialEndsAt) {
			return CategoryTrial
		}
	}

	// Grandfathered: created before the billing-required date.
	if inputs.CreatedAt != nil && inputs.CreatedAt.Before(billingRequiredDate) {
		return CategoryGrandfathered
	}

	return CategoryBasic
}
