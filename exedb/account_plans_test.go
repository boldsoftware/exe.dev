package exedb_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
	"exe.dev/tslog"
	_ "modernc.org/sqlite"
)

func setupAccountTestDB(t *testing.T) (*sql.DB, *exedb.Queries) {
	t.Helper()

	dbPath := t.TempDir() + "/account_test.db"
	if err := exedb.CopyTemplateDB(tslog.Slogger(t), dbPath); err != nil {
		t.Fatalf("failed to copy template database: %v", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	queries := exedb.New(db)
	return db, queries
}

// TestAccountsHaveParentIDAndStatus verifies that migration 119 added parent_id and status
// columns to the accounts table with correct constraints.
func TestAccountsHaveParentID(t *testing.T) {
	t.Parallel()

	db, _ := setupAccountTestDB(t)
	ctx := context.Background()

	// Verify parent_id column exists.
	var parentIDName string
	err := db.QueryRowContext(ctx, `SELECT name FROM pragma_table_info('accounts') WHERE name = 'parent_id'`).Scan(&parentIDName)
	if err != nil {
		t.Fatalf("parent_id column missing from accounts: %v", err)
	}

	// Verify status column was dropped.
	var statusName string
	err = db.QueryRowContext(ctx, `SELECT name FROM pragma_table_info('accounts') WHERE name = 'status'`).Scan(&statusName)
	if err == nil {
		t.Fatal("status column should have been dropped from accounts")
	}

	// NULL parent_id (top-level account) must be valid.
	userID := "usr_migtest00001"
	if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "migtest@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, "exe_migtest00001", userID); err != nil {
		t.Fatalf("insert account with NULL parent_id: %v", err)
	}
}

// TestAccountPlansPartialUniqueIndex verifies that migration 120 created the account_plans
// table and that the partial unique index prevents two active plans for the same account.
func TestAccountPlansPartialUniqueIndex(t *testing.T) {
	t.Parallel()

	db, _ := setupAccountTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	userID := "usr_planidx00001"
	acctID := "exe_planidx00001"

	if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "planidx@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, acctID, userID); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	// First active plan row should succeed.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO account_plans (account_id, plan_id, started_at, changed_by) VALUES (?, ?, ?, ?)`,
		acctID, "basic", now, "system:test",
	); err != nil {
		t.Fatalf("insert first plan row: %v", err)
	}

	// Second active row (ended_at IS NULL) for same account must be rejected.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO account_plans (account_id, plan_id, started_at, changed_by) VALUES (?, ?, ?, ?)`,
		acctID, "individual", now, "system:test",
	); err == nil {
		t.Error("expected UNIQUE constraint violation for second active plan, got nil")
	}

	// Close the first plan; then a second active row must succeed.
	if _, err := db.ExecContext(ctx,
		`UPDATE account_plans SET ended_at = ? WHERE account_id = ? AND ended_at IS NULL`,
		now, acctID,
	); err != nil {
		t.Fatalf("close first plan: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO account_plans (account_id, plan_id, started_at, changed_by) VALUES (?, ?, ?, ?)`,
		acctID, "individual", now.Add(time.Second), "system:test",
	); err != nil {
		t.Fatalf("insert second plan after closing first: %v", err)
	}

	// Both rows should exist in history.
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_plans WHERE account_id = ?`, acctID).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 account_plans rows, got %d", count)
	}
}

// TestInsertAndGetActiveAccountPlan verifies sqlc-generated InsertAccountPlan,
// GetActiveAccountPlan, CloseAccountPlan, and ListAccountPlanHistory work correctly.
func TestInsertAndGetActiveAccountPlan(t *testing.T) {
	t.Parallel()

	db, q := setupAccountTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	userID := "usr_planget00001"
	acctID := "exe_planget00001"

	if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "planget@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, acctID, userID); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	// Insert initial plan.
	if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID: acctID,
		PlanID:    "basic",
		StartedAt: now,
		ChangedBy: new("system:signup"),
	}); err != nil {
		t.Fatalf("InsertAccountPlan: %v", err)
	}

	// GetActiveAccountPlan should return the row with ended_at = nil.
	ap, err := q.GetActiveAccountPlan(ctx, acctID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	if ap.PlanID != "basic" {
		t.Errorf("expected plan_id='basic', got %q", ap.PlanID)
	}
	if ap.EndedAt != nil {
		t.Errorf("expected ended_at=nil, got %v", ap.EndedAt)
	}

	// Upgrade: close then insert new plan.
	if err := q.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{
		AccountID: acctID,
		EndedAt:   &now,
	}); err != nil {
		t.Fatalf("CloseAccountPlan: %v", err)
	}

	now2 := now.Add(time.Minute)
	if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID: acctID,
		PlanID:    "individual",
		StartedAt: now2,
		ChangedBy: new("stripe:event"),
	}); err != nil {
		t.Fatalf("InsertAccountPlan individual: %v", err)
	}

	ap2, err := q.GetActiveAccountPlan(ctx, acctID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan after upgrade: %v", err)
	}
	if ap2.PlanID != "individual" {
		t.Errorf("expected plan_id='individual', got %q", ap2.PlanID)
	}

	// ListAccountPlanHistory returns newest first.
	history, err := q.ListAccountPlanHistory(ctx, acctID)
	if err != nil {
		t.Fatalf("ListAccountPlanHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history rows, got %d", len(history))
	}
	if history[0].PlanID != "individual" {
		t.Errorf("expected newest plan='individual', got %q", history[0].PlanID)
	}
	if history[1].PlanID != "basic" {
		t.Errorf("expected oldest plan='basic', got %q", history[1].PlanID)
	}
}

// TestGetActivePlanForUserWithParent verifies that GetActivePlanForUser returns the parent
// account's plan when parent_id is set (team member scenario).
func TestGetActivePlanForUserWithParent(t *testing.T) {
	t.Parallel()

	db, q := setupAccountTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Team owner.
	teamUserID := "usr_teamown0001"
	teamAcctID := "exe_teamown0001"
	// Member with parent pointing to team.
	memberUserID := "usr_teammem0001"
	memberAcctID := "exe_teammem0001"

	for _, row := range []struct{ uid, email string }{
		{teamUserID, "team-owner@example.com"},
		{memberUserID, "team-member@example.com"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, row.uid, row.email); err != nil {
			t.Fatalf("insert user %s: %v", row.uid, err)
		}
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, teamAcctID, teamUserID); err != nil {
		t.Fatalf("insert team account: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by, parent_id) VALUES (?, ?, ?)`, memberAcctID, memberUserID, teamAcctID); err != nil {
		t.Fatalf("insert member account with parent: %v", err)
	}

	// Team account plan = 'team'.
	if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID: teamAcctID,
		PlanID:    "team",
		StartedAt: now,
		ChangedBy: new("system:test"),
	}); err != nil {
		t.Fatalf("insert team plan: %v", err)
	}
	// Member's own account plan = 'basic' (overridden by parent).
	if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID: memberAcctID,
		PlanID:    "basic",
		StartedAt: now,
		ChangedBy: new("system:test"),
	}); err != nil {
		t.Fatalf("insert member plan: %v", err)
	}

	// Member should resolve to parent's 'team' plan.
	memberPlan, err := q.GetActivePlanForUser(ctx, memberUserID)
	if err != nil {
		t.Fatalf("GetActivePlanForUser(member): %v", err)
	}
	if memberPlan.PlanID != "team" {
		t.Errorf("expected member plan='team' (from parent), got %q", memberPlan.PlanID)
	}
	if memberPlan.AccountID != teamAcctID {
		t.Errorf("expected account_id=%q (team), got %q", teamAcctID, memberPlan.AccountID)
	}

	// Owner (no parent) should resolve to their own 'team' plan.
	ownerPlan, err := q.GetActivePlanForUser(ctx, teamUserID)
	if err != nil {
		t.Fatalf("GetActivePlanForUser(owner): %v", err)
	}
	if ownerPlan.PlanID != "team" {
		t.Errorf("expected owner plan='team', got %q", ownerPlan.PlanID)
	}

	// Standalone user (no parent) should resolve to their own plan.
	soloUserID := "usr_solousers01"
	soloAcctID := "exe_solousers01"
	if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, soloUserID, "solo@example.com"); err != nil {
		t.Fatalf("insert solo user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, soloAcctID, soloUserID); err != nil {
		t.Fatalf("insert solo account: %v", err)
	}
	if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID: soloAcctID,
		PlanID:    "individual",
		StartedAt: now,
		ChangedBy: new("stripe:event"),
	}); err != nil {
		t.Fatalf("insert solo plan: %v", err)
	}

	soloPlan, err := q.GetActivePlanForUser(ctx, soloUserID)
	if err != nil {
		t.Fatalf("GetActivePlanForUser(solo): %v", err)
	}
	if soloPlan.PlanID != "individual" {
		t.Errorf("expected solo plan='individual', got %q", soloPlan.PlanID)
	}
	if soloPlan.AccountID != soloAcctID {
		t.Errorf("expected solo account_id=%q, got %q", soloAcctID, soloPlan.AccountID)
	}
}

func TestBusinessParentInheritance(t *testing.T) {
	t.Parallel()
	db, q := setupAccountTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	ownerUserID := "usr_entowner001"
	ownerAcctID := "exe_entowner001"
	memberUserID := "usr_entmember01"
	memberAcctID := "exe_entmember01"

	for _, row := range []struct{ uid, email string }{
		{ownerUserID, "business-owner@example.com"},
		{memberUserID, "business-member@example.com"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, row.uid, row.email); err != nil {
			t.Fatalf("insert user %s: %v", row.uid, err)
		}
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, ownerAcctID, ownerUserID); err != nil {
		t.Fatalf("insert owner account: %v", err)
	}
	if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID: ownerAcctID,
		PlanID:    "business:monthly:20260106",
		StartedAt: now,
		ChangedBy: new("test:setup"),
	}); err != nil {
		t.Fatalf("insert owner business plan: %v", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by, parent_id) VALUES (?, ?, ?)`, memberAcctID, memberUserID, ownerAcctID); err != nil {
		t.Fatalf("insert member account with parent_id: %v", err)
	}
	if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID: memberAcctID,
		PlanID:    "basic",
		StartedAt: now,
		ChangedBy: new("test:setup"),
	}); err != nil {
		t.Fatalf("insert member basic plan: %v", err)
	}

	ownerPlan, err := q.GetActivePlanForUser(ctx, ownerUserID)
	if err != nil {
		t.Fatalf("GetActivePlanForUser(owner): %v", err)
	}
	if ownerPlan.PlanID != "business:monthly:20260106" {
		t.Errorf("owner plan = %q, want %q", ownerPlan.PlanID, "business:monthly:20260106")
	}

	memberPlan, err := q.GetActivePlanForUser(ctx, memberUserID)
	if err != nil {
		t.Fatalf("GetActivePlanForUser(member): %v", err)
	}
	if memberPlan.PlanID != "business:monthly:20260106" {
		t.Errorf("member inherited plan = %q, want %q (from parent)", memberPlan.PlanID, "business:monthly:20260106")
	}
	if memberPlan.AccountID != ownerAcctID {
		t.Errorf("member account_id = %q, want %q (parent's account)", memberPlan.AccountID, ownerAcctID)
	}

	if _, ok := plan.ByID(memberPlan.PlanID); !ok {
		t.Fatal("ByID failed for business plan")
	}
	if !plan.Grants(memberPlan.PlanID, plan.VMCreate) {
		t.Error("Business plan should grant VMCreate")
	}
	if !plan.Grants(memberPlan.PlanID, plan.CreditPurchase) {
		t.Error("Business plan should grant CreditPurchase")
	}
}

func TestHadTrial(t *testing.T) {
	t.Parallel()

	db, queries := setupAccountTestDB(t)
	ctx := context.Background()

	userID := "usr_stripeless001"
	accountID := "exe_stripeless001"
	if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "stripeless@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, accountID, userID); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	// No plan history at all — should report false.
	had, err := queries.HadTrial(ctx, accountID)
	if err != nil {
		t.Fatalf("HadTrial: %v", err)
	}
	if had != 0 {
		t.Fatal("expected no stripeless trial for account with no plans")
	}

	// Add a non-stripeless plan — should still report false.
	if err := queries.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID: accountID,
		PlanID:    "basic:monthly:20260106",
		StartedAt: time.Now(),
		ChangedBy: new("system:signup"),
	}); err != nil {
		t.Fatalf("InsertAccountPlan: %v", err)
	}
	had, err = queries.HadTrial(ctx, accountID)
	if err != nil {
		t.Fatalf("HadTrial: %v", err)
	}
	if had != 0 {
		t.Fatal("expected no trial for account with only a basic plan")
	}

	// Close that plan, add a stripeless trial plan.
	if err := queries.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{
		AccountID: accountID,
		EndedAt:   new(time.Now()),
	}); err != nil {
		t.Fatalf("CloseAccountPlan: %v", err)
	}
	if err := queries.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID:      accountID,
		PlanID:         "trial:monthly:20260106",
		StartedAt:      time.Now(),
		TrialExpiresAt: new(time.Now().Add(7 * 24 * time.Hour)),
		ChangedBy:      new("system:stripeless_trial"),
	}); err != nil {
		t.Fatalf("InsertAccountPlan: %v", err)
	}
	had, err = queries.HadTrial(ctx, accountID)
	if err != nil {
		t.Fatalf("HadTrial: %v", err)
	}
	if had != 1 {
		t.Fatal("expected trial to be detected")
	}

	// Close the trial plan — should still report true (checks history, not just active).
	if err := queries.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{
		AccountID: accountID,
		EndedAt:   new(time.Now()),
	}); err != nil {
		t.Fatalf("CloseAccountPlan: %v", err)
	}
	had, err = queries.HadTrial(ctx, accountID)
	if err != nil {
		t.Fatalf("HadTrial: %v", err)
	}
	if had != 1 {
		t.Fatal("expected trial to be detected even after plan closed")
	}

	// Stripe trial: individual plan with trial_expires_at set.
	stripeUserID := "usr_stripe_trial001"
	stripeAccountID := "exe_stripe_trial001"
	if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, stripeUserID, "stripe-trial@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, stripeAccountID, stripeUserID); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if err := queries.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
		AccountID:      stripeAccountID,
		PlanID:         "individual:monthly:20260106",
		StartedAt:      time.Now(),
		TrialExpiresAt: new(time.Now().Add(7 * 24 * time.Hour)),
		ChangedBy:      new("stripe:event"),
	}); err != nil {
		t.Fatalf("InsertAccountPlan: %v", err)
	}
	had, err = queries.HadTrial(ctx, stripeAccountID)
	if err != nil {
		t.Fatalf("HadTrial: %v", err)
	}
	if had != 1 {
		t.Fatal("expected Stripe trial (individual plan with trial_expires_at) to be detected")
	}
}

func TestExpiredTrialCandidates(t *testing.T) {
	t.Parallel()

	db, queries := setupAccountTestDB(t)
	ctx := context.Background()

	// Helper to insert a user + account + trial plan + box.
	setup := func(t *testing.T, userID, accountID, planID string, trialExpiresAt *time.Time, boxStatus string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, userID+"@example.com"); err != nil {
			t.Fatalf("insert user: %v", err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO accounts (id, created_by) VALUES (?, ?)`, accountID, userID); err != nil {
			t.Fatalf("insert account: %v", err)
		}
		if err := queries.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID:      accountID,
			PlanID:         planID,
			StartedAt:      time.Now().Add(-48 * time.Hour),
			TrialExpiresAt: trialExpiresAt,
			ChangedBy:      new("system:stripeless_trial"),
		}); err != nil {
			t.Fatalf("InsertAccountPlan: %v", err)
		}
		if _, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "ctr-01",
			Name:            userID + "-box",
			Status:          boxStatus,
			Image:           "ubuntu",
			CreatedByUserID: userID,
			Region:          "us",
		}); err != nil {
			t.Fatalf("InsertBox: %v", err)
		}
	}

	oldExpired := new(time.Now().Add(-2 * time.Hour))
	newExpired := new(time.Now().Add(-1 * time.Hour))
	active := new(time.Now().Add(6 * 24 * time.Hour))

	// Case 1: older expired trial + running box -> should be returned first.
	setup(t, "usr_exp_run", "acct_exp_run", "trial:monthly:20260106", oldExpired, "running")

	// Case 2: active trial + running box -> should NOT appear.
	setup(t, "usr_act_run", "acct_act_run", "trial:monthly:20260106", active, "running")

	// Case 3: newer expired trial + stopped box -> should appear second.
	setup(t, "usr_exp_stop", "acct_exp_stop", "trial:monthly:20260106", newExpired, "stopped")

	// Case 4: non-trial plan + running box -> should NOT appear.
	setup(t, "usr_basic_run", "acct_basic_run", "basic:monthly:20260106", nil, "running")

	// Case 5: expired trial but account has parent_id (team member whose
	// effective plan resolves through the billing owner's account) -> should
	// NOT appear. plan.ForUser would otherwise skip these, churning the
	// enforcer loop.
	olderExpired := new(time.Now().Add(-3 * time.Hour))
	setup(t, "usr_team_member", "acct_team_member", "trial:monthly:20260106", olderExpired, "running")
	if _, err := db.ExecContext(ctx, `UPDATE accounts SET parent_id = ? WHERE id = ?`, "acct_exp_run", "acct_team_member"); err != nil {
		t.Fatalf("set parent_id: %v", err)
	}

	candidates, err := queries.ExpiredTrialCandidates(ctx)
	if err != nil {
		t.Fatalf("ExpiredTrialCandidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 expired trial candidates, got %d", len(candidates))
	}
	if candidates[0].UserID != "usr_exp_run" || candidates[0].AccountID != "acct_exp_run" {
		t.Fatalf("first candidate = (%s, %s), want (usr_exp_run, acct_exp_run)", candidates[0].UserID, candidates[0].AccountID)
	}
	if candidates[1].UserID != "usr_exp_stop" || candidates[1].AccountID != "acct_exp_stop" {
		t.Fatalf("second candidate = (%s, %s), want (usr_exp_stop, acct_exp_stop)", candidates[1].UserID, candidates[1].AccountID)
	}
}
