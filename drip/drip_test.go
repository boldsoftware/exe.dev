package drip

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"exe.dev/email"
	"exe.dev/exedb"
	"exe.dev/sqlite"
	"exe.dev/stage"
	"exe.dev/tslog"
)

func testDB(t *testing.T) *sqlite.DB {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"

	// Open raw DB for migrations.
	rawDB, err := sql.Open("sqlite", sqlite.WithTimeParams(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlite.InitDB(rawDB, 1); err != nil {
		t.Fatal(err)
	}
	if err := exedb.RunMigrations(tslog.Slogger(t), rawDB); err != nil {
		t.Fatal(err)
	}
	rawDB.Close()

	db, err := sqlite.New(dbPath, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func testEnv() stage.Env {
	env := stage.Test()
	env.WebHost = "exe.dev"
	return env
}

type sentEmail struct {
	To      string
	Subject string
	Body    string
}

// testNow is a fixed point in time used by all drip tests.
// 20:00 UTC ensures isDue's "past 9 AM local" check passes for all regions
// (the westernmost test region is lax/America/Los_Angeles at UTC-7, so
// 20:00 UTC = 13:00 Pacific, well past the 9 AM gate).
var testNow = time.Date(2026, 4, 15, 20, 0, 0, 0, time.UTC)

func newTestRunner(t *testing.T, db *sqlite.DB) (*Runner, *[]sentEmail) {
	t.Helper()
	var mu sync.Mutex
	var sent []sentEmail
	sendFn := func(ctx context.Context, msg email.Message) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, sentEmail{To: msg.To, Subject: msg.Subject, Body: msg.Body})
		return nil
	}
	r := NewRunner(db, testEnv(), sendFn, tslog.Slogger(t))
	r.now = func() time.Time { return testNow }
	return r, &sent
}

func strPtr(s string) *string { return &s }

// createTrialUser creates a user with a trial plan, returning the user_id.
func createTrialUser(t *testing.T, ctx context.Context, db *sqlite.DB, emailAddr string, createdAt time.Time) string {
	t.Helper()
	userID := "user_" + emailAddr

	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		if err := q.InsertUser(ctx, exedb.InsertUserParams{
			UserID:         userID,
			Email:          emailAddr,
			CanonicalEmail: &emailAddr,
			Region:         "lax",
		}); err != nil {
			return err
		}
		if err := q.InsertAccount(ctx, exedb.InsertAccountParams{
			ID:        "acct_" + userID,
			CreatedBy: userID,
		}); err != nil {
			return err
		}
		expires := createdAt.Add(7 * 24 * time.Hour)
		if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID:      "acct_" + userID,
			PlanID:         "trial:monthly:20260106",
			StartedAt:      createdAt,
			TrialExpiresAt: &expires,
			ChangedBy:      strPtr("test"),
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return userID
}

func TestDay0Welcome_NoVM(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r, sent := newTestRunner(t, db)

	// User signed up 2 hours ago, no VM.
	createTrialUser(t, ctx, db, "alice@test.com", testNow.Add(-2*time.Hour))

	r.runOnce(ctx)

	if len(*sent) != 1 {
		t.Fatalf("expected 1 email sent, got %d", len(*sent))
	}
	if (*sent)[0].Subject != "exe.dev: ready to build" {
		t.Errorf("unexpected subject: %s", (*sent)[0].Subject)
	}
}

func TestDay0Welcome_WithVM_Skipped(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r, sent := newTestRunner(t, db)

	userID := createTrialUser(t, ctx, db, "bob@test.com", testNow.Add(-2*time.Hour))

	// Create a box for the user.
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		_, err := q.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "host1",
			Name:            "bob-vm",
			Status:          "running",
			Image:           "ubuntu",
			CreatedByUserID: userID,
			Region:          "lax",
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	r.runOnce(ctx)

	if len(*sent) != 0 {
		t.Fatalf("expected 0 emails sent, got %d", len(*sent))
	}

	// Verify the skip was recorded.
	sends, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetDripSendsForUser, exedb.GetDripSendsForUserParams{
		UserID:   userID,
		Campaign: campaignTrialOnboarding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sends) != 1 {
		t.Fatalf("expected 1 drip record, got %d", len(sends))
	}
	if sends[0].Status != statSkipped {
		t.Errorf("expected status 'skipped', got %q", sends[0].Status)
	}
	if sends[0].SkipReason == nil || *sends[0].SkipReason != "user already created a VM" {
		t.Errorf("unexpected skip reason: %v", sends[0].SkipReason)
	}
}

func TestDay1Nudge_InactiveUser(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r, sent := newTestRunner(t, db)

	// User signed up 1.5 hours ago, no VM. Day0 is due but day1 is not yet.
	createTrialUser(t, ctx, db, "charlie@test.com", testNow.Add(-90*time.Minute))

	// First run: sends day0.
	r.runOnce(ctx)
	if len(*sent) != 1 {
		t.Fatalf("expected 1 email (day0), got %d", len(*sent))
	}
	if (*sent)[0].Subject != "exe.dev: ready to build" {
		t.Errorf("unexpected day0 subject: %s", (*sent)[0].Subject)
	}

	// Simulate time passing: create a second user 25h into trial.
	// (We need a fresh user for day1 to be due.)
	db2 := testDB(t)
	r2, sent2 := newTestRunner(t, db2)
	createTrialUser(t, ctx, db2, "charlie2@test.com", testNow.Add(-90*time.Minute))

	// First run: day0.
	r2.runOnce(ctx)
	if len(*sent2) != 1 {
		t.Fatalf("expected 1 email, got %d", len(*sent2))
	}

	// Manually insert day0 for a user 25h old to simulate progression.
	db3 := testDB(t)
	r3, sent3 := newTestRunner(t, db3)
	userID := createTrialUser(t, ctx, db3, "charlie3@test.com", testNow.Add(-25*time.Hour))
	// Simulate day0 already processed.
	err := exedb.WithTx1(db3, ctx, (*exedb.Queries).InsertDripSend, exedb.InsertDripSendParams{
		UserID:   userID,
		Campaign: "trial_onboarding",
		Step:     stepDay0Welcome,
		Status:   statSent,
	})
	if err != nil {
		t.Fatal(err)
	}

	r3.runOnce(ctx)
	if len(*sent3) != 1 {
		t.Fatalf("expected 1 email (day1), got %d", len(*sent3))
	}
	if (*sent3)[0].Subject != "exe.dev: 6 days left, start something" {
		t.Errorf("unexpected day1 subject: %s", (*sent3)[0].Subject)
	}
}

func TestNoDoubleDelivery(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r, sent := newTestRunner(t, db)

	createTrialUser(t, ctx, db, "diana@test.com", testNow.Add(-2*time.Hour))

	r.runOnce(ctx)
	r.runOnce(ctx)
	r.runOnce(ctx)

	if len(*sent) != 1 {
		t.Fatalf("expected exactly 1 email across 3 runs, got %d", len(*sent))
	}
}

func TestStepProgression(t *testing.T) {
	// Test the full lifecycle by simulating a user who started their trial
	// at the same time as the drip system, so each step fires in order.
	db := testDB(t)
	ctx := context.Background()
	r, sent := newTestRunner(t, db)

	// User signed up 15 days ago with a VM.
	userID := createTrialUser(t, ctx, db, "eve@test.com", testNow.Add(-15*24*time.Hour))

	// Create a box for the user.
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		_, err := q.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "host1",
			Name:            "eve-vm",
			Status:          "running",
			Image:           "ubuntu",
			CreatedByUserID: userID,
			Region:          "lax",
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// First run: retroactive skip of all overdue steps except the last (day14).
	r.runOnce(ctx)

	// Subsequent runs: no more steps to process.
	r.runOnce(ctx)

	sends, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetDripSendsForUser, exedb.GetDripSendsForUserParams{
		UserID:   userID,
		Campaign: campaignTrialOnboarding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sends) != 7 {
		t.Fatalf("expected 7 drip records, got %d", len(sends))
	}

	stepStatus := make(map[string]string, len(sends))
	for _, s := range sends {
		stepStatus[s.Step] = s.Status
	}

	// day0-day10: retroactive skipped
	for _, step := range []string{stepDay0Welcome, stepDay1Nudge, stepDay3Feature, stepDay5Urgency, stepDay7Expiry, stepDay10WinBack} {
		if stepStatus[step] != statSkipped {
			t.Errorf("%s: expected skipped (retroactive), got %s", step, stepStatus[step])
		}
	}
	// day14: sent (the most recent overdue step)
	if stepStatus[stepDay14Final] != statSent {
		t.Errorf("day14: expected sent, got %s", stepStatus[stepDay14Final])
	}

	// Only 1 email actually sent.
	if len(*sent) != 1 {
		t.Errorf("expected 1 email actually sent, got %d", len(*sent))
	}
}

func TestFullLifecycleOneStepAtATime(t *testing.T) {
	// Simulates a fresh user going through all steps by pre-populating prior steps.
	db := testDB(t)
	ctx := context.Background()

	// User signed up 15 days ago with a VM.
	userID := createTrialUser(t, ctx, db, "lifecycle@test.com", testNow.Add(-15*24*time.Hour))
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		_, err := q.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "host1",
			Name:            "lifecycle-vm",
			Status:          "running",
			Image:           "ubuntu",
			CreatedByUserID: userID,
			Region:          "lax",
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	allSteps := []string{stepDay0Welcome, stepDay1Nudge, stepDay3Feature, stepDay5Urgency, stepDay7Expiry, stepDay10WinBack, stepDay14Final}

	// Pre-populate all steps except the last as already processed.
	for _, step := range allSteps[:len(allSteps)-1] {
		err := exedb.WithTx1(db, ctx, (*exedb.Queries).InsertDripSend, exedb.InsertDripSendParams{
			UserID:     userID,
			Campaign:   "trial_onboarding",
			Step:       step,
			Status:     statSkipped,
			SkipReason: strPtr("test setup"),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	r, sent := newTestRunner(t, db)
	r.runOnce(ctx)

	// day14 should fire since all prior steps are recorded.
	if len(*sent) != 1 {
		t.Fatalf("expected 1 email (day14), got %d", len(*sent))
	}
	if (*sent)[0].Subject != "Last note from us" {
		t.Errorf("unexpected subject: %s", (*sent)[0].Subject)
	}
}

func TestNotYetDue(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r, sent := newTestRunner(t, db)

	// User just signed up 5 minutes ago.
	createTrialUser(t, ctx, db, "frank@test.com", testNow.Add(-5*time.Minute))

	r.runOnce(ctx)

	if len(*sent) != 0 {
		t.Fatalf("expected 0 emails (too early), got %d", len(*sent))
	}
}

func TestUpgradedUserNotEmailed(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r, sent := newTestRunner(t, db)

	// User signed up 1.5 hours ago (only day0 is due, no retroactive issue).
	userID := createTrialUser(t, ctx, db, "upgraded@test.com", testNow.Add(-90*time.Minute))

	// First run: day0 sends.
	r.runOnce(ctx)
	if len(*sent) != 1 {
		t.Fatalf("expected 1 email, got %d", len(*sent))
	}

	// Simulate upgrade: close trial plan, open individual plan.
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		accountID := "acct_" + userID
		now := testNow
		if err := q.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{
			AccountID: accountID,
			EndedAt:   &now,
		}); err != nil {
			return err
		}
		if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: accountID,
			PlanID:    "individual:monthly:20260106",
			StartedAt: testNow,
			ChangedBy: strPtr("stripe:event"),
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Run again: no more emails — user excluded by query.
	r.runOnce(ctx)
	r.runOnce(ctx)
	if len(*sent) != 1 {
		t.Fatalf("expected still 1 email after upgrade, got %d", len(*sent))
	}
}

func TestRetroactiveUserGetsOnlyLatestStep(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r, sent := newTestRunner(t, db)

	// User signed up 4 days ago. Drip system is seeing them for the first time.
	// Steps day0 (1h), day1 (24h), day3 (72h) are all overdue.
	// Only day3 should be evaluated; day0 and day1 should be auto-skipped.
	userID := createTrialUser(t, ctx, db, "retro@test.com", testNow.Add(-96*time.Hour))

	// Create a box so day3 feature email fires (day3 only sends to active users).
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		_, err := q.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "host1",
			Name:            "retro-vm",
			Status:          "running",
			Image:           "ubuntu",
			CreatedByUserID: userID,
			Region:          "lax",
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	r.runOnce(ctx)

	// Should have sent exactly 1 email (day3 feature), not 3.
	if len(*sent) != 1 {
		t.Fatalf("expected 1 email on first contact, got %d", len(*sent))
	}

	// Check DB records: day0 and day1 should be skipped as retroactive.
	sends, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetDripSendsForUser, exedb.GetDripSendsForUserParams{
		UserID:   userID,
		Campaign: "trial_onboarding",
	})
	if err != nil {
		t.Fatal(err)
	}

	stepStatus := make(map[string]string, len(sends))
	stepReasons := make(map[string]string, len(sends))
	for _, s := range sends {
		stepStatus[s.Step] = s.Status
		if s.SkipReason != nil {
			stepReasons[s.Step] = *s.SkipReason
		}
	}

	if stepStatus[stepDay0Welcome] != statSkipped {
		t.Errorf("day0: expected skipped, got %s", stepStatus[stepDay0Welcome])
	}
	if stepReasons[stepDay0Welcome] != "retroactive: drip campaign started after this step was due" {
		t.Errorf("day0: unexpected reason: %s", stepReasons[stepDay0Welcome])
	}
	if stepStatus[stepDay1Nudge] != statSkipped {
		t.Errorf("day1: expected skipped, got %s", stepStatus[stepDay1Nudge])
	}
	// day3 should be sent (active user with VM).
	if stepStatus[stepDay3Feature] != statSent {
		t.Errorf("day3: expected sent, got %s", stepStatus[stepDay3Feature])
	}
}
