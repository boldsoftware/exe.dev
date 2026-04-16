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
	return stage.Env{
		WebHost:   "exe.dev",
		FakeEmail: true,
	}
}

type sentEmail struct {
	To      string
	Subject string
	Body    string
}

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
	createTrialUser(t, ctx, db, "alice@test.com", time.Now().Add(-2*time.Hour))

	r.runOnce(ctx)

	if len(*sent) != 1 {
		t.Fatalf("expected 1 email sent, got %d", len(*sent))
	}
	if (*sent)[0].Subject != "Ready to create computers" {
		t.Errorf("unexpected subject: %s", (*sent)[0].Subject)
	}
}

func TestDay0Welcome_WithVM_Skipped(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r, sent := newTestRunner(t, db)

	userID := createTrialUser(t, ctx, db, "bob@test.com", time.Now().Add(-2*time.Hour))

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

	// User signed up 25 hours ago, no VM.
	createTrialUser(t, ctx, db, "charlie@test.com", time.Now().Add(-25*time.Hour))

	// First run: sends day0.
	r.runOnce(ctx)
	if len(*sent) != 1 {
		t.Fatalf("expected 1 email (day0), got %d", len(*sent))
	}

	// Second run: sends day1.
	r.runOnce(ctx)
	if len(*sent) != 2 {
		t.Fatalf("expected 2 emails (day0+day1), got %d", len(*sent))
	}
	if (*sent)[1].Subject != "You have 6 days left \u2014 start something" {
		t.Errorf("unexpected day1 subject: %s", (*sent)[1].Subject)
	}
}

func TestNoDoubleDelivery(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r, sent := newTestRunner(t, db)

	createTrialUser(t, ctx, db, "diana@test.com", time.Now().Add(-2*time.Hour))

	r.runOnce(ctx)
	r.runOnce(ctx)
	r.runOnce(ctx)

	if len(*sent) != 1 {
		t.Fatalf("expected exactly 1 email across 3 runs, got %d", len(*sent))
	}
}

func TestStepProgression(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r, sent := newTestRunner(t, db)

	// User signed up 15 days ago with a VM.
	userID := createTrialUser(t, ctx, db, "eve@test.com", time.Now().Add(-15*24*time.Hour))

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

	// Run 7 times to process all steps.
	for range 7 {
		r.runOnce(ctx)
	}

	// Verify we got through all steps.
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

	// Build a map of step -> status for easy checking.
	stepStatus := make(map[string]string, len(sends))
	for _, s := range sends {
		stepStatus[s.Step] = s.Status
	}

	// day0: skipped (has VM)
	if stepStatus[stepDay0Welcome] != statSkipped {
		t.Errorf("day0: expected skipped, got %s", stepStatus[stepDay0Welcome])
	}
	// day1: skipped (has VM)
	if stepStatus[stepDay1Nudge] != statSkipped {
		t.Errorf("day1: expected skipped, got %s", stepStatus[stepDay1Nudge])
	}
	// day3: sent (feature email for active users)
	if stepStatus[stepDay3Feature] != statSent {
		t.Errorf("day3: expected sent, got %s", stepStatus[stepDay3Feature])
	}
	// day5: sent
	if stepStatus[stepDay5Urgency] != statSent {
		t.Errorf("day5: expected sent, got %s", stepStatus[stepDay5Urgency])
	}
	// day7: sent
	if stepStatus[stepDay7Expiry] != statSent {
		t.Errorf("day7: expected sent, got %s", stepStatus[stepDay7Expiry])
	}
	// day10: sent (has VM)
	if stepStatus[stepDay10WinBack] != statSent {
		t.Errorf("day10: expected sent, got %s", stepStatus[stepDay10WinBack])
	}
	// day14: sent
	if stepStatus[stepDay14Final] != statSent {
		t.Errorf("day14: expected sent, got %s", stepStatus[stepDay14Final])
	}

	// Count actual sends (not skips).
	nSent := len(*sent)
	// day0: skip, day1: skip, day3: send, day5: send, day7: send, day10: send, day14: send = 5 sent
	if nSent != 5 {
		t.Errorf("expected 5 emails actually sent, got %d", nSent)
	}
}

func TestUTM(t *testing.T) {
	got := utm("https://exe.dev/idea", "day0_welcome")
	want := "https://exe.dev/idea?utm_source=drip&utm_medium=email&utm_campaign=trial_onboarding&utm_content=day0_welcome"
	if got != want {
		t.Errorf("utm mismatch:\n  got:  %s\n  want: %s", got, want)
	}

	// URL with existing query params.
	got2 := utm("https://exe.dev/idea?foo=bar", "day1_nudge")
	want2 := "https://exe.dev/idea?foo=bar&utm_source=drip&utm_medium=email&utm_campaign=trial_onboarding&utm_content=day1_nudge"
	if got2 != want2 {
		t.Errorf("utm mismatch with existing params:\n  got:  %s\n  want: %s", got2, want2)
	}
}

func TestNotYetDue(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r, sent := newTestRunner(t, db)

	// User just signed up 5 minutes ago.
	createTrialUser(t, ctx, db, "frank@test.com", time.Now().Add(-5*time.Minute))

	r.runOnce(ctx)

	if len(*sent) != 0 {
		t.Fatalf("expected 0 emails (too early), got %d", len(*sent))
	}
}

func TestUpgradedUserNotEmailed(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	r, sent := newTestRunner(t, db)

	// User signed up 3 days ago.
	userID := createTrialUser(t, ctx, db, "upgraded@test.com", time.Now().Add(-72*time.Hour))

	// First run: day0 sends.
	r.runOnce(ctx)
	if len(*sent) != 1 {
		t.Fatalf("expected 1 email, got %d", len(*sent))
	}

	// Simulate upgrade: close trial plan, open individual plan, add billing event.
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		accountID := "acct_" + userID
		now := time.Now()
		if err := q.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{
			AccountID: accountID,
			EndedAt:   &now,
		}); err != nil {
			return err
		}
		if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: accountID,
			PlanID:    "individual:monthly:20260106",
			StartedAt: time.Now(),
			ChangedBy: strPtr("stripe:event"),
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Run again: no more emails.
	r.runOnce(ctx)
	r.runOnce(ctx)
	if len(*sent) != 1 {
		t.Fatalf("expected still 1 email after upgrade, got %d", len(*sent))
	}
}
