package execore

import (
	"context"

	"exe.dev/billing/entitlement"
	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// teamBillingCovers checks if the user's team billing_owner has active billing.
// Returns true if user is in a team and the billing_owner has a paying subscription.
func (s *Server) teamBillingCovers(ctx context.Context, userID string) bool {
	billingOwnerID, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamBillingOwnerUserID, userID)
	if err != nil {
		return false
	}
	row, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserBilling, billingOwnerID)
	if err != nil {
		return false
	}
	return row.Category == "has_billing"
}

// UserHasEntitlement reports whether the user's plan grants the given entitlement.
// Returns false on any error (safe default). Handles SkipBilling internally.
// Logs all denials with user_id, email, and entitlement.
func (s *Server) UserHasEntitlement(ctx context.Context, source entitlement.Source, ent entitlement.Entitlement, userID string) bool {
	if s.env.SkipBilling {
		return true
	}

	row, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserBilling, userID)
	if err != nil {
		s.slog().WarnContext(ctx, "entitlement check failed",
			"source", string(source),
			"user_id", userID,
			"entitlement", ent.ID,
			"error", err,
		)
		return false
	}

	inputs := entitlement.UserPlanInputs{
		Category:           row.Category,
		BillingStatus:      row.BillingStatus,
		BillingExemption:   row.BillingExemption,
		CreatedAt:          row.CreatedAt,
		BillingTrialEndsAt: row.BillingTrialEndsAt,
		TeamBillingActive:  s.teamBillingCovers(ctx, userID),
	}
	version := entitlement.GetPlanVersion(inputs)
	granted := entitlement.PlanGrants(version, ent)
	if !granted {
		s.slog().InfoContext(ctx, "entitlement denied by plan",
			"source", string(source),
			"user_id", userID,
			"email", row.Email,
			"entitlement", ent.ID,
			"plan", string(version),
		)
	}
	return granted
}

// checkCanCreateVM validates that a user is allowed to create a new VM.
// Returns an error message if blocked, or empty string if allowed.
// The allowOverride parameter bypasses throttle and disabled checks (used for exelet override).
func (ss *SSHServer) checkCanCreateVM(ctx context.Context, user *exemenu.UserInfo, allowOverride bool) string {
	// Check if user is throttled from creating new VMs
	if !allowOverride {
		if throttled, msg := ss.server.CheckNewThrottle(ctx, user.ID, user.Email); throttled {
			return msg
		}
	}

	// Check if user has VM creation disabled
	if !allowOverride {
		if disabled, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserNewVMCreationDisabled, user.ID); err == nil && disabled {
			return "VM creation is not available for your account; contact support@exe.dev"
		}
	}

	// Check if user's plan grants VM creation
	if !ss.server.UserHasEntitlement(ctx, entitlement.SourceSSH, entitlement.VMCreate, user.ID) {
		billingURL := ss.server.webBaseURLNoRequest() + "/billing/update?source=exemenu"
		return "Billing Required\r\n\r\nYou need to add billing information before creating a VM.\r\n\r\nVisit: " + billingURL
	}

	return ""
}
