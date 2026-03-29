package exedb

import (
	"context"
	"fmt"
	"time"
)

// ReplaceAccountPlanParams contains parameters for replacing an account's plan.
type ReplaceAccountPlanParams struct {
	AccountID      string
	PlanID         string
	At             time.Time  // When the plan change occurs
	TrialExpiresAt *time.Time // Optional trial expiration
	ChangedBy      string     // Source of change (e.g., "stripe:event", "debug:add-billing")
}

// ReplaceAccountPlan closes any existing plan for the account and inserts a new one.
// This is the canonical way to switch an account's plan.
//
// Returns an error if the close or insert fails. The caller should wrap this
// in a transaction via exedb.WithTx if atomicity is required.
func (q *Queries) ReplaceAccountPlan(ctx context.Context, params ReplaceAccountPlanParams) error {
	// Close any existing active plan
	if err := q.CloseAccountPlan(ctx, CloseAccountPlanParams{
		AccountID: params.AccountID,
		EndedAt:   &params.At,
	}); err != nil {
		return fmt.Errorf("close existing plan: %w", err)
	}

	// Insert new plan
	changedBy := &params.ChangedBy
	return q.InsertAccountPlan(ctx, InsertAccountPlanParams{
		AccountID:      params.AccountID,
		PlanID:         params.PlanID,
		StartedAt:      params.At,
		TrialExpiresAt: params.TrialExpiresAt,
		ChangedBy:      changedBy,
	})
}
