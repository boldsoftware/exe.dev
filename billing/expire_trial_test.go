package billing

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"exe.dev/billing/entitlement"
	"exe.dev/exedb"
	"exe.dev/sqlite"
	"exe.dev/tslog"
)

func TestExpireTrialPlans(t *testing.T) {
	db := newTestDB(t)
	m := &Manager{DB: db, Logger: tslog.Slogger(t)}

	accountID := "acct_trial_expire"
	userID := "user_trial_expire"
	createTestAccount(t, db, accountID, userID)

	// Insert a trial plan that expired 1 hour ago.
	expired := sqlite.NormalizeTime(time.Now().Add(-1 * time.Hour))
	started := sqlite.NormalizeTime(time.Now().Add(-8 * 24 * time.Hour))
	changedBy := "test"
	err := exedb.WithTx1(db, context.Background(), (*exedb.Queries).InsertAccountPlan, exedb.InsertAccountPlanParams{
		AccountID:      accountID,
		PlanID:         "trial",
		StartedAt:      started,
		TrialExpiresAt: &expired,
		ChangedBy:      &changedBy,
	})
	if err != nil {
		t.Fatalf("InsertAccountPlan: %v", err)
	}

	// Verify the trial plan is active.
	plan, err := exedb.WithRxRes1(db, context.Background(), (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	if plan.PlanID != "trial" {
		t.Fatalf("expected trial plan, got %q", plan.PlanID)
	}

	// Track downgrade callback.
	var downgradedAccountID atomic.Value
	m.OnPlanDowngrade = func(ctx context.Context, acctID string) {
		downgradedAccountID.Store(acctID)
	}

	// Run expiry.
	n, err := m.ExpireTrialPlans(context.Background())
	if err != nil {
		t.Fatalf("ExpireTrialPlans: %v", err)
	}
	if n != 1 {
		t.Fatalf("ExpireTrialPlans returned %d, want 1", n)
	}

	// Verify the plan is now basic.
	plan, err = exedb.WithRxRes1(db, context.Background(), (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan after expire: %v", err)
	}
	if plan.PlanID != "basic" {
		t.Fatalf("expected basic plan after expire, got %q", plan.PlanID)
	}

	// Verify callback was called.
	if got, ok := downgradedAccountID.Load().(string); !ok || got != accountID {
		t.Fatalf("OnPlanDowngrade called with %q, want %q", got, accountID)
	}

	// Running again should be a no-op.
	n, err = m.ExpireTrialPlans(context.Background())
	if err != nil {
		t.Fatalf("ExpireTrialPlans second run: %v", err)
	}
	if n != 0 {
		t.Fatalf("ExpireTrialPlans second run returned %d, want 0", n)
	}
}

func TestExpireTrialPlans_NotExpiredYet(t *testing.T) {
	db := newTestDB(t)
	m := &Manager{DB: db, Logger: tslog.Slogger(t)}

	accountID := "acct_trial_future"
	userID := "user_trial_future"
	createTestAccount(t, db, accountID, userID)

	// Insert a trial plan that expires in the future.
	future := sqlite.NormalizeTime(time.Now().Add(24 * time.Hour))
	started := sqlite.NormalizeTime(time.Now().Add(-1 * 24 * time.Hour))
	changedBy := "test"
	err := exedb.WithTx1(db, context.Background(), (*exedb.Queries).InsertAccountPlan, exedb.InsertAccountPlanParams{
		AccountID:      accountID,
		PlanID:         "trial",
		StartedAt:      started,
		TrialExpiresAt: &future,
		ChangedBy:      &changedBy,
	})
	if err != nil {
		t.Fatalf("InsertAccountPlan: %v", err)
	}

	n, err := m.ExpireTrialPlans(context.Background())
	if err != nil {
		t.Fatalf("ExpireTrialPlans: %v", err)
	}
	if n != 0 {
		t.Fatalf("ExpireTrialPlans returned %d, want 0 (trial not expired yet)", n)
	}

	// Verify the plan is still trial.
	plan, err := exedb.WithRxRes1(db, context.Background(), (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	if plan.PlanID != "trial" {
		t.Fatalf("expected trial plan still active, got %q", plan.PlanID)
	}
}

func TestExpireTrialPlans_InviteCodeExcluded(t *testing.T) {
	db := newTestDB(t)
	m := &Manager{DB: db, Logger: tslog.Slogger(t)}

	accountID := "acct_invite_trial"
	userID := "user_invite_trial"
	createTestAccount(t, db, accountID, userID)

	// Insert an invite-code trial that has expired.
	expired := sqlite.NormalizeTime(time.Now().Add(-1 * time.Hour))
	started := sqlite.NormalizeTime(time.Now().Add(-32 * 24 * time.Hour))
	changedBy := "invite:TESTCODE123"
	err := exedb.WithTx1(db, context.Background(), (*exedb.Queries).InsertAccountPlan, exedb.InsertAccountPlanParams{
		AccountID:      accountID,
		PlanID:         "trial",
		StartedAt:      started,
		TrialExpiresAt: &expired,
		ChangedBy:      &changedBy,
	})
	if err != nil {
		t.Fatalf("InsertAccountPlan: %v", err)
	}

	called := false
	m.OnPlanDowngrade = func(ctx context.Context, acctID string) {
		called = true
	}

	// Run expiry — should NOT expire invite-code trials.
	n, err := m.ExpireTrialPlans(context.Background())
	if err != nil {
		t.Fatalf("ExpireTrialPlans: %v", err)
	}
	if n != 0 {
		t.Fatalf("ExpireTrialPlans returned %d, want 0 (invite-code trials excluded)", n)
	}

	if called {
		t.Fatal("OnPlanDowngrade should not be called for invite-code trials")
	}

	// Verify the plan is still trial.
	plan, err := exedb.WithRxRes1(db, context.Background(), (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	if plan.PlanID != "trial" {
		t.Fatalf("expected trial plan still active, got %q", plan.PlanID)
	}
}

func TestSyncAccountPlanDowngradeCallback(t *testing.T) {
	db := newTestDB(t)
	m := &Manager{DB: db, Logger: tslog.Slogger(t)}

	accountID := "acct_downgrade_cb"
	userID := "user_downgrade_cb"
	createTestAccount(t, db, accountID, userID)

	// Insert an active individual plan.
	started := sqlite.NormalizeTime(time.Now().Add(-30 * 24 * time.Hour))
	changedBy := "test"
	err := exedb.WithTx1(db, context.Background(), (*exedb.Queries).InsertAccountPlan, exedb.InsertAccountPlanParams{
		AccountID: accountID,
		PlanID:    "individual",
		StartedAt: started,
		ChangedBy: &changedBy,
	})
	if err != nil {
		t.Fatalf("InsertAccountPlan: %v", err)
	}

	// Track downgrade callback.
	var downgradedAccountID atomic.Value
	m.OnPlanDowngrade = func(ctx context.Context, acctID string) {
		downgradedAccountID.Store(acctID)
	}

	// Sync a "canceled" event.
	if err := m.syncAccountPlan(context.Background(), accountID, "canceled", time.Now(), nil); err != nil {
		t.Fatalf("syncAccountPlan: %v", err)
	}

	// Verify callback was called.
	if got, ok := downgradedAccountID.Load().(string); !ok || got != accountID {
		t.Fatalf("OnPlanDowngrade called with %q, want %q", got, accountID)
	}

	// Verify plan is basic.
	plan, err := exedb.WithRxRes1(db, context.Background(), (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	if entitlement.BasePlan(plan.PlanID) != entitlement.CategoryBasic {
		t.Fatalf("expected basic plan, got %q", plan.PlanID)
	}
}

func TestSyncAccountPlanActiveNoCallback(t *testing.T) {
	db := newTestDB(t)
	m := &Manager{DB: db, Logger: tslog.Slogger(t)}

	accountID := "acct_active_nocb"
	userID := "user_active_nocb"
	createTestAccount(t, db, accountID, userID)

	// Insert a basic plan.
	started := sqlite.NormalizeTime(time.Now().Add(-30 * 24 * time.Hour))
	changedBy := "test"
	err := exedb.WithTx1(db, context.Background(), (*exedb.Queries).InsertAccountPlan, exedb.InsertAccountPlanParams{
		AccountID: accountID,
		PlanID:    "basic",
		StartedAt: started,
		ChangedBy: &changedBy,
	})
	if err != nil {
		t.Fatalf("InsertAccountPlan: %v", err)
	}

	called := false
	m.OnPlanDowngrade = func(ctx context.Context, acctID string) {
		called = true
	}

	// Sync an "active" event — should NOT call the downgrade callback.
	if err := m.syncAccountPlan(context.Background(), accountID, "active", time.Now(), nil); err != nil {
		t.Fatalf("syncAccountPlan: %v", err)
	}

	if called {
		t.Fatal("OnPlanDowngrade should not be called for active events")
	}

	// Verify plan is individual.
	plan, err := exedb.WithRxRes1(db, context.Background(), (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	if entitlement.BasePlan(plan.PlanID) != entitlement.CategoryIndividual {
		t.Fatalf("expected individual plan, got %q", plan.PlanID)
	}
}
