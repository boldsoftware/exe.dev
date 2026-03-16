package entitlement

import "time"

// PlanVersion identifies a billing plan.
type PlanVersion string

// Plan version constants.
const (
	VersionVIP           PlanVersion = "vip"
	VersionTeam          PlanVersion = "team"
	VersionIndividual    PlanVersion = "individual"
	VersionFriend        PlanVersion = "friend"
	VersionGrandfathered PlanVersion = "grandfathered"
	VersionInvite        PlanVersion = "invite"
	VersionBasic         PlanVersion = "basic"
)

// Plan describes a billing plan and the entitlements it grants.
type Plan struct {
	// Version is the unique identifier for this plan (e.g., "individual", "vip").
	Version PlanVersion

	// Name is the human-readable display name.
	Name string

	// Entitlements is the set of capabilities this plan grants.
	Entitlements map[Entitlement]bool
}

var plans = map[PlanVersion]Plan{
	VersionVIP: {
		Version: VersionVIP,
		Name:    "VIP",
		Entitlements: map[Entitlement]bool{
			All: true,
		},
	},
	VersionTeam: {
		Version: VersionTeam,
		Name:    "Team",
		Entitlements: map[Entitlement]bool{
			LLMUse:          true,
			CreditRenew:     true,
			CreditPurchase:  true,
			CreditRefresh:   true,
			VMCreate:        true,
			VMConnect:       true,
			ComputeSpend:    true,
			ComputePurchase: true,
			ComputeDebt:     true,
		},
	},
	VersionIndividual: {
		Version: VersionIndividual,
		Name:    "Individual",
		Entitlements: map[Entitlement]bool{
			LLMUse:          true,
			CreditRenew:     true,
			CreditPurchase:  true,
			CreditRefresh:   true,
			VMCreate:        true,
			VMConnect:       true,
			ComputeSpend:    true,
			ComputePurchase: true,
			ComputeDebt:     true,
		},
	},
	VersionFriend: {
		Version: VersionFriend,
		Name:    "Friend",
		Entitlements: map[Entitlement]bool{
			LLMUse:        true,
			CreditRefresh: true,
			VMCreate:      true,
			VMConnect:     true,
			ComputeSpend:  true,
		},
	},
	VersionGrandfathered: {
		Version: VersionGrandfathered,
		Name:    "Grandfathered",
		Entitlements: map[Entitlement]bool{
			LLMUse:        true,
			CreditRefresh: true,
			VMCreate:      true,
			VMConnect:     true,
			ComputeSpend:  true,
		},
	},
	VersionInvite: {
		Version: VersionInvite,
		Name:    "Invite",
		Entitlements: map[Entitlement]bool{
			LLMUse:        true,
			CreditRefresh: true,
			VMCreate:      true,
			VMConnect:     true,
			ComputeSpend:  true,
		},
	},
	VersionBasic: {
		Version: VersionBasic,
		Name:    "Basic",
		Entitlements: map[Entitlement]bool{
			LLMUse:        true,
			CreditRefresh: true,
			VMConnect:     true,
		},
	},
}

// PlanGrants reports whether the given plan version grants the specified entitlement.
// Returns false for unknown plan versions.
func PlanGrants(version PlanVersion, ent Entitlement) bool {
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
}

// GetPlanVersion maps existing billing state to a PlanVersion.
func GetPlanVersion(inputs UserPlanInputs) PlanVersion {
	// VIP: friend category with explicit overrides.
	if inputs.Category == "friend" && inputs.HasExplicitOverrides {
		return VersionVIP
	}

	// Friend: friend category without overrides.
	if inputs.Category == "friend" {
		return VersionFriend
	}

	// Individual: has active billing.
	if inputs.Category == "has_billing" {
		return VersionIndividual
	}

	// Invite: trial exemption that hasn't expired.
	if inputs.BillingExemption != nil && *inputs.BillingExemption == "trial" {
		if inputs.BillingTrialEndsAt != nil && time.Now().Before(*inputs.BillingTrialEndsAt) {
			return VersionInvite
		}
	}

	// Grandfathered: created before the billing-required date.
	if inputs.CreatedAt != nil && inputs.CreatedAt.Before(billingRequiredDate) {
		return VersionGrandfathered
	}

	return VersionBasic
}
