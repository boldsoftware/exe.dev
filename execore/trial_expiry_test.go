package execore

import (
	"context"
	"testing"
	"time"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
	"exe.dev/sqlite"
)

func TestTrialExpiryEnforcerEnforcesExpiredTrial(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)
	s.env.SkipBilling = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}

	expiredUserID := "usr_trial_expired"
	expiredAccountID := "acct_trial_expired"
	createTrialUserWithBox(t, s, ctx, expiredUserID, expiredAccountID,
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")

	activeUserID := "usr_trial_active"
	activeAccountID := "acct_trial_active"
	createTrialUserWithBox(t, s, ctx, activeUserID, activeAccountID,
		"trial:monthly:20260106", time.Now().Add(6*24*time.Hour), "running")

	go s.startTrialExpiryEnforcer(ctx)
	waitForRunningBoxCount(t, s, ctx, expiredUserID, 0)

	assertPlanCategory(t, s, ctx, expiredUserID, plan.CategoryBasic)
	assertRunningBoxCount(t, s, ctx, activeUserID, 1)
	assertPlanCategory(t, s, ctx, activeUserID, plan.CategoryTrial)
}

func TestTrialExpiryEnforcerSkipsSubscribedUser(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)
	s.env.SkipBilling = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Set up the user (expired trial → individual subscription) before
	// enabling the enforcer, so the auto-started enforcer (see exe.go's
	// startTrialExpiryEnforcer goroutine launched during Serve) cannot
	// race with the plan swap and transition the user to basic.
	userID := "usr_trial_then_sub"
	accountID := "acct_trial_then_sub"
	createTrialUserWithBox(t, s, ctx, userID, accountID,
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")

	if err := s.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		if err := q.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{
			AccountID: accountID,
			EndedAt:   timePtr(time.Now()),
		}); err != nil {
			return err
		}
		return q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: accountID,
			PlanID:    "individual:monthly:20260106",
			StartedAt: time.Now(),
			ChangedBy: strPtr("stripe:event"),
		})
	}); err != nil {
		t.Fatalf("swap trial for individual plan: %v", err)
	}
	if err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `INSERT INTO billing_events (account_id, event_type, event_at) VALUES (?, 'active', datetime('now'))`,
			accountID)
		return err
	}); err != nil {
		t.Fatalf("insert billing event: %v", err)
	}

	assertPlanCategory(t, s, ctx, userID, plan.CategoryIndividual)

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}
	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryRateLimit, "1ms"); err != nil {
		t.Fatalf("SetTrialExpiryRateLimit: %v", err)
	}

	go s.startTrialExpiryEnforcer(ctx)
	assertUserRemainsRunning(t, s, ctx, userID)
	assertPlanCategory(t, s, ctx, userID, plan.CategoryIndividual)
}

func TestTrialExpiryEnforcerWakesOnNotify(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)
	s.env.SkipBilling = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}

	userID := "usr_wake_test"
	accountID := "acct_wake_test"
	createTrialUserWithBox(t, s, ctx, userID, accountID,
		"trial:monthly:20260106", time.Now().Add(24*time.Hour), "running")

	go s.startTrialExpiryEnforcer(ctx)
	assertRunningBoxCount(t, s, ctx, userID, 1)

	if err := withTx1(s, ctx, (*exedb.Queries).DebugSetTrialExpiresAt, exedb.DebugSetTrialExpiresAtParams{
		AccountID:      accountID,
		TrialExpiresAt: timePtr(time.Now().Add(-1 * time.Hour)),
	}); err != nil {
		t.Fatalf("DebugSetTrialExpiresAt: %v", err)
	}
	s.wakeTrialExpiryEnforcer()

	waitForRunningBoxCount(t, s, ctx, userID, 0)
}

func TestTrialExpiryEnforcerDisabled(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)
	s.env.SkipBilling = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	userID := "usr_disabled_test"
	accountID := "acct_disabled_test"
	createTrialUserWithBox(t, s, ctx, userID, accountID,
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")

	go s.startTrialExpiryEnforcer(ctx)
	assertUserRemainsRunning(t, s, ctx, userID)
}

func TestTrialExpiryEnforcerEnforcesAtMostOneUserPerPass(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)
	s.env.SkipBilling = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}
	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryRateLimit, "1h"); err != nil {
		t.Fatalf("SetTrialExpiryRateLimit: %v", err)
	}

	createTrialUserWithBox(t, s, ctx, "usr_once1", "acct_once1",
		"trial:monthly:20260106", time.Now().Add(-2*time.Hour), "running")
	createTrialUserWithBox(t, s, ctx, "usr_once2", "acct_once2",
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")

	go s.startTrialExpiryEnforcer(ctx)
	waitForExactlyRunningUsers(t, s, ctx, []string{"usr_once1", "usr_once2"}, 1)
}

func TestTrialExpiryRateLimit(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)
	s.env.SkipBilling = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}
	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryRateLimit, "1h"); err != nil {
		t.Fatalf("SetTrialExpiryRateLimit: %v", err)
	}

	createTrialUserWithBox(t, s, ctx, "usr_rate1", "acct_rate1",
		"trial:monthly:20260106", time.Now().Add(-2*time.Hour), "running")
	createTrialUserWithBox(t, s, ctx, "usr_rate2", "acct_rate2",
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")

	go s.startTrialExpiryEnforcer(ctx)
	waitForExactlyRunningUsers(t, s, ctx, []string{"usr_rate1", "usr_rate2"}, 1)

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryRateLimit, "1ms"); err != nil {
		t.Fatalf("SetTrialExpiryRateLimit: %v", err)
	}
	s.wakeTrialExpiryEnforcer()
	waitForExactlyRunningUsers(t, s, ctx, []string{"usr_rate1", "usr_rate2"}, 0)
}

func TestTrialExpiryNextExpiry(t *testing.T) {
	t.Parallel()

	s := newTestServer(t)
	ctx := t.Context()

	next := s.nextTrialExpiry(ctx)
	if next != nil {
		t.Fatalf("expected nil, got %v", *next)
	}

	userID := "usr_next_exp"
	accountID := "acct_next_exp"
	expiry := time.Now().Add(3 * 24 * time.Hour)
	createTrialUserWithBox(t, s, ctx, userID, accountID,
		"trial:monthly:20260106", expiry, "running")

	next = s.nextTrialExpiry(ctx)
	if next == nil {
		t.Fatal("expected non-nil next expiry")
	}
	if diff := next.Sub(expiry).Abs(); diff > 2*time.Second {
		t.Fatalf("next expiry %v differs from expected %v by %v", *next, expiry, diff)
	}
}

func assertRunningBoxCount(t *testing.T, s *Server, ctx context.Context, userID string, want int) {
	t.Helper()
	boxes, err := withRxRes1(s, ctx, (*exedb.Queries).GetRunningBoxesForUser, userID)
	if err != nil {
		t.Fatalf("GetRunningBoxesForUser(%s): %v", userID, err)
	}
	if len(boxes) != want {
		t.Fatalf("expected %d running boxes for %s, got %d", want, userID, len(boxes))
	}
}

func waitForRunningBoxCount(t *testing.T, s *Server, ctx context.Context, userID string, want int) {
	t.Helper()
	for range 100 {
		boxes, err := withRxRes1(s, ctx, (*exedb.Queries).GetRunningBoxesForUser, userID)
		if err != nil {
			t.Fatalf("GetRunningBoxesForUser(%s): %v", userID, err)
		}
		if len(boxes) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	assertRunningBoxCount(t, s, ctx, userID, want)
}

func assertUserRemainsRunning(t *testing.T, s *Server, ctx context.Context, userID string) {
	t.Helper()
	for range 20 {
		boxes, err := withRxRes1(s, ctx, (*exedb.Queries).GetRunningBoxesForUser, userID)
		if err != nil {
			t.Fatalf("GetRunningBoxesForUser(%s): %v", userID, err)
		}
		if len(boxes) != 1 {
			t.Fatalf("expected 1 running box for %s, got %d", userID, len(boxes))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForExactlyRunningUsers(t *testing.T, s *Server, ctx context.Context, userIDs []string, want int) {
	t.Helper()
	for range 100 {
		got := 0
		for _, userID := range userIDs {
			boxes, err := withRxRes1(s, ctx, (*exedb.Queries).GetRunningBoxesForUser, userID)
			if err != nil {
				t.Fatalf("GetRunningBoxesForUser(%s): %v", userID, err)
			}
			if len(boxes) > 0 {
				got++
			}
		}
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	got := 0
	for _, userID := range userIDs {
		boxes, err := withRxRes1(s, ctx, (*exedb.Queries).GetRunningBoxesForUser, userID)
		if err != nil {
			t.Fatalf("GetRunningBoxesForUser(%s): %v", userID, err)
		}
		if len(boxes) > 0 {
			got++
		}
	}
	t.Fatalf("expected %d users with running boxes, got %d", want, got)
}

func assertPlanCategory(t *testing.T, s *Server, ctx context.Context, userID string, want plan.Category) {
	t.Helper()
	var got plan.Category
	if err := s.withRx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		var err error
		got, err = plan.ForUser(ctx, q, userID)
		return err
	}); err != nil {
		t.Fatalf("ForUser(%s): %v", userID, err)
	}
	if got != want {
		t.Fatalf("expected %v for %s, got %v", want, userID, got)
	}
}

func createTrialUserWithBox(t *testing.T, s *Server, ctx context.Context, userID, accountID, planID string, trialExpiresAt time.Time, boxStatus string) {
	t.Helper()
	if err := s.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		if err := q.InsertUser(ctx, exedb.InsertUserParams{
			UserID: userID,
			Email:  userID + "@example.com",
			Region: "us",
		}); err != nil {
			return err
		}
		if err := q.InsertAccount(ctx, exedb.InsertAccountParams{
			ID:        accountID,
			CreatedBy: userID,
		}); err != nil {
			return err
		}
		if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID:      accountID,
			PlanID:         planID,
			StartedAt:      time.Now().Add(-48 * time.Hour),
			TrialExpiresAt: &trialExpiresAt,
			ChangedBy:      strPtr("system:stripeless_trial"),
		}); err != nil {
			return err
		}
		if _, err := q.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "ctr-01",
			Name:            userID + "-box",
			Status:          boxStatus,
			Image:           "ubuntu",
			CreatedByUserID: userID,
			Region:          "us",
		}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("createTrialUserWithBox(%s): %v", userID, err)
	}
}

func strPtr(s string) *string        { return &s }
func timePtr(t time.Time) *time.Time { return &t }
