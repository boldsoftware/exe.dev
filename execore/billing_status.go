package execore

import (
	"context"
	"time"

	"exe.dev/billing/entitlement"
	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// billingRequiredDate is when billing became required for new users.
// Users created before this date are grandfathered and don't need billing.
var billingRequiredDate = time.Date(2026, 1, 6, 23, 10, 0, 0, time.UTC)

// userIsPaying reports whether status indicates an active billing subscription.
func userIsPaying(status *exedb.GetUserBillingStatusRow) bool {
	return status.BillingStatus == "active"
}

// userNeedsBilling reports whether the user must add billing before creating VMs.
func userNeedsBilling(status *exedb.GetUserBillingStatusRow) bool {
	// Active users don't need billing
	if status.BillingStatus == "active" {
		return false
	}
	// CANCELED users ALWAYS need billing (check BEFORE exemptions)
	// This prevents canceled users from bypassing billing even if they have
	// legacy status, free tier, or trial exemptions.
	if status.BillingStatus == "canceled" {
		return true
	}
	// Users created before billing requirement date are grandfathered
	if status.CreatedAt != nil && status.CreatedAt.Before(billingRequiredDate) {
		return false
	}
	// Free exemptions never need billing
	if status.BillingExemption != nil && *status.BillingExemption == "free" {
		return false
	}
	// Trial exemptions with future end date don't need billing yet
	if status.BillingExemption != nil && *status.BillingExemption == "trial" &&
		status.BillingTrialEndsAt != nil && status.BillingTrialEndsAt.After(time.Now()) {
		return false
	}
	return true
}

// teamBillingCovers checks if the user's team billing_owner has active billing.
// Returns true if user is in a team and the billing_owner's subscription covers them.
func (s *Server) teamBillingCovers(ctx context.Context, userID string) bool {
	billingOwnerID, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamBillingOwnerUserID, userID)
	if err != nil {
		return false
	}
	status, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserBillingStatus, billingOwnerID)
	if err != nil {
		return false
	}
	return !userNeedsBilling(&status)
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
