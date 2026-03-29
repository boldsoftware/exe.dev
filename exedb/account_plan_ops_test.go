package exedb_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"exe.dev/exedb"
)

// TestReplaceAccountPlan verifies the basic replace operation:
// closes existing plan and inserts new plan.
func TestReplaceAccountPlan(t *testing.T) {
	t.Parallel()

	db, q := setupAccountTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	userID := "usr_replace0001"
	acctID := "exe_replace0001"

	// Setup user and account
	if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "replace@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, acctID, userID); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	// Insert initial plan
	if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID: acctID,
		PlanID:    "basic",
		StartedAt: now,
		ChangedBy: strPtr("system:signup"),
	}); err != nil {
		t.Fatalf("insert initial plan: %v", err)
	}

	// Verify initial plan is active
	initialPlan, err := q.GetActiveAccountPlan(ctx, acctID)
	if err != nil {
		t.Fatalf("get initial plan: %v", err)
	}
	if initialPlan.PlanID != "basic" {
		t.Errorf("expected initial plan='basic', got %q", initialPlan.PlanID)
	}

	// Replace with new plan
	replaceTime := now.Add(time.Hour)
	if err := q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
		AccountID: acctID,
		PlanID:    "individual",
		At:        replaceTime,
		ChangedBy: "stripe:event",
	}); err != nil {
		t.Fatalf("replace account plan: %v", err)
	}

	// Verify new plan is active
	newPlan, err := q.GetActiveAccountPlan(ctx, acctID)
	if err != nil {
		t.Fatalf("get new plan: %v", err)
	}
	if newPlan.PlanID != "individual" {
		t.Errorf("expected new plan='individual', got %q", newPlan.PlanID)
	}
	if newPlan.EndedAt != nil {
		t.Errorf("expected new plan ended_at=nil, got %v", newPlan.EndedAt)
	}
	if !newPlan.StartedAt.Equal(replaceTime) {
		t.Errorf("expected new plan started_at=%v, got %v", replaceTime, newPlan.StartedAt)
	}

	// Verify old plan was closed
	history, err := q.ListAccountPlanHistory(ctx, acctID)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history rows, got %d", len(history))
	}

	// History is ordered newest first
	if history[0].PlanID != "individual" {
		t.Errorf("expected newest plan='individual', got %q", history[0].PlanID)
	}
	if history[1].PlanID != "basic" {
		t.Errorf("expected oldest plan='basic', got %q", history[1].PlanID)
	}
	if history[1].EndedAt == nil {
		t.Error("expected old plan to be closed (ended_at != nil)")
	} else if !history[1].EndedAt.Equal(replaceTime) {
		t.Errorf("expected old plan ended_at=%v, got %v", replaceTime, *history[1].EndedAt)
	}
}

// TestReplaceAccountPlanWithTrial verifies that trial_expires_at is preserved
// when replacing a plan.
func TestReplaceAccountPlanWithTrial(t *testing.T) {
	t.Parallel()

	db, q := setupAccountTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	userID := "usr_trial00001"
	acctID := "exe_trial00001"

	// Setup user and account
	if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "trial@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, acctID, userID); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	// Insert initial plan without trial
	if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID: acctID,
		PlanID:    "basic",
		StartedAt: now,
		ChangedBy: strPtr("system:signup"),
	}); err != nil {
		t.Fatalf("insert initial plan: %v", err)
	}

	// Replace with new plan that has trial
	replaceTime := now.Add(time.Hour)
	trialExpires := replaceTime.Add(14 * 24 * time.Hour) // 14 days trial
	if err := q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
		AccountID:      acctID,
		PlanID:         "individual",
		At:             replaceTime,
		TrialExpiresAt: &trialExpires,
		ChangedBy:      "stripe:event",
	}); err != nil {
		t.Fatalf("replace account plan: %v", err)
	}

	// Verify trial_expires_at was set
	newPlan, err := q.GetActiveAccountPlan(ctx, acctID)
	if err != nil {
		t.Fatalf("get new plan: %v", err)
	}
	if newPlan.TrialExpiresAt == nil {
		t.Fatal("expected trial_expires_at to be set")
	}
	if !newPlan.TrialExpiresAt.Equal(trialExpires) {
		t.Errorf("expected trial_expires_at=%v, got %v", trialExpires, *newPlan.TrialExpiresAt)
	}
}

// TestReplaceAccountPlanMultipleTimes verifies that multiple sequential
// plan replacements work correctly.
func TestReplaceAccountPlanMultipleTimes(t *testing.T) {
	t.Parallel()

	db, q := setupAccountTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	userID := "usr_multi0001"
	acctID := "exe_multi0001"

	// Setup user and account
	if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "multi@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, acctID, userID); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	// Insert initial plan
	if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID: acctID,
		PlanID:    "basic",
		StartedAt: now,
		ChangedBy: strPtr("system:signup"),
	}); err != nil {
		t.Fatalf("insert initial plan: %v", err)
	}

	// First replacement: basic -> trial
	time1 := now.Add(time.Hour)
	if err := q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
		AccountID: acctID,
		PlanID:    "trial",
		At:        time1,
		ChangedBy: "invite:test-code",
	}); err != nil {
		t.Fatalf("first replace: %v", err)
	}

	// Second replacement: trial -> individual
	time2 := time1.Add(time.Hour)
	if err := q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
		AccountID: acctID,
		PlanID:    "individual",
		At:        time2,
		ChangedBy: "stripe:event",
	}); err != nil {
		t.Fatalf("second replace: %v", err)
	}

	// Third replacement: individual -> team
	time3 := time2.Add(time.Hour)
	if err := q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
		AccountID: acctID,
		PlanID:    "team",
		At:        time3,
		ChangedBy: "stripe:event",
	}); err != nil {
		t.Fatalf("third replace: %v", err)
	}

	// Verify final active plan is team
	activePlan, err := q.GetActiveAccountPlan(ctx, acctID)
	if err != nil {
		t.Fatalf("get active plan: %v", err)
	}
	if activePlan.PlanID != "team" {
		t.Errorf("expected plan='team', got %q", activePlan.PlanID)
	}
	if activePlan.EndedAt != nil {
		t.Errorf("expected active plan ended_at=nil, got %v", activePlan.EndedAt)
	}

	// Verify history has all 4 plans
	history, err := q.ListAccountPlanHistory(ctx, acctID)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("expected 4 history rows, got %d", len(history))
	}

	// Verify order and ended_at timestamps
	expected := []struct {
		planID  string
		endedAt *time.Time
	}{
		{"team", nil},
		{"individual", &time3},
		{"trial", &time2},
		{"basic", &time1},
	}
	for i, exp := range expected {
		if history[i].PlanID != exp.planID {
			t.Errorf("history[%d]: expected plan=%q, got %q", i, exp.planID, history[i].PlanID)
		}
		if exp.endedAt == nil {
			if history[i].EndedAt != nil {
				t.Errorf("history[%d]: expected ended_at=nil, got %v", i, history[i].EndedAt)
			}
		} else {
			if history[i].EndedAt == nil {
				t.Errorf("history[%d]: expected ended_at=%v, got nil", i, exp.endedAt)
			} else if !history[i].EndedAt.Equal(*exp.endedAt) {
				t.Errorf("history[%d]: expected ended_at=%v, got %v", i, exp.endedAt, history[i].EndedAt)
			}
		}
	}
}

// TestReplaceAccountPlanNoExistingPlan verifies that ReplaceAccountPlan
// works correctly when the account has no existing active plan.
func TestReplaceAccountPlanNoExistingPlan(t *testing.T) {
	t.Parallel()

	db, q := setupAccountTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	userID := "usr_noplan0001"
	acctID := "exe_noplan0001"

	// Setup user and account
	if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "noplan@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, acctID, userID); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	// Don't insert any initial plan

	// Verify no active plan exists
	_, err := q.GetActiveAccountPlan(ctx, acctID)
	if err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows for no active plan, got: %v", err)
	}

	// Replace (should just insert new plan)
	if err := q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
		AccountID: acctID,
		PlanID:    "individual",
		At:        now,
		ChangedBy: "stripe:event",
	}); err != nil {
		t.Fatalf("replace when no existing plan: %v", err)
	}

	// Verify new plan is active
	newPlan, err := q.GetActiveAccountPlan(ctx, acctID)
	if err != nil {
		t.Fatalf("get new plan: %v", err)
	}
	if newPlan.PlanID != "individual" {
		t.Errorf("expected plan='individual', got %q", newPlan.PlanID)
	}
	if newPlan.EndedAt != nil {
		t.Errorf("expected ended_at=nil, got %v", newPlan.EndedAt)
	}

	// Verify history has only 1 row
	history, err := q.ListAccountPlanHistory(ctx, acctID)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("expected 1 history row, got %d", len(history))
	}
}

// TestReplaceAccountPlanChangedBy verifies that changed_by is properly
// tracked for both close and insert operations.
func TestReplaceAccountPlanChangedBy(t *testing.T) {
	t.Parallel()

	db, q := setupAccountTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	userID := "usr_changedby01"
	acctID := "exe_changedby01"

	// Setup user and account
	if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "changedby@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, acctID, userID); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	// Insert initial plan with one changed_by
	if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID: acctID,
		PlanID:    "basic",
		StartedAt: now,
		ChangedBy: strPtr("system:signup"),
	}); err != nil {
		t.Fatalf("insert initial plan: %v", err)
	}

	// Replace with different changed_by
	replaceTime := now.Add(time.Hour)
	if err := q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
		AccountID: acctID,
		PlanID:    "individual",
		At:        replaceTime,
		ChangedBy: "debug:add-billing",
	}); err != nil {
		t.Fatalf("replace account plan: %v", err)
	}

	// Verify new plan has correct changed_by
	newPlan, err := q.GetActiveAccountPlan(ctx, acctID)
	if err != nil {
		t.Fatalf("get new plan: %v", err)
	}
	if newPlan.ChangedBy == nil || *newPlan.ChangedBy != "debug:add-billing" {
		t.Errorf("expected changed_by='debug:add-billing', got %v", newPlan.ChangedBy)
	}

	// Verify old plan still has original changed_by
	history, err := q.ListAccountPlanHistory(ctx, acctID)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history rows, got %d", len(history))
	}
	if history[1].ChangedBy == nil || *history[1].ChangedBy != "system:signup" {
		t.Errorf("expected old plan changed_by='system:signup', got %v", history[1].ChangedBy)
	}
}

// TestReplaceAccountPlanDifferentSources verifies plan replacements
// from different sources (invite, stripe, debug).
func TestReplaceAccountPlanDifferentSources(t *testing.T) {
	t.Parallel()

	db, q := setupAccountTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	userID := "usr_sources01"
	acctID := "exe_sources01"

	// Setup user and account
	if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "sources@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, acctID, userID); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	// Start with basic plan
	if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID: acctID,
		PlanID:    "basic",
		StartedAt: now,
		ChangedBy: strPtr("system:signup"),
	}); err != nil {
		t.Fatalf("insert initial plan: %v", err)
	}

	// Replace via invite code
	time1 := now.Add(time.Hour)
	if err := q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
		AccountID: acctID,
		PlanID:    "friend",
		At:        time1,
		ChangedBy: "invite:abc-xyz-123",
	}); err != nil {
		t.Fatalf("replace via invite: %v", err)
	}

	// Replace via stripe
	time2 := time1.Add(time.Hour)
	if err := q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
		AccountID: acctID,
		PlanID:    "individual",
		At:        time2,
		ChangedBy: "stripe:event",
	}); err != nil {
		t.Fatalf("replace via stripe: %v", err)
	}

	// Replace via debug endpoint
	time3 := time2.Add(time.Hour)
	if err := q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
		AccountID: acctID,
		PlanID:    "team",
		At:        time3,
		ChangedBy: "debug:add-billing",
	}); err != nil {
		t.Fatalf("replace via debug: %v", err)
	}

	// Verify all changed_by values are tracked correctly
	history, err := q.ListAccountPlanHistory(ctx, acctID)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("expected 4 history rows, got %d", len(history))
	}

	expectedSources := []string{"debug:add-billing", "stripe:event", "invite:abc-xyz-123", "system:signup"}
	for i, exp := range expectedSources {
		if history[i].ChangedBy == nil {
			t.Errorf("history[%d]: expected changed_by=%q, got nil", i, exp)
		} else if *history[i].ChangedBy != exp {
			t.Errorf("history[%d]: expected changed_by=%q, got %q", i, exp, *history[i].ChangedBy)
		}
	}
}
