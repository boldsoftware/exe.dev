package execore

import (
	"context"
	"database/sql"
	"errors"

	"exe.dev/billing/entitlement"
	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// UserHasEntitlement reports whether the user's plan grants the given entitlement.
// Returns false on any error (safe default). Handles SkipBilling internally.
// Logs all denials with user_id, email, and entitlement.
//
// Plan resolution: user -> account (via accounts.created_by) -> account_plans (WHERE ended_at IS NULL).
// If the account has a parent_id, the parent's active plan is used instead.
func (s *Server) UserHasEntitlement(ctx context.Context, source entitlement.Source, ent entitlement.Entitlement, userID string) bool {
	if s.env.SkipBilling {
		return true
	}

	planRow, err := withRxRes1(s, ctx, (*exedb.Queries).GetActivePlanForUser, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No account or no active plan — user may not have been migrated yet.
			// Fall back to the legacy waterfall so we don't break existing users.
			return s.userHasEntitlementLegacy(ctx, source, ent, userID)
		}
		s.slog().WarnContext(ctx, "entitlement check failed",
			"source", string(source),
			"user_id", userID,
			"entitlement", ent.ID,
			"error", err,
		)
		return false
	}

	version := entitlement.PlanVersion(planRow.PlanID)
	granted := entitlement.PlanGrants(version, ent)
	if !granted {
		s.slog().InfoContext(ctx, "entitlement denied by plan",
			"source", string(source),
			"user_id", userID,
			"entitlement", ent.ID,
			"plan", planRow.PlanID,
		)
	}
	return granted
}

// userHasEntitlementLegacy is the pre-account_plans fallback entitlement check.
// Used when a user has no account_plans row (e.g., legacy users before backfill).
// This path will be removed once migration 121 has run on all databases.
func (s *Server) userHasEntitlementLegacy(ctx context.Context, source entitlement.Source, ent entitlement.Entitlement, userID string) bool {
	row, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserBilling, userID)
	if err != nil {
		s.slog().WarnContext(ctx, "legacy entitlement check failed",
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
		s.slog().InfoContext(ctx, "entitlement denied by plan (legacy path)",
			"source", string(source),
			"user_id", userID,
			"email", row.Email,
			"entitlement", ent.ID,
			"plan", string(version),
		)
	}
	return granted
}

// teamBillingCovers checks if the user's team billing_owner has active billing.
// Returns true if user is in a team and the billing_owner has a paying subscription.
// Used by the legacy entitlement path only.
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
