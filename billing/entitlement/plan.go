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
		Quotas: PlanQuotas{
			SignupBonusCreditUSD: 100.0,
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

// GetPlan returns the Plan for a given version and whether it exists.
func GetPlan(version PlanVersion) (Plan, bool) {
	p, ok := plans[version]
	return p, ok
}

// PlanName returns the human-readable name for a plan version (e.g., "Individual").
// Returns empty string for unknown plan versions.
func PlanName(version PlanVersion) string {
	p, ok := plans[version]
	if !ok {
		return ""
	}
	return p.Name
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

	// TeamBillingActive is true when the user's team billing owner has active billing.
	TeamBillingActive bool
}

// GetPlanVersion maps existing billing state to a PlanVersion.
func GetPlanVersion(inputs UserPlanInputs) PlanVersion {
	// Canceled users go straight to Basic — canceling overrides
	// grandfathered status, exemptions, and trial access.
	if inputs.BillingStatus == "canceled" {
		return VersionBasic
	}

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

	// Team: user has no individual plan but their team billing owner covers them.
	if inputs.TeamBillingActive {
		return VersionTeam
	}

	return VersionBasic
}
