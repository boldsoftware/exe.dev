package entitlement

import (
	"strings"
	"time"
)

// PlanVersion identifies a billing plan.
type PlanVersion string

// Plan version constants.
const (
	VersionVIP           PlanVersion = "vip"
	VersionTeam          PlanVersion = "team"
	VersionIndividual    PlanVersion = "individual"
	VersionFriend        PlanVersion = "friend"
	VersionGrandfathered PlanVersion = "grandfathered"
	VersionTrial         PlanVersion = "trial"
	VersionBasic         PlanVersion = "basic"
	VersionRestricted    PlanVersion = "restricted"
)

// Plan describes a billing plan and the entitlements it grants.
type Plan struct {
	// Version is the unique identifier for this plan (e.g., "individual", "vip").
	Version PlanVersion

	// Name is the human-readable display name.
	Name string

	// LLMGatewayCategory determines credit refresh behavior in the LLM gateway.
	// Values: "has_billing", "friend", "no_billing".
	LLMGatewayCategory string

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
		Version:            VersionVIP,
		Name:               "VIP",
		LLMGatewayCategory: "friend",
		Entitlements: map[Entitlement]bool{
			All: true,
		},
	},
	VersionTeam: {
		Version:            VersionTeam,
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
	VersionIndividual: {
		Version:            VersionIndividual,
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
	VersionFriend: {
		Version:            VersionFriend,
		Name:               "Friend",
		LLMGatewayCategory: "friend",
		Entitlements: map[Entitlement]bool{
			LLMUse:    true,
			VMCreate:  true,
			VMConnect: true,
			VMRun:     true,
		},
	},
	VersionGrandfathered: {
		Version:            VersionGrandfathered,
		Name:               "Grandfathered",
		LLMGatewayCategory: "no_billing",
		Entitlements: map[Entitlement]bool{
			LLMUse:    true,
			VMCreate:  true,
			VMConnect: true,
			VMRun:     true,
		},
	},
	VersionTrial: {
		Version:            VersionTrial,
		Name:               "Trial",
		LLMGatewayCategory: "no_billing",
		Entitlements: map[Entitlement]bool{
			LLMUse:    true,
			VMCreate:  true,
			VMConnect: true,
			VMRun:     true,
		},
	},
	VersionBasic: {
		Version:            VersionBasic,
		Name:               "Basic",
		LLMGatewayCategory: "no_billing",
		Entitlements: map[Entitlement]bool{
			LLMUse:    true,
			VMConnect: true,
		},
	},
	VersionRestricted: {
		Version:            VersionRestricted,
		Name:               "Restricted",
		LLMGatewayCategory: "no_billing",
		Entitlements:       map[Entitlement]bool{},
	},
}

// GetPlan returns the Plan for a given version and whether it exists.
func GetPlan(version PlanVersion) (Plan, bool) {
	p, ok := plans[version]
	return p, ok
}

// ParsePlanID extracts the base plan, interval, and version from a plan ID.
// Versioned IDs use the format "{plan}:{interval}:{YYYYMMDD}" (e.g.
// "individual:monthly:20260325"). Bare legacy IDs ("individual") are
// handled gracefully: the base plan is the ID itself, interval and version
// are empty strings.
//
// ParsePlanID is the single code path for extracting the base plan from
// any plan_id value stored in account_plans.
func ParsePlanID(id string) (plan PlanVersion, interval, version string) {
	parts := strings.SplitN(id, ":", 3)
	switch len(parts) {
	case 3:
		return PlanVersion(parts[0]), parts[1], parts[2]
	default:
		return PlanVersion(id), "", ""
	}
}

// FormatPlanID constructs a versioned plan ID from its components.
// The result has the format "{plan}:{interval}:{version}".
func FormatPlanID(plan PlanVersion, interval, version string) string {
	return string(plan) + ":" + interval + ":" + version
}

// VersionedPlanID returns a versioned plan ID using the given plan, interval,
// and the provided timestamp formatted as YYYYMMDD.
func VersionedPlanID(plan PlanVersion, interval string, t time.Time) string {
	return FormatPlanID(plan, interval, t.UTC().Format("20060102"))
}

// BasePlan extracts the base PlanVersion from a possibly-versioned plan ID.
// This is a convenience wrapper around ParsePlanID.
func BasePlan(id string) PlanVersion {
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

	// Team: user is on a team whose billing owner has active billing.
	if inputs.TeamBillingActive {
		return VersionTeam
	}

	// Individual: has active billing (not on a team).
	if inputs.Category == "has_billing" {
		return VersionIndividual
	}

	// Trial: trial exemption that hasn't expired.
	if inputs.BillingExemption != nil && *inputs.BillingExemption == "trial" {
		if inputs.BillingTrialEndsAt != nil && time.Now().Before(*inputs.BillingTrialEndsAt) {
			return VersionTrial
		}
	}

	// Grandfathered: created before the billing-required date.
	if inputs.CreatedAt != nil && inputs.CreatedAt.Before(billingRequiredDate) {
		return VersionGrandfathered
	}

	return VersionBasic
}
