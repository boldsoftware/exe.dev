// Package entitlement defines the plan catalog and entitlement constants for exe.dev.
//
// This package imports only stdlib and can be imported by any other package without cycles.
package entitlement

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

	// Team operations.
	TeamCreate = Entitlement{"team:create", "Create Teams"}

	// VM operations.
	VMCreate  = Entitlement{"vm:create", "Create VMs"}
	VMConnect = Entitlement{"vm:connect", "Connect to VMs"}
	VMRun     = Entitlement{"vm:run", "Run VMs"}

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
		TeamCreate,
		VMCreate,
		VMConnect,
		VMRun,
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
