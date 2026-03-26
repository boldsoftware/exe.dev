package billing

import (
	"context"
	"testing"
	"time"

	"exe.dev/exedb"
	exesqlite "exe.dev/sqlite"
	"exe.dev/tslog"
)

// TestSyncAccountPlanTrialExpiresAt verifies that trialing subscriptions get
// trial_expires_at populated while non-trialing ones leave it NULL.
func TestSyncAccountPlanTrialExpiresAt(t *testing.T) {
	db := newTestDB(t)
	logger := tslog.Slogger(t)
	m := &Manager{DB: db, Logger: logger}

	ctx := context.Background()
	accountID := "exe_trial_test"
	userID := "usr_trial_test"
	createTestAccount(t, db, accountID, userID)

	// Sync a trialing subscription — should write trial_expires_at.
	now := time.Now().UTC().Truncate(time.Second)
	trialEnd := now.Add(15 * 24 * time.Hour)
	if err := m.syncAccountPlan(ctx, accountID, "active", now, &trialEnd); err != nil {
		t.Fatalf("syncAccountPlan(trialing): %v", err)
	}

	plan, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	if plan.TrialExpiresAt == nil {
		t.Fatal("trial_expires_at is nil, expected non-nil for trialing subscription")
	}
	if !plan.TrialExpiresAt.Equal(exesqlite.NormalizeTime(trialEnd)) {
		t.Errorf("trial_expires_at = %v, want %v", *plan.TrialExpiresAt, trialEnd)
	}
	if plan.ChangedBy == nil || *plan.ChangedBy != "stripe:event" {
		t.Errorf("changed_by = %v, want stripe:event", plan.ChangedBy)
	}
}

// TestSyncAccountPlanNoTrialNullExpiresAt verifies that a non-trialing
// subscription (active/canceled) does not set trial_expires_at.
func TestSyncAccountPlanNoTrialNullExpiresAt(t *testing.T) {
	db := newTestDB(t)
	logger := tslog.Slogger(t)
	m := &Manager{DB: db, Logger: logger}

	ctx := context.Background()
	accountID := "exe_notrial_test"
	userID := "usr_notrial_test"
	createTestAccount(t, db, accountID, userID)

	now := time.Now().UTC().Truncate(time.Second)
	if err := m.syncAccountPlan(ctx, accountID, "active", now, nil); err != nil {
		t.Fatalf("syncAccountPlan(active, no trial): %v", err)
	}

	plan, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	if plan.TrialExpiresAt != nil {
		t.Errorf("trial_expires_at = %v, want nil for non-trialing subscription", *plan.TrialExpiresAt)
	}
}

// TestSyncAccountPlanTrialToActivePreservesExpiry verifies that when a trialing
// subscription transitions to active, trial_expires_at is NOT overwritten to NULL.
// The old plan row (with trial_expires_at set) gets closed, and the new row does
// not carry trial_expires_at since the trial has ended.
func TestSyncAccountPlanTrialToActivePreservesExpiry(t *testing.T) {
	db := newTestDB(t)
	logger := tslog.Slogger(t)
	m := &Manager{DB: db, Logger: logger}

	ctx := context.Background()
	accountID := "exe_t2a_test"
	userID := "usr_t2a_test"
	createTestAccount(t, db, accountID, userID)

	// First sync: trialing subscription.
	now := time.Now().UTC().Truncate(time.Second)
	trialEnd := now.Add(15 * 24 * time.Hour)
	if err := m.syncAccountPlan(ctx, accountID, "active", now, &trialEnd); err != nil {
		t.Fatalf("syncAccountPlan(trialing): %v", err)
	}

	// Verify trial_expires_at is set.
	plan, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan after trial: %v", err)
	}
	if plan.TrialExpiresAt == nil {
		t.Fatal("trial_expires_at should be set during trial")
	}

	// The poller will see the same plan base ("individual") so syncAccountPlan
	// will skip since the base matches — this is the correct behavior.
	// The trial row remains with trial_expires_at set.
	later := now.Add(16 * 24 * time.Hour)
	if err := m.syncAccountPlan(ctx, accountID, "active", later, nil); err != nil {
		t.Fatalf("syncAccountPlan(active, post-trial): %v", err)
	}

	// The active plan should still be the same row (individual) — not overwritten.
	plan2, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan after active: %v", err)
	}
	// The plan base is still individual, so the sync was a no-op.
	// trial_expires_at is preserved on the existing row.
	if plan2.TrialExpiresAt == nil {
		t.Fatal("trial_expires_at was overwritten to NULL on transition to active")
	}
}

// TestSyncAccountPlanInviteTrialUnaffected verifies that the Stripe sync path
// does not modify existing invite-code trial plans.
func TestSyncAccountPlanInviteTrialUnaffected(t *testing.T) {
	db := newTestDB(t)
	_ = tslog.Slogger(t)

	ctx := context.Background()
	accountID := "exe_invite_test"
	userID := "usr_invite_test"
	createTestAccount(t, db, accountID, userID)

	// Simulate an invite-code trial by inserting directly with changed_by=invite:XYZ.
	now := exesqlite.NormalizeTime(time.Now().UTC().Truncate(time.Second))
	inviteTrialEnd := exesqlite.NormalizeTime(time.Now().UTC().Add(30 * 24 * time.Hour))
	changedBy := "invite:TESTCODE"
	err := exedb.WithTx1(db, ctx, (*exedb.Queries).InsertAccountPlan, exedb.InsertAccountPlanParams{
		AccountID:      accountID,
		PlanID:         "trial:monthly:20260101",
		StartedAt:      now,
		TrialExpiresAt: &inviteTrialEnd,
		ChangedBy:      &changedBy,
	})
	if err != nil {
		t.Fatalf("InsertAccountPlan(invite trial): %v", err)
	}

	// Verify invite trial is set correctly.
	plan, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	if plan.TrialExpiresAt == nil {
		t.Fatal("invite trial_expires_at should be set")
	}
	if plan.ChangedBy == nil || *plan.ChangedBy != "invite:TESTCODE" {
		t.Fatalf("changed_by = %v, want invite:TESTCODE", plan.ChangedBy)
	}

	// SetTrialExpiresAt should NOT modify invite trials (it filters by changed_by='stripe:event').
	newExpiry := exesqlite.NormalizeTime(time.Now().UTC().Add(5 * 24 * time.Hour))
	err = exedb.WithTx1(db, ctx, (*exedb.Queries).SetTrialExpiresAt, exedb.SetTrialExpiresAtParams{
		AccountID:      accountID,
		TrialExpiresAt: &newExpiry,
	})
	if err != nil {
		t.Fatalf("SetTrialExpiresAt: %v", err)
	}

	// The invite trial should retain its original expiry.
	plan2, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan after SetTrialExpiresAt: %v", err)
	}
	if !plan2.TrialExpiresAt.Equal(inviteTrialEnd) {
		t.Errorf("invite trial_expires_at changed from %v to %v", inviteTrialEnd, *plan2.TrialExpiresAt)
	}
}

// TestSetTrialExpiresAtBackfill verifies the backfill query only updates
// Stripe-managed plans and preserves invite trials.
func TestSetTrialExpiresAtBackfill(t *testing.T) {
	db := newTestDB(t)
	_ = tslog.Slogger(t)

	ctx := context.Background()

	// Create two accounts: one with a Stripe trial, one with an invite trial.
	stripeAcct := "exe_stripe_bf"
	inviteAcct := "exe_invite_bf"
	createTestAccount(t, db, stripeAcct, "usr_stripe_bf")
	createTestAccount(t, db, inviteAcct, "usr_invite_bf")

	now := exesqlite.NormalizeTime(time.Now().UTC().Truncate(time.Second))

	// Insert a Stripe trial plan without trial_expires_at (the pre-fix state).
	stripeChanged := "stripe:event"
	err := exedb.WithTx1(db, ctx, (*exedb.Queries).InsertAccountPlan, exedb.InsertAccountPlanParams{
		AccountID: stripeAcct,
		PlanID:    "individual:monthly:20260101",
		StartedAt: now,
		ChangedBy: &stripeChanged,
	})
	if err != nil {
		t.Fatalf("InsertAccountPlan(stripe): %v", err)
	}

	// Insert an invite trial plan with trial_expires_at set.
	inviteChanged := "invite:CODE123"
	inviteExpiry := exesqlite.NormalizeTime(time.Now().UTC().Add(30 * 24 * time.Hour))
	err = exedb.WithTx1(db, ctx, (*exedb.Queries).InsertAccountPlan, exedb.InsertAccountPlanParams{
		AccountID:      inviteAcct,
		PlanID:         "trial:monthly:20260101",
		StartedAt:      now,
		TrialExpiresAt: &inviteExpiry,
		ChangedBy:      &inviteChanged,
	})
	if err != nil {
		t.Fatalf("InsertAccountPlan(invite): %v", err)
	}

	// Backfill: set trial_expires_at on the Stripe plan.
	backfillExpiry := exesqlite.NormalizeTime(time.Now().UTC().Add(10 * 24 * time.Hour))
	err = exedb.WithTx1(db, ctx, (*exedb.Queries).SetTrialExpiresAt, exedb.SetTrialExpiresAtParams{
		AccountID:      stripeAcct,
		TrialExpiresAt: &backfillExpiry,
	})
	if err != nil {
		t.Fatalf("SetTrialExpiresAt(stripe): %v", err)
	}

	// Verify the Stripe plan got updated.
	plan, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetActiveAccountPlan, stripeAcct)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan(stripe): %v", err)
	}
	if plan.TrialExpiresAt == nil {
		t.Fatal("Stripe plan trial_expires_at should be set after backfill")
	}
	if !plan.TrialExpiresAt.Equal(backfillExpiry) {
		t.Errorf("Stripe plan trial_expires_at = %v, want %v", *plan.TrialExpiresAt, backfillExpiry)
	}

	// Attempt backfill on invite account — should be a no-op.
	err = exedb.WithTx1(db, ctx, (*exedb.Queries).SetTrialExpiresAt, exedb.SetTrialExpiresAtParams{
		AccountID:      inviteAcct,
		TrialExpiresAt: &backfillExpiry,
	})
	if err != nil {
		t.Fatalf("SetTrialExpiresAt(invite): %v", err)
	}

	// Invite plan should retain original expiry.
	plan2, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetActiveAccountPlan, inviteAcct)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan(invite): %v", err)
	}
	if !plan2.TrialExpiresAt.Equal(inviteExpiry) {
		t.Errorf("invite plan trial_expires_at changed from %v to %v", inviteExpiry, *plan2.TrialExpiresAt)
	}
}
