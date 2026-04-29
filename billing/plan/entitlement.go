// Entitlement types, constants, and grant-checking functions for exe.dev plans.
package plan

import "log/slog"

// Entitlement represents a capability or permission granted by a plan.
type Entitlement struct {
	// ID is the stable identifier used in code and storage (e.g., "llm:use").
	ID string

	// DisplayName is the human-readable description (e.g., "Use LLM Gateway").
	DisplayName string
}

var (
	// LLM usage.
	LLMUse = Entitlement{"llm:use", "Use LLM Gateway"}

	// Credit operations.
	CreditPurchase = Entitlement{"credit:purchase", "Purchase Credits"}

	// Invite operations.
	InviteRequest = Entitlement{"invite:request", "Request Invites"}
	InviteClaim   = Entitlement{"invite:claim", "Claim Invite Codes"}

	// Team operations.
	TeamCreate = Entitlement{"team:create", "Create Teams"}

	// VM operations.
	VMCreate = Entitlement{"vm:create", "Create VMs"}
	VMRun    = Entitlement{"vm:run", "Run VMs"}

	// Disk operations.
	DiskResize = Entitlement{"disk:resize", "Resize VM Disks"}

	// Billing operations.
	BillingSelfServe   = Entitlement{"billing:selfserve", "Self-Service Billing Management"}
	BillingSeats       = Entitlement{"billing:seats", "Per-Seat Pricing"}
	BillingTrialAccess = Entitlement{"billing:trialaccess", "Has / Had Access to Trials"}

	// Account operations.
	AccountDelete = Entitlement{"account:delete", "Account Deletable"}

	// All is a wildcard that grants every entitlement.
	All = Entitlement{"*", "All Entitlements"}
)

// AllEntitlements returns all concrete entitlements in a stable order.
// The All wildcard is excluded since it is not a real entitlement.
func AllEntitlements() []Entitlement {
	return []Entitlement{
		LLMUse,
		CreditPurchase,
		InviteRequest,
		InviteClaim,
		TeamCreate,
		VMCreate,
		VMRun,
		DiskResize,
		BillingSelfServe,
		BillingSeats,
		BillingTrialAccess,
		AccountDelete,
	}
}

// Source identifies the surface that triggered an entitlement check.
// It is not part of the entitlement decision — a user either has an
// entitlement or they don't, regardless of how they got there.
// Source exists purely for observability: every denial log line includes
// it so we can distinguish SSH vs Web traffic when debugging.
type Source string

const (
	// SourceWeb is an entitlement check originating from the web UI.
	SourceWeb Source = "web"
	// SourceSSH is an entitlement check originating from an SSH session.
	SourceSSH Source = "ssh"
)

// Grants reports whether the given plan ID grants the specified entitlement.
// Resolves tiers from any plan ID format (4-part, 3-part legacy, or bare category)
// and applies tier-override → plan-fallback logic.
func Grants(planID string, ent Entitlement) bool {
	tier, err := getTierByID(planID)
	if err != nil {
		slog.Error("entitlement check failed: unknown tier", "plan_id", planID, "entitlement", ent.ID, "error", err)
		return false
	}
	return tierGrants(tier, ent)
}
