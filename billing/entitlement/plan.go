package entitlement

import (
	"context"
	"strings"
	"time"

	"exe.dev/exedb"
	"exe.dev/stage"
)

// PlanCategory identifies a billing plan.
type PlanCategory string

// StripePriceInfo contains Stripe price metadata for a plan.
type StripePriceInfo struct {
	LookupKey string
	Model     string
	Interval  string
}

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

	// StripePrices maps billing option keys ("monthly", "annual", "usage-disk", "usage-bandwidth")
	// to Stripe price metadata.
	StripePrices map[string]StripePriceInfo

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

	// MonthlyLLMCreditUSD is the monthly LLM credit allowance in USD.
	// This is the amount refreshed at the start of each month.
	MonthlyLLMCreditUSD float64

	// MaxUserVMs is the maximum number of VMs an individual user can create.
	MaxUserVMs int

	// MaxTeamVMs is the maximum number of VMs a team can create.
	MaxTeamVMs int

	// TrialDays is the number of days of trial access granted for this plan.
	TrialDays int

	// MaxMemory is the maximum memory in bytes per VM.
	// 0 means use stage.Env.DefaultMemory as the fallback.
	MaxMemory uint64

	// MaxDisk is the maximum disk in bytes per VM.
	// 0 means use stage.Env.DefaultDisk as the fallback.
	MaxDisk uint64

	// MaxCPUs is the maximum number of CPUs per VM.
	// 0 means use stage.Env.DefaultCPUs as the fallback.
	MaxCPUs uint64
}

// QuotaContext provides the context needed to resolve quotas for a specific request.
type QuotaContext struct {
	// UserLimits contains per-user override values from user.limits JSON.
	UserLimits *UserLimits

	// TeamLimits contains per-team override values from team.limits JSON.
	TeamLimits *UserLimits

	// InTeam indicates whether this request is in team context.
	InTeam bool

	// Env provides environment defaults for memory, disk, CPU.
	// Pass nil to use hard-coded fallbacks.
	Env *stage.Env
}

// UserLimits represents per-user or per-team resource limit overrides.
// All fields are optional; when nil/zero, plan or default limits apply.
type UserLimits struct {
	MaxBoxes  int    `json:"max_boxes,omitempty"`  // Max number of VMs
	MaxMemory uint64 `json:"max_memory,omitempty"` // Max memory in bytes
	MaxDisk   uint64 `json:"max_disk,omitempty"`   // Max disk in bytes
	MaxCPUs   uint64 `json:"max_cpus,omitempty"`   // Max number of CPUs
}

// ResolvedQuotas contains single resolved quota values for a specific context.
// Unlike PlanQuotas which has MaxUserVMs/MaxTeamVMs, this has one MaxVMs value.
type ResolvedQuotas struct {
	MaxVMs    int    // Max VMs (already resolved for user vs team)
	TrialDays int    // Trial period in days
	MaxMemory uint64 // Max memory in bytes per VM
	MaxDisk   uint64 // Max disk in bytes per VM
	MaxCPUs   uint64 // Max number of CPUs per VM
}

// PlanQuotas returns the base quota values for this plan without applying
// any per-user or per-team overrides. Use this to access plan defaults like
// SignupBonusCreditUSD or MonthlyLLMCreditUSD.
func (p Plan) PlanQuotas() PlanQuotas {
	return p.Quotas
}

// GetQuotas resolves the effective quota values for a specific context by applying
// the precedence: override > plan > default.
// This is the primary API for getting quotas - it returns single resolved values
// instead of requiring callers to choose between user/team variants.
func (p Plan) GetQuotas(ctx QuotaContext) ResolvedQuotas {
	quotas := ResolvedQuotas{
		TrialDays: p.Quotas.TrialDays,
	}

	// Determine which plan limit applies (user vs team)
	var planMaxVMs int
	if ctx.InTeam {
		planMaxVMs = p.Quotas.MaxTeamVMs
	} else {
		planMaxVMs = p.Quotas.MaxUserVMs
	}

	// Determine which override limit applies (user vs team)
	var effectiveLimits *UserLimits
	if ctx.InTeam && ctx.TeamLimits != nil {
		effectiveLimits = ctx.TeamLimits
	} else if !ctx.InTeam && ctx.UserLimits != nil {
		effectiveLimits = ctx.UserLimits
	}

	// MaxVMs: override > plan > default
	if effectiveLimits != nil && effectiveLimits.MaxBoxes > 0 {
		quotas.MaxVMs = effectiveLimits.MaxBoxes
	} else if planMaxVMs > 0 {
		quotas.MaxVMs = planMaxVMs
	} else {
		// Fall back to stage defaults
		if ctx.InTeam {
			quotas.MaxVMs = stage.DefaultMaxTeamBoxes
		} else {
			quotas.MaxVMs = stage.DefaultMaxBoxes
		}
	}

	// MaxMemory: override > plan > env default
	if effectiveLimits != nil && effectiveLimits.MaxMemory > 0 {
		quotas.MaxMemory = effectiveLimits.MaxMemory
	} else if p.Quotas.MaxMemory > 0 {
		quotas.MaxMemory = p.Quotas.MaxMemory
	} else if ctx.Env != nil {
		quotas.MaxMemory = max(ctx.Env.DefaultMemory, uint64(stage.MinMemory))
	} else {
		quotas.MaxMemory = uint64(stage.MinMemory)
	}

	// MaxDisk: override > plan > env default
	if effectiveLimits != nil && effectiveLimits.MaxDisk > 0 {
		quotas.MaxDisk = effectiveLimits.MaxDisk
	} else if p.Quotas.MaxDisk > 0 {
		quotas.MaxDisk = p.Quotas.MaxDisk
	} else if ctx.Env != nil {
		quotas.MaxDisk = max(ctx.Env.DefaultDisk, uint64(stage.MinDisk))
	} else {
		quotas.MaxDisk = uint64(stage.MinDisk)
	}

	// MaxCPUs: override > plan > env default
	if effectiveLimits != nil && effectiveLimits.MaxCPUs > 0 {
		quotas.MaxCPUs = effectiveLimits.MaxCPUs
	} else if p.Quotas.MaxCPUs > 0 {
		quotas.MaxCPUs = p.Quotas.MaxCPUs
	} else if ctx.Env != nil {
		quotas.MaxCPUs = max(ctx.Env.DefaultCPUs, uint64(stage.MinCPUs))
	} else {
		quotas.MaxCPUs = uint64(stage.MinCPUs)
	}

	return quotas
}

var plans = map[PlanCategory]Plan{
	CategoryVIP: {
		ID:        "vip",
		Available: true,
		Category:  CategoryVIP,
		Name:      "VIP",
		Entitlements: map[Entitlement]bool{
			All: true,
		},
		Quotas: PlanQuotas{
			MonthlyLLMCreditUSD: 500.0,
		},
	},
	CategoryEnterprise: {
		ID:        "enterprise:monthly:20260106",
		Available: true,
		Category:  CategoryEnterprise,
		Paid:      true,
		Name:      "Enterprise",
		Entitlements: map[Entitlement]bool{
			LLMUse:         true,
			CreditPurchase: true,
			InviteRequest:  true,
			VMCreate:       true,
			VMConnect:      true,
			VMRun:          true,
		},
		Quotas: PlanQuotas{
			MonthlyLLMCreditUSD: 500.0,
		},
	},
	CategoryTeam: {
		ID:        "team:monthly:20260106",
		Available: true,
		Category:  CategoryTeam,
		Paid:      true,
		Name:      "Team",
		StripePrices: map[string]StripePriceInfo{
			"monthly":         {LookupKey: "team:monthly:20260106", Model: "subscription", Interval: "monthly"},
			"annual":          {LookupKey: "team:annual:20260106", Model: "subscription", Interval: "annual"},
			"usage-disk":      {LookupKey: "team:usage-disk:20260106", Model: "metered", Interval: ""},
			"usage-bandwidth": {LookupKey: "team:usage-bandwidth:20260106", Model: "metered", Interval: ""},
		},
		Entitlements: map[Entitlement]bool{
			LLMUse:         true,
			CreditPurchase: true,
			InviteRequest:  true,
			VMCreate:       true,
			VMConnect:      true,
			VMRun:          true,
		},
		Quotas: PlanQuotas{
			MonthlyLLMCreditUSD: 500.0,
		},
	},
	CategoryIndividual: {
		ID:        "individual:monthly:20260106",
		Available: true,
		Category:  CategoryIndividual,
		Paid:      true,
		Name:      "Individual",
		StripePrices: map[string]StripePriceInfo{
			"monthly":         {LookupKey: "individual", Model: "subscription", Interval: "monthly"},
			"annual":          {LookupKey: "individual:annual:20260106", Model: "subscription", Interval: "annual"},
			"usage-disk":      {LookupKey: "individual:usage-disk:20260106", Model: "metered", Interval: ""},
			"usage-bandwidth": {LookupKey: "individual:usage-bandwidth:20260106", Model: "metered", Interval: ""},
		},
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
			MonthlyLLMCreditUSD:  100.0,
		},
	},
	CategoryFriend: {
		ID:        "friend",
		Available: true,
		Category:  CategoryFriend,
		Name:      "Friend",
		Entitlements: map[Entitlement]bool{
			LLMUse:    true,
			VMCreate:  true,
			VMConnect: true,
			VMRun:     true,
		},
		Quotas: PlanQuotas{
			MonthlyLLMCreditUSD: 500.0,
		},
	},
	CategoryGrandfathered: {
		ID:        "grandfathered",
		Available: true,
		Category:  CategoryGrandfathered,
		Name:      "Grandfathered",
		Entitlements: map[Entitlement]bool{
			LLMUse:    true,
			VMCreate:  true,
			VMConnect: true,
			VMRun:     true,
		},
		Quotas: PlanQuotas{
			MonthlyLLMCreditUSD: 0,
		},
	},
	CategoryTrial: {
		ID:        "trial:monthly:20260106",
		Available: true,
		Category:  CategoryTrial,
		Name:      "Trial",
		Entitlements: map[Entitlement]bool{
			LLMUse:    true,
			VMCreate:  true,
			VMConnect: true,
			VMRun:     true,
		},
		Quotas: PlanQuotas{
			MonthlyLLMCreditUSD: 0,
		},
	},
	CategoryBasic: {
		ID:        "basic:monthly:20260106",
		Available: true,
		Category:  CategoryBasic,
		Name:      "Basic",
		Entitlements: map[Entitlement]bool{
			LLMUse:    true,
			VMConnect: true,
		},
		Quotas: PlanQuotas{
			MonthlyLLMCreditUSD: 0,
		},
	},
	CategoryRestricted: {
		ID:           "restricted",
		Available:    true,
		Category:     CategoryRestricted,
		Name:         "Restricted",
		Entitlements: map[Entitlement]bool{},
		Quotas: PlanQuotas{
			MonthlyLLMCreditUSD: 0,
		},
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
	// BillingStatus is the subscription status: "active", "canceled", or "".
	BillingStatus string

	// PlanID is the active plan ID from account_plans (e.g., "trial:monthly:20260106", "friend", "free").
	PlanID *string

	// TrialExpiresAt is when the trial expires, if the plan is a trial.
	TrialExpiresAt *time.Time

	// CreatedAt is when the user account was created.
	CreatedAt *time.Time

	// HasExplicitOverrides indicates VIP-style per-user overrides exist.
	HasExplicitOverrides bool

	// TeamBillingActive is true when the user's team billing owner has active billing.
	TeamBillingActive bool
}

// PlanStripePriceInfo returns the StripePriceInfo for a given plan version and billing option.
// Returns an empty StripePriceInfo if the plan or billing option is not found.
func PlanStripePriceInfo(version PlanCategory, billingOption string) StripePriceInfo {
	p, ok := plans[version]
	if !ok {
		return StripePriceInfo{}
	}
	info, ok := p.StripePrices[billingOption]
	if !ok {
		return StripePriceInfo{}
	}
	return info
}

// GetPlanCategory maps existing billing state to a PlanCategory.
func GetPlanCategory(inputs UserPlanInputs) PlanCategory {
	// Canceled users go straight to Basic — canceling overrides
	// grandfathered status, exemptions, and trial access.
	if inputs.BillingStatus == "canceled" {
		return CategoryBasic
	}

	// VIP: identified by explicit overrides flag (from plan_id like 'vip:%').
	if inputs.HasExplicitOverrides {
		return CategoryVIP
	}

	// Friend: plan_id is "friend" or "free".
	if inputs.PlanID != nil && (*inputs.PlanID == "friend" || *inputs.PlanID == "free") {
		return CategoryFriend
	}

	// Team: user is on a team whose billing owner has active billing.
	if inputs.TeamBillingActive {
		return CategoryTeam
	}

	// Individual: has active billing (not on a team).
	if inputs.BillingStatus == "active" {
		return CategoryIndividual
	}

	// Trial: plan_id starts with "trial:" and hasn't expired.
	if inputs.PlanID != nil && strings.HasPrefix(*inputs.PlanID, "trial:") {
		if inputs.TrialExpiresAt != nil && time.Now().Before(*inputs.TrialExpiresAt) {
			return CategoryTrial
		}
	}

	// Grandfathered: created before the billing-required date.
	if inputs.CreatedAt != nil && inputs.CreatedAt.Before(billingRequiredDate) {
		return CategoryGrandfathered
	}

	return CategoryBasic
}

// PlanDataQuerier abstracts the database query needed by GetPlanForUser.
type PlanDataQuerier interface {
	GetUserPlanData(ctx context.Context, userID string) (exedb.GetUserPlanDataRow, error)
}

// GetPlanForUser returns the user's plan category by querying the database
// and applying all billing logic in one function. This replaces the pattern
// of manually constructing UserPlanInputs and calling GetPlanCategory.
//
// This is the preferred way to determine a user's plan. It encapsulates:
//   - Querying account_plans for trial/friend/free status
//   - Checking billing_events for active/canceled status
//   - Checking team membership and team billing owner status
//   - Applying grandfathered status for old accounts
//   - Handling trial expiration logic
//
// Returns sql.ErrNoRows if the user doesn't exist.
func GetPlanForUser(ctx context.Context, q PlanDataQuerier, userID string) (PlanCategory, error) {
	row, err := q.GetUserPlanData(ctx, userID)
	if err != nil {
		return "", err
	}

	// Construct inputs for GetPlanCategory
	inputs := UserPlanInputs{
		BillingStatus:        row.BillingStatus,
		PlanID:               row.PlanID,
		TrialExpiresAt:       row.TrialExpiresAt,
		CreatedAt:            row.CreatedAt,
		HasExplicitOverrides: row.HasExplicitOverrides != 0,
		TeamBillingActive:    row.TeamBillingActive != 0,
	}

	return GetPlanCategory(inputs), nil
}

// DeriveExemptionDisplay returns a human-readable billing exemption string
// for display purposes (debug UI, logs). This is NOT used for plan decisions.
// Returns "free", "trial", or empty string.
func DeriveExemptionDisplay(planID *string) string {
	if planID == nil {
		return ""
	}
	if *planID == "friend" || *planID == "free" {
		return "free"
	}
	if strings.HasPrefix(*planID, "trial:") {
		return "trial"
	}
	return ""
}
