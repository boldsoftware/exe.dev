package plan

import (
	"context"
	"strings"
	"time"

	"exe.dev/exedb"
)

// Category identifies a billing plan.
type Category string

// Plan category constants.
const (
	CategoryEnterprise    Category = "enterprise"
	CategoryTeam          Category = "team"
	CategoryIndividual    Category = "individual"
	CategoryFriend        Category = "friend"
	CategoryGrandfathered Category = "grandfathered"
	CategoryTrial         Category = "trial"
	CategoryBasic         Category = "basic"
	CategoryRestricted    Category = "restricted"
)

const (
	ShortSignupTrialDays = 7
	InviteTrialDays      = 30
)

// Plan describes a billing plan category and the base entitlements it grants.
// Numeric quotas (memory, disk, CPUs, credit amounts) live in Tier — use
// getTierByID to look those up.
type Plan struct {
	// ID is the stable identifier stored in account_plans.plan_id
	// (e.g. "individual:monthly:20260106"). For plans without a billing
	// interval this is the same as string(Category).
	ID string

	// Category is the base plan identifier (e.g., "individual", "friend").
	Category Category

	// Available indicates whether this plan can be assigned to new accounts.
	Available bool

	// Name is the human-readable display name.
	Name string

	// Entitlements is the set of capabilities this plan grants.
	// Individual tiers inherit this set unless they have their own override.
	Entitlements map[Entitlement]bool

	// Paid indicates whether this plan represents active paid billing.
	Paid bool

	// TrialDays is optional plan-level trial duration metadata for flows that
	// derive the trial length from plan configuration.
	TrialDays int

	// SignupBonusCreditUSD is the one-time credit (in USD) granted when a user
	// first upgrades to this plan.
	SignupBonusCreditUSD float64

	// MonthlyLLMCreditUSD is the LLM credit (in USD) granted each billing period.
	MonthlyLLMCreditUSD float64

	// DefaultTier is the tier ID to use for plans that have a single tier.
	// For multi-tier plans (Individual), the tier is resolved from account_plans.plan_id.
	DefaultTier string
}

// UserLimits represents per-user or per-team resource limit overrides.
// All fields are optional; when nil/zero, plan or default limits apply.
type UserLimits struct {
	MaxBoxes  int    `json:"max_boxes,omitempty"`  // Max number of VMs
	MaxMemory uint64 `json:"max_memory,omitempty"` // Max memory in bytes
	MaxDisk   uint64 `json:"max_disk,omitempty"`   // Max disk in bytes
	MaxCPUs   uint64 `json:"max_cpus,omitempty"`   // Max number of CPUs
}

// DataQuerier abstracts the database query needed by ForUser.
type DataQuerier interface {
	GetUserPlanData(ctx context.Context, userID string) (exedb.GetUserPlanDataRow, error)
}

// AllPlans returns all plans sorted alphabetically by name.
func AllPlans() []Plan {
	result := make([]Plan, 0, len(plans))
	for _, p := range plans {
		result = append(result, p)
	}
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j].Name < result[j-1].Name; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

// Get returns the Plan for a given category.
// Returns false if the category is unknown or the plan is not active.
func Get(cat Category) (Plan, bool) {
	p, ok := plans[cat]
	if !ok || !p.Available {
		return Plan{}, false
	}
	return p, true
}

// ParseID extracts the base plan, interval, and version from a plan ID.
// Versioned IDs use the format "{plan}:{interval}:{YYYYMMDD}" (e.g.
// "individual:monthly:20260325"). Bare legacy IDs ("individual") are
// handled gracefully: the base plan is the ID itself, interval and version
// are empty strings.
//
// ParseID is the single code path for extracting the base plan from
// any plan_id value stored in account_plans.
func ParseID(id string) (plan Category, interval, version string) {
	parts := strings.SplitN(id, ":", 3)
	switch len(parts) {
	case 3:
		return Category(parts[0]), parts[1], parts[2]
	default:
		return Category(id), "", ""
	}
}

// ID returns the stable plan ID for a given version.
// Panics if the version is unknown — all valid versions are in the plan map.
func ID(v Category) string {
	p, ok := plans[v]
	if !ok {
		panic("unknown plan version: " + string(v))
	}
	return p.ID
}

// Base extracts the base Category from a possibly-versioned plan ID.
// This is a convenience wrapper around ParseID.
func Base(id string) Category {
	plan, _, _ := ParseID(id)
	return plan
}

// ByID returns the Plan for a given plan ID string and whether it exists.
// It handles both versioned IDs ("individual:monthly:20260325") and bare
// legacy IDs ("individual") by extracting the base plan via ParseID.
func ByID(id string) (Plan, bool) {
	plan := Base(id)
	return Get(plan)
}

// Name returns the human-readable name for a plan version (e.g., "Individual").
// Returns empty string for unknown plan versions.
func Name(version Category) string {
	p, ok := plans[version]
	if !ok {
		return ""
	}
	return p.Name
}

// IsPaid reports whether the plan represents active paid billing.
func IsPaid(version Category) bool {
	p, ok := plans[version]
	return ok && p.Paid
}

// ForUser returns the user's plan category by querying the database
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
func ForUser(ctx context.Context, q DataQuerier, userID string) (Category, error) {
	row, err := q.GetUserPlanData(ctx, userID)
	if err != nil {
		return "", err
	}

	inputs := userPlanInputs{
		BillingStatus:     row.BillingStatus,
		PlanID:            row.PlanID,
		TrialExpiresAt:    row.TrialExpiresAt,
		CreatedAt:         row.CreatedAt,
		TeamBillingActive: row.TeamBillingActive != 0,
	}

	return getPlanCategory(inputs), nil
}

// CategoryFromRow computes a user's plan category from a pre-fetched
// GetUserPlanDataRow. Useful for bulk computations where callers fetch
// plan data for many users in a single query.
func CategoryFromRow(row exedb.GetUserPlanDataRow) Category {
	inputs := userPlanInputs{
		BillingStatus:     row.BillingStatus,
		PlanID:            row.PlanID,
		TrialExpiresAt:    row.TrialExpiresAt,
		CreatedAt:         row.CreatedAt,
		TeamBillingActive: row.TeamBillingActive != 0,
	}
	return getPlanCategory(inputs)
}

// CategoryFromProductName returns the plan category for a Stripe product name.
// Returns false if the product name is not recognized.
func CategoryFromProductName(name string) (Category, bool) {
	switch strings.ToLower(name) {
	case "individual":
		return CategoryIndividual, true
	case "team":
		return CategoryTeam, true
	case "enterprise":
		return CategoryEnterprise, true
	default:
		return "", false
	}
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

// --- unexported ---

// stripePriceInfo contains Stripe price metadata for a plan.
type stripePriceInfo struct {
	LookupKey string
	Model     string
	Interval  string
}

// userPlanInputs captures the billing state needed to resolve a user's plan version.
// Uses plain Go types to avoid importing exedb or execore.
type userPlanInputs struct {
	// BillingStatus is the subscription status: "active", "canceled", or "".
	BillingStatus string

	// PlanID is the active plan ID from account_plans (e.g., "trial:monthly:20260106", "friend", "free").
	PlanID *string

	// TrialExpiresAt is when the trial expires, if the plan is a trial.
	TrialExpiresAt *time.Time

	// CreatedAt is when the user account was created.
	CreatedAt *time.Time

	// TeamBillingActive is true when the user's team billing owner has active billing.
	TeamBillingActive bool
}

var plans = map[Category]Plan{
	CategoryEnterprise: {
		ID:                  "enterprise:monthly:20260106",
		Available:           true,
		Category:            CategoryEnterprise,
		Paid:                true,
		Name:                "Enterprise",
		MonthlyLLMCreditUSD: 500,
		DefaultTier:         "enterprise:default:monthly:20260106",
		Entitlements: map[Entitlement]bool{
			LLMUse:         true,
			CreditPurchase: true,
			InviteRequest:  true,
			VMCreate:       true,
			VMRun:          true,
			DiskResize:     true,
		},
	},
	CategoryTeam: {
		ID:                  "team:monthly:20260106",
		Available:           true,
		Category:            CategoryTeam,
		Paid:                true,
		Name:                "Team",
		MonthlyLLMCreditUSD: 500,
		DefaultTier:         "team:default:monthly:20260106",
		Entitlements: map[Entitlement]bool{
			LLMUse:         true,
			CreditPurchase: true,
			InviteRequest:  true,
			BillingSeats:   true,
			VMCreate:       true,
			VMRun:          true,
			DiskResize:     true,
		},
	},
	CategoryIndividual: {
		ID:                   "individual:small:monthly:20260106",
		Available:            true,
		Category:             CategoryIndividual,
		Paid:                 true,
		Name:                 "Individual",
		TrialDays:            ShortSignupTrialDays,
		SignupBonusCreditUSD: 100.0,
		MonthlyLLMCreditUSD:  20,
		// Individual has multiple tiers; DefaultTier is the Small tier used for
		// new subscribers and legacy IDs without an explicit tier component.
		DefaultTier: "individual:small:monthly:20260106",
		Entitlements: map[Entitlement]bool{
			LLMUse:           true,
			CreditPurchase:   true,
			InviteRequest:    true,
			TeamCreate:       true,
			VMCreate:         true,
			VMRun:            true,
			DiskResize:       true,
			BillingSelfServe: true,
		},
	},
	CategoryFriend: {
		ID:                  "friend",
		Available:           true,
		Category:            CategoryFriend,
		Name:                "Friend",
		MonthlyLLMCreditUSD: 500,
		DefaultTier:         "friend:default:monthly:20260106",
		Entitlements: map[Entitlement]bool{
			LLMUse:     true,
			VMCreate:   true,
			VMRun:      true,
			DiskResize: true,
		},
	},
	CategoryGrandfathered: {
		ID:          "grandfathered",
		Available:   true,
		Category:    CategoryGrandfathered,
		Name:        "Grandfathered",
		DefaultTier: "grandfathered:default:monthly:20260106",
		Entitlements: map[Entitlement]bool{
			LLMUse:      true,
			InviteClaim: true,
			VMCreate:    true,
			VMRun:       true,
			DiskResize:  true,
		},
	},
	CategoryTrial: {
		ID:          "trial:monthly:20260106",
		Available:   true,
		Category:    CategoryTrial,
		Name:        "Trial",
		DefaultTier: "trial:default:monthly:20260106",
		Entitlements: map[Entitlement]bool{
			LLMUse:             true,
			VMCreate:           true,
			VMRun:              true,
			DiskResize:         true,
			BillingSelfServe:   true,
			BillingTrialAccess: true,
			AccountDelete:      true,
		},
	},
	CategoryBasic: {
		ID:          "basic:monthly:20260106",
		Available:   true,
		Category:    CategoryBasic,
		Name:        "Basic",
		DefaultTier: "basic:default:monthly:20260106",
		Entitlements: map[Entitlement]bool{
			LLMUse:             true,
			InviteClaim:        true,
			BillingSelfServe:   true,
			BillingTrialAccess: true,
			AccountDelete:      true,
		},
	},
	CategoryRestricted: {
		ID:          "restricted",
		Available:   true,
		Category:    CategoryRestricted,
		Name:        "Restricted",
		DefaultTier: "restricted:default:monthly:20260106",
		Entitlements: map[Entitlement]bool{
			AccountDelete: true,
		},
	},
}

// billingRequiredDate is the cutoff: users created before this are grandfathered.
// Duplicated from execore/billing_status.go to avoid importing execore.
var billingRequiredDate = time.Date(2026, 1, 6, 23, 10, 0, 0, time.UTC)

// getPlanCategory maps existing billing state to a Category.
func getPlanCategory(inputs userPlanInputs) Category {
	// Canceled users go straight to Basic — canceling overrides
	// grandfathered status, exemptions, and trial access.
	if inputs.BillingStatus == "canceled" {
		return CategoryBasic
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
