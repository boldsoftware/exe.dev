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
	CreditRenew    = Entitlement{"credit:renew", "Renew Credits"}
	CreditPurchase = Entitlement{"credit:purchase", "Purchase Credits"}
	CreditRefresh  = Entitlement{"credit:refresh", "Refresh Credits"}

	// VM operations.
	VMCreate  = Entitlement{"vm:create", "Create VMs"}
	VMConnect = Entitlement{"vm:connect", "Connect to VMs"}

	// Compute operations.
	ComputeSpend    = Entitlement{"compute:spend", "Spend Compute Credits"}
	ComputePurchase = Entitlement{"compute:purchase", "Purchase Compute"}
	ComputeDebt     = Entitlement{"compute:debt", "Accrue Compute Debt"}
	ComputeOnDemand = Entitlement{"compute:on_demand", "On-Demand Compute"}

	// Admin operations.
	AdminOverride = Entitlement{"admin:override", "Admin Override"}

	// All is a wildcard that grants every entitlement.
	All = Entitlement{"*", "All Entitlements"}
)

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
