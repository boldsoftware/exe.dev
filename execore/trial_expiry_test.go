package execore

import (
	"context"
	"testing"
	"time"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
	"exe.dev/sqlite"
	"golang.org/x/time/rate"
)

func TestTrialExpiryEnforcerEnforcesExpiredTrial(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env.SkipBilling = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	expiredUserID := "usr_trial_expired"
	expiredAccountID := "acct_trial_expired"
	createTrialUserWithBox(t, s, ctx, expiredUserID, expiredAccountID,
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")

	activeUserID := "usr_trial_active"
	activeAccountID := "acct_trial_active"
	createTrialUserWithBox(t, s, ctx, activeUserID, activeAccountID,
		"trial:monthly:20260106", time.Now().Add(6*24*time.Hour), "running")

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}

	go s.startTrialExpiryEnforcer(ctx)
	waitForRunningBoxCount(t, s, ctx, expiredUserID, 0)

	assertPlanCategory(t, s, ctx, expiredUserID, plan.CategoryBasic)
	assertRunningBoxCount(t, s, ctx, activeUserID, 1)
	assertPlanCategory(t, s, ctx, activeUserID, plan.CategoryTrial)
}

func TestTrialExpiryEnforcerSkipsSubscribedUser(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env.SkipBilling = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Set up the user (expired trial → individual subscription) before
	// enabling the enforcer so no background trial-expiry loop can race
	// with the plan swap and transition the user to basic.
	userID := "usr_trial_then_sub"
	accountID := "acct_trial_then_sub"
	createTrialUserWithBox(t, s, ctx, userID, accountID,
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")

	if err := s.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		if err := q.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{
			AccountID: accountID,
			EndedAt:   new(time.Now()),
		}); err != nil {
			return err
		}
		return q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: accountID,
			PlanID:    "individual:monthly:20260106",
			StartedAt: time.Now(),
			ChangedBy: new("stripe:event"),
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

	s := newUnstartedServer(t)
	s.env.SkipBilling = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	userID := "usr_wake_test"
	accountID := "acct_wake_test"
	createTrialUserWithBox(t, s, ctx, userID, accountID,
		"trial:monthly:20260106", time.Now().Add(24*time.Hour), "running")

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}

	go s.startTrialExpiryEnforcer(ctx)
	assertRunningBoxCount(t, s, ctx, userID, 1)

	if err := withTx1(s, ctx, (*exedb.Queries).DebugSetTrialExpiresAt, exedb.DebugSetTrialExpiresAtParams{
		AccountID:      accountID,
		TrialExpiresAt: new(time.Now().Add(-1 * time.Hour)),
	}); err != nil {
		t.Fatalf("DebugSetTrialExpiresAt: %v", err)
	}
	s.wakeTrialExpiryEnforcer()

	waitForRunningBoxCount(t, s, ctx, userID, 0)
}

func TestTrialExpiryEnforcerDisabled(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
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

func TestTrialExpiryEnforcerContinuesPastBrokenUser(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env.SkipBilling = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	brokenUserID := "usr_trial_broken"
	createTrialUserWithBox(t, s, ctx, brokenUserID, "acct_trial_broken",
		"trial:monthly:20260106", time.Now().Add(-2*time.Hour), "running")
	deleteUserRow(t, s, ctx, brokenUserID)

	goodUserID := "usr_trial_good"
	createTrialUserWithBox(t, s, ctx, goodUserID, "acct_trial_good",
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}

	go s.startTrialExpiryEnforcer(ctx)

	waitForRunningBoxCount(t, s, ctx, goodUserID, 0)
	assertRunningBoxCount(t, s, ctx, brokenUserID, 1)
	assertPlanCategory(t, s, ctx, goodUserID, plan.CategoryBasic)

	deferred := listTrialExpiryDeferredAccounts(t, s, ctx)
	if len(deferred) != 1 {
		t.Fatalf("expected 1 deferred account, got %d", len(deferred))
	}
	if deferred[0].AccountID != "acct_trial_broken" || deferred[0].Kind != "error" {
		t.Fatalf("deferred account = (%s, %s), want (acct_trial_broken, error)", deferred[0].AccountID, deferred[0].Kind)
	}
}

func TestTrialExpiryEnforcerEnforcesAtMostOneUserPerPass(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env.SkipBilling = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	createTrialUserWithBox(t, s, ctx, "usr_once1", "acct_once1",
		"trial:monthly:20260106", time.Now().Add(-2*time.Hour), "running")
	createTrialUserWithBox(t, s, ctx, "usr_once2", "acct_once2",
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}
	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryRateLimit, "1h"); err != nil {
		t.Fatalf("SetTrialExpiryRateLimit: %v", err)
	}

	go s.startTrialExpiryEnforcer(ctx)
	waitForExactlyRunningUsers(t, s, ctx, []string{"usr_once1", "usr_once2"}, 1)
}

func TestTrialExpiryPassWithQueueKeepsRemainingCandidates(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env.SkipBilling = false
	ctx := t.Context()

	firstUserID := "usr_queue1"
	secondUserID := "usr_queue2"
	createTrialUserWithBox(t, s, ctx, firstUserID, "acct_queue1",
		"trial:monthly:20260106", time.Now().Add(-2*time.Hour), "running")
	createTrialUserWithBox(t, s, ctx, secondUserID, "acct_queue2",
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}
	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryRateLimit, "1h"); err != nil {
		t.Fatalf("SetTrialExpiryRateLimit: %v", err)
	}

	var candidateQueue queue[exedb.ExpiredTrialCandidatesRow]
	if delay := s.runTrialExpiryPass(ctx, &candidateQueue); delay != 0 {
		t.Fatalf("first runTrialExpiryPass() = %v, want 0", delay)
	}
	if len(candidateQueue) != 1 {
		t.Fatalf("expected 1 candidate left in queue, got %d", len(candidateQueue))
	}
	if candidateQueue[0].AccountID != "acct_queue2" || candidateQueue[0].UserID != secondUserID {
		t.Fatalf("queue[0] = (%s, %s), want (acct_queue2, %s)", candidateQueue[0].AccountID, candidateQueue[0].UserID, secondUserID)
	}
	assertPlanCategory(t, s, ctx, firstUserID, plan.CategoryBasic)
	secondPlan := activeAccountPlan(t, s, ctx, "acct_queue2")
	if secondPlan.PlanID != "trial:monthly:20260106" {
		t.Fatalf("active plan for acct_queue2 = %q, want trial:monthly:20260106", secondPlan.PlanID)
	}
	if secondPlan.ChangedBy == nil || *secondPlan.ChangedBy != "system:stripeless_trial" {
		t.Fatalf("active plan changed_by for acct_queue2 = %v, want system:stripeless_trial", secondPlan.ChangedBy)
	}
	if history := accountPlanHistory(t, s, ctx, "acct_queue2"); len(history) != 1 {
		t.Fatalf("plan history for acct_queue2 has %d rows, want 1", len(history))
	}
	assertRunningBoxCount(t, s, ctx, secondUserID, 1)

	delay := s.runTrialExpiryPass(ctx, &candidateQueue)
	if delay <= 0 || delay >= trialExpiryIdlePollInterval {
		t.Fatalf("second runTrialExpiryPass() = %v, want positive rate-limit delay less than idle poll", delay)
	}
	if len(candidateQueue) != 1 || candidateQueue[0].AccountID != "acct_queue2" {
		t.Fatalf("queue after rate-limited pass = %+v, want acct_queue2 still pending", candidateQueue)
	}
	secondPlan = activeAccountPlan(t, s, ctx, "acct_queue2")
	if secondPlan.PlanID != "trial:monthly:20260106" {
		t.Fatalf("active plan for acct_queue2 after rate-limited pass = %q, want trial:monthly:20260106", secondPlan.PlanID)
	}
	if secondPlan.ChangedBy == nil || *secondPlan.ChangedBy != "system:stripeless_trial" {
		t.Fatalf("active plan changed_by for acct_queue2 after rate-limited pass = %v, want system:stripeless_trial", secondPlan.ChangedBy)
	}
	if history := accountPlanHistory(t, s, ctx, "acct_queue2"); len(history) != 1 {
		t.Fatalf("plan history for acct_queue2 after rate-limited pass has %d rows, want 1", len(history))
	}
	assertRunningBoxCount(t, s, ctx, secondUserID, 1)
}

func TestTrialExpiryPassSkipsStaleQueuedCandidate(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env.SkipBilling = false
	ctx := t.Context()

	createTrialUserWithBox(t, s, ctx, "usr_stale_first", "acct_stale_first",
		"trial:monthly:20260106", time.Now().Add(-2*time.Hour), "running")
	createTrialUserWithBox(t, s, ctx, "usr_stale_second", "acct_stale_second",
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}
	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryRateLimit, "1h"); err != nil {
		t.Fatalf("SetTrialExpiryRateLimit: %v", err)
	}

	var candidateQueue queue[exedb.ExpiredTrialCandidatesRow]
	if delay := s.runTrialExpiryPass(ctx, &candidateQueue); delay != 0 {
		t.Fatalf("first runTrialExpiryPass() = %v, want 0", delay)
	}
	if len(candidateQueue) != 1 || candidateQueue[0].AccountID != "acct_stale_second" {
		t.Fatalf("queue after first pass = %+v, want acct_stale_second pending", candidateQueue)
	}

	changeAt := time.Now()
	if err := s.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
			AccountID: "acct_stale_second",
			PlanID:    plan.ID(plan.CategoryRestricted),
			At:        changeAt,
			ChangedBy: "test:stale_candidate",
		})
	}); err != nil {
		t.Fatalf("replace stale candidate plan: %v", err)
	}

	// Give the next pass a fresh token so it reaches the stale-candidate logic.
	s.trialExpiryLimiter = rate.NewLimiter(rate.Limit(1), 1)

	delay := s.runTrialExpiryPass(ctx, &candidateQueue)
	if delay <= 0 || delay >= trialExpiryIdlePollInterval {
		t.Fatalf("second runTrialExpiryPass() = %v, want positive retry delay less than idle poll", delay)
	}
	if len(candidateQueue) != 0 {
		t.Fatalf("queue after stale candidate pass = %+v, want empty", candidateQueue)
	}

	activePlan := activeAccountPlan(t, s, ctx, "acct_stale_second")
	if activePlan.PlanID != plan.ID(plan.CategoryRestricted) {
		t.Fatalf("active plan for acct_stale_second = %q, want %q", activePlan.PlanID, plan.ID(plan.CategoryRestricted))
	}
	if activePlan.ChangedBy == nil || *activePlan.ChangedBy != "test:stale_candidate" {
		t.Fatalf("active plan changed_by for acct_stale_second = %v, want test:stale_candidate", activePlan.ChangedBy)
	}
	assertRunningBoxCount(t, s, ctx, "usr_stale_second", 1)

	history := accountPlanHistory(t, s, ctx, "acct_stale_second")
	if len(history) != 2 {
		t.Fatalf("plan history for acct_stale_second has %d rows, want 2", len(history))
	}
	for _, row := range history {
		if row.ChangedBy != nil && *row.ChangedBy == "system:trial_expired" {
			t.Fatalf("unexpected system:trial_expired history row for acct_stale_second: %+v", row)
		}
	}
}

func TestTrialExpiryWakeForcesQueueRefresh(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env.SkipBilling = false
	ctx := t.Context()

	createTrialUserWithBox(t, s, ctx, "usr_refresh1", "acct_refresh1",
		"trial:monthly:20260106", time.Now().Add(-3*time.Hour), "running")
	createTrialUserWithBox(t, s, ctx, "usr_refresh2", "acct_refresh2",
		"trial:monthly:20260106", time.Now().Add(-2*time.Hour), "running")

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}
	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryRateLimit, "1h"); err != nil {
		t.Fatalf("SetTrialExpiryRateLimit: %v", err)
	}

	var candidateQueue queue[exedb.ExpiredTrialCandidatesRow]
	if delay := s.runTrialExpiryPass(ctx, &candidateQueue); delay != 0 {
		t.Fatalf("first runTrialExpiryPass() = %v, want 0", delay)
	}
	if len(candidateQueue) != 1 || candidateQueue[0].AccountID != "acct_refresh2" {
		t.Fatalf("queue after first pass = %+v, want acct_refresh2 pending", candidateQueue)
	}

	createTrialUserWithBox(t, s, ctx, "usr_refresh3", "acct_refresh3",
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")

	s.wakeTrialExpiryEnforcer()

	delay := s.runTrialExpiryPass(ctx, &candidateQueue)
	if delay <= 0 || delay >= trialExpiryIdlePollInterval {
		t.Fatalf("second runTrialExpiryPass() = %v, want positive rate-limit delay less than idle poll", delay)
	}
	if len(candidateQueue) != 2 {
		t.Fatalf("expected refreshed queue to have 2 pending candidates, got %d", len(candidateQueue))
	}
	if candidateQueue[0].AccountID != "acct_refresh2" || candidateQueue[1].AccountID != "acct_refresh3" {
		t.Fatalf("refreshed queue = (%s, %s), want (acct_refresh2, acct_refresh3)", candidateQueue[0].AccountID, candidateQueue[1].AccountID)
	}
}

func TestTrialExpiryRateLimit(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env.SkipBilling = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	createTrialUserWithBox(t, s, ctx, "usr_rate1", "acct_rate1",
		"trial:monthly:20260106", time.Now().Add(-2*time.Hour), "running")
	createTrialUserWithBox(t, s, ctx, "usr_rate2", "acct_rate2",
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}
	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryRateLimit, "1h"); err != nil {
		t.Fatalf("SetTrialExpiryRateLimit: %v", err)
	}

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

	s := newUnstartedServer(t)
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

func TestTrialExpiryPassDelayUsesFutureNextExpiry(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env.SkipBilling = false
	ctx := t.Context()

	expiry := time.Now().Add(12 * time.Hour)
	createTrialUserWithBox(t, s, ctx, "usr_future_expiry", "acct_future_expiry",
		"trial:monthly:20260106", expiry, "running")

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}

	delay := s.nextTrialExpiryPassDelay(ctx)
	if delay < 11*time.Hour || delay > 12*time.Hour+2*time.Second {
		t.Fatalf("nextTrialExpiryPassDelay() = %v, want roughly 12h until next expiry", delay)
	}
}

func TestTrialExpiryEnforcerWakeKeepsFutureNextExpiry(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env.SkipBilling = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	expiry := time.Now().Add(12 * time.Hour)
	createTrialUserWithBox(t, s, ctx, "usr_future_wake", "acct_future_wake",
		"trial:monthly:20260106", expiry, "running")

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}

	go s.startTrialExpiryEnforcer(ctx)

	initialWake := waitForTrialExpiryNextWake(t, s)
	if delay := time.Until(*initialWake); delay < 11*time.Hour || delay > 12*time.Hour+2*time.Second {
		t.Fatalf("initial next wake = %v, want roughly 12h until next expiry", delay)
	}

	s.wakeTrialExpiryEnforcer()

	rescheduledWake := waitForTrialExpiryNextWakeChange(t, s, initialWake)
	if delay := time.Until(*rescheduledWake); delay < 11*time.Hour || delay > 12*time.Hour+2*time.Second {
		t.Fatalf("rescheduled next wake = %v, want roughly 12h until next expiry", delay)
	}
}

func TestTrialExpiryPassReturnsRetryDelayWhenAllCandidatesFail(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env.SkipBilling = false
	ctx := t.Context()

	brokenUserID := "usr_trial_fail_only"
	createTrialUserWithBox(t, s, ctx, brokenUserID, "acct_trial_fail_only",
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")
	deleteUserRow(t, s, ctx, brokenUserID)

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}

	var candidateQueue queue[exedb.ExpiredTrialCandidatesRow]
	delay := s.runTrialExpiryPass(ctx, &candidateQueue)
	if delay <= 0 || delay >= trialExpiryIdlePollInterval {
		t.Fatalf("runTrialExpiryPass() = %v, want positive retry delay less than idle poll", delay)
	}
	if nextDelay := s.nextTrialExpiryPassDelay(ctx); nextDelay <= 0 || nextDelay >= trialExpiryIdlePollInterval {
		t.Fatalf("nextTrialExpiryPassDelay() = %v, want deferred retry before idle poll", nextDelay)
	}

	deferred := listTrialExpiryDeferredAccounts(t, s, ctx)
	if len(deferred) != 1 {
		t.Fatalf("expected 1 deferred account, got %d", len(deferred))
	}
	if deferred[0].AccountID != "acct_trial_fail_only" || deferred[0].Kind != "error" {
		t.Fatalf("deferred account = (%s, %s), want (acct_trial_fail_only, error)", deferred[0].AccountID, deferred[0].Kind)
	}
}

func TestTrialExpiryPassDefersSkippedUser(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env.SkipBilling = false
	ctx := t.Context()

	userID := "usr_trial_then_sub_defer"
	accountID := "acct_trial_then_sub_defer"
	createTrialUserWithBox(t, s, ctx, userID, accountID,
		"trial:monthly:20260106", time.Now().Add(-1*time.Hour), "running")

	// Keep the active plan as a trial candidate, but add an active billing event
	// so plan.ForUser resolves to Individual and the enforcer defers it.
	if err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `INSERT INTO billing_events (account_id, event_type, event_at) VALUES (?, 'active', datetime('now'))`,
			accountID)
		return err
	}); err != nil {
		t.Fatalf("insert billing event: %v", err)
	}

	if err := withTx1(s, ctx, (*exedb.Queries).SetTrialExpiryEnforcerEnabled, "true"); err != nil {
		t.Fatalf("SetTrialExpiryEnforcerEnabled: %v", err)
	}

	var candidateQueue queue[exedb.ExpiredTrialCandidatesRow]
	delay := s.runTrialExpiryPass(ctx, &candidateQueue)
	if delay <= 0 || delay >= trialExpiryIdlePollInterval {
		t.Fatalf("runTrialExpiryPass() = %v, want positive retry delay less than idle poll", delay)
	}

	deferred := listTrialExpiryDeferredAccounts(t, s, ctx)
	if len(deferred) != 1 {
		t.Fatalf("expected 1 deferred account, got %d", len(deferred))
	}
	if deferred[0].AccountID != accountID || deferred[0].Kind != "skip" {
		t.Fatalf("deferred account = (%s, %s), want (%s, skip)", deferred[0].AccountID, deferred[0].Kind, accountID)
	}

	assertRunningBoxCount(t, s, ctx, userID, 1)
	assertPlanCategory(t, s, ctx, userID, plan.CategoryIndividual)
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
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
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
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
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

func waitForTrialExpiryNextWake(t *testing.T, s *Server) *time.Time {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if nextWake := s.trialExpiryNextWake.Load(); nextWake != nil {
			return nextWake
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for trialExpiryNextWake")
	return nil
}

func waitForTrialExpiryNextWakeChange(t *testing.T, s *Server, previous *time.Time) *time.Time {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if nextWake := s.trialExpiryNextWake.Load(); nextWake != nil && nextWake != previous {
			return nextWake
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for trialExpiryNextWake to change")
	return nil
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

func activeAccountPlan(t *testing.T, s *Server, ctx context.Context, accountID string) exedb.AccountPlan {
	t.Helper()
	ap, err := withRxRes1(s, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan(%s): %v", accountID, err)
	}
	return ap
}

func accountPlanHistory(t *testing.T, s *Server, ctx context.Context, accountID string) []exedb.AccountPlan {
	t.Helper()
	history, err := withRxRes1(s, ctx, (*exedb.Queries).ListAccountPlanHistory, accountID)
	if err != nil {
		t.Fatalf("ListAccountPlanHistory(%s): %v", accountID, err)
	}
	return history
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
			ChangedBy:      new("system:stripeless_trial"),
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

func deleteUserRow(t *testing.T, s *Server, ctx context.Context, userID string) {
	t.Helper()
	if err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `DELETE FROM users WHERE user_id = ?`, userID)
		return err
	}); err != nil {
		t.Fatalf("deleteUserRow(%s): %v", userID, err)
	}
}

func listTrialExpiryDeferredAccounts(t *testing.T, s *Server, ctx context.Context) []trialExpirySkipAccount {
	t.Helper()
	_ = ctx
	return s.trialExpirySkipAccountsSnapshot()
}
