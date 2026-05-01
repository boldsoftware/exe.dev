package execore

import (
	"context"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
)

// EntitlementCell is a single cell in the entitlement matrix. HasPlan is
// false when no plan applies (rendered as "—"); otherwise Granted indicates
// whether the plan grants the entitlement.
type EntitlementCell struct {
	HasPlan bool
	Granted bool
}

// EntitlementRow is a single row in the debug entitlements table, comparing
// the user's own plan grant, the team (parent account) plan grant, and the
// effective grant (team if the user inherits, else user).
type EntitlementRow struct {
	ID        string
	User      EntitlementCell
	Team      EntitlementCell
	Effective EntitlementCell
}

// buildEntitlementRows resolves entitlements for a user account, optionally
// inheriting from a parent (team) account. userAccountID may be empty when
// the user has no account; in that case all cells render as "—".
func (s *Server) buildEntitlementRows(ctx context.Context, userAccountID, parentAccountID string) []EntitlementRow {
	var userPlanID, teamPlanID string
	if userAccountID != "" {
		if ap, err := withRxRes1(s, ctx, (*exedb.Queries).GetActiveAccountPlan, userAccountID); err == nil {
			userPlanID = ap.PlanID
		}
	}
	if parentAccountID != "" {
		if ap, err := withRxRes1(s, ctx, (*exedb.Queries).GetActiveAccountPlan, parentAccountID); err == nil {
			teamPlanID = ap.PlanID
		}
	}
	effectivePlanID := teamPlanID
	if effectivePlanID == "" {
		effectivePlanID = userPlanID
	}
	cell := func(planID string, ent plan.Entitlement) EntitlementCell {
		if planID == "" {
			return EntitlementCell{}
		}
		return EntitlementCell{HasPlan: true, Granted: plan.Grants(planID, ent)}
	}
	rows := make([]EntitlementRow, 0, len(plan.AllEntitlements()))
	for _, ent := range plan.AllEntitlements() {
		rows = append(rows, EntitlementRow{
			ID:        ent.ID,
			User:      cell(userPlanID, ent),
			Team:      cell(teamPlanID, ent),
			Effective: cell(effectivePlanID, ent),
		})
	}
	return rows
}
