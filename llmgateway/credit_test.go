package llmgateway

import (
	"context"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/sqlite"
	"exe.dev/tslog"
)

func setupTestDB(t *testing.T) *sqlite.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "credit_test.db")
	if err := exedb.CopyTemplateDB(tslog.Slogger(t), dbPath); err != nil {
		t.Fatalf("failed to copy template database: %v", err)
	}

	db, err := sqlite.New(dbPath, 1)
	if err != nil {
		t.Fatalf("failed to create sqlite wrapper: %v", err)
	}
	return db
}

func createTestUser(t *testing.T, db *sqlite.DB, userID, email string) {
	t.Helper()
	now := time.Now().UTC()
	err := exedb.WithTx(db, context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		if err := q.InsertUser(ctx, exedb.InsertUserParams{
			UserID: userID,
			Email:  email,
			Region: "pdx",
		}); err != nil {
			return err
		}
		// Mirror production: every user gets an account and an active
		// account_plans row (default Basic).
		acctID := "acct-" + userID
		if err := q.InsertAccount(ctx, exedb.InsertAccountParams{
			ID:        acctID,
			CreatedBy: userID,
		}); err != nil {
			return err
		}
		changedBy := "test:createTestUser"
		return q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: acctID,
			PlanID:    "basic:monthly:20260106",
			StartedAt: now,
			ChangedBy: &changedBy,
		})
	})
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
}

func createTestBox(t *testing.T, db *sqlite.DB, name, userID string) int {
	t.Helper()
	var boxID int64
	err := exedb.WithTx(db, context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		var err error
		boxID, err = q.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-ctr",
			Name:            name,
			Status:          "running",
			Image:           "base",
			CreatedByUserID: userID,
			Region:          "pdx",
		})
		return err
	})
	if err != nil {
		t.Fatalf("failed to create test box: %v", err)
	}
	return int(boxID)
}

// createBillingAccount activates billing for an existing test user (which
// already has an account from createTestUser) and upgrades them to the
// Individual plan, mirroring a real Stripe checkout completion.
func createBillingAccount(t *testing.T, db *sqlite.DB, userID, _ string, now time.Time) {
	t.Helper()
	err := exedb.WithTx(db, context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		if err := q.ActivateAccount(ctx, exedb.ActivateAccountParams{
			CreatedBy: userID,
			EventAt:   now,
		}); err != nil {
			return err
		}
		acct, err := q.GetAccountByUserID(ctx, userID)
		if err != nil {
			return err
		}
		return q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
			AccountID: acct.ID,
			PlanID:    "individual:small:monthly:20260106",
			At:        now,
			ChangedBy: "test:createBillingAccount",
		})
	})
	if err != nil {
		t.Fatalf("failed to create billing account: %v", err)
	}
}

// setUserPlan replaces the active account_plan row for an existing test user.
func setUserPlan(t *testing.T, db *sqlite.DB, userID, planID string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		acct, err := q.GetAccountByUserID(ctx, userID)
		if err != nil {
			return err
		}
		return q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
			AccountID: acct.ID,
			PlanID:    planID,
			At:        now,
			ChangedBy: "test:setUserPlan",
		})
	})
	if err != nil {
		t.Fatalf("failed to set user plan %q: %v", planID, err)
	}
}

func floatClose(a, b, epsilon float64) bool {
	return math.Abs(a-b) < epsilon
}

func TestCreditManager_CheckAndRefreshCredit_DefaultOneTimeCredit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr := NewCreditManager(&DBGatewayData{db})
	mgr.now = func() time.Time { return now }

	ctx := context.Background()
	userID := "test-user-default-bucket"
	createTestUser(t, db, userID, "default-bucket@example.com")

	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected credit info, got nil")
	}
	if !floatClose(info.Available, initialFreeCreditNoSubscriptionUSD, 0.000001) {
		t.Fatalf("available = %f, want %f", info.Available, initialFreeCreditNoSubscriptionUSD)
	}
	if !floatClose(info.Max, initialFreeCreditNoSubscriptionUSD, 0.000001) {
		t.Fatalf("max = %f, want %f", info.Max, initialFreeCreditNoSubscriptionUSD)
	}
	if !floatClose(info.RefreshPerHour, 0, 0.000001) {
		t.Fatalf("refresh_per_hour = %f, want 0", info.RefreshPerHour)
	}
	if info.Plan.Name != "no_billing" {
		t.Fatalf("plan = %q, want %q", info.Plan.Name, "no_billing")
	}
}

func TestCreditManager_DebitCredit_FreeOnly(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr := NewCreditManager(&DBGatewayData{db})
	mgr.now = func() time.Time { return now }

	ctx := context.Background()
	userID := "test-user-free-only"
	createTestUser(t, db, userID, "free-only@example.com")

	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if !floatClose(info.Available, initialFreeCreditNoSubscriptionUSD, 0.000001) {
		t.Fatalf("initial available = %f, want %f", info.Available, initialFreeCreditNoSubscriptionUSD)
	}

	firstDebit := 4.5
	info, err = mgr.DebitCredit(ctx, userID, firstDebit, nil)
	if err != nil {
		t.Fatalf("first debit failed: %v", err)
	}
	if !floatClose(info.Available, initialFreeCreditNoSubscriptionUSD-firstDebit, 0.000001) {
		t.Fatalf("available after first debit = %f, want %f", info.Available, initialFreeCreditNoSubscriptionUSD-firstDebit)
	}

	secondDebit := 2.25
	info, err = mgr.DebitCredit(ctx, userID, secondDebit, nil)
	if err != nil {
		t.Fatalf("second debit failed: %v", err)
	}
	wantAfterSecond := initialFreeCreditNoSubscriptionUSD - firstDebit - secondDebit
	if !floatClose(info.Available, wantAfterSecond, 0.000001) {
		t.Fatalf("available after second debit = %f, want %f", info.Available, wantAfterSecond)
	}

	var totalUsed float64
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		q := exedb.New(tx.Conn())
		credit, err := q.GetUserLLMCredit(ctx, userID)
		if err != nil {
			return err
		}
		totalUsed = credit.TotalUsed
		return nil
	})
	if err != nil {
		t.Fatalf("failed to load user credit: %v", err)
	}
	if !floatClose(totalUsed, firstDebit+secondDebit, 0.000001) {
		t.Fatalf("total_used = %f, want %f", totalUsed, firstDebit+secondDebit)
	}
}

func TestCreditManager_NoIntraMonthRefill(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := "test-user-no-intra-month-refill"
	createTestUser(t, db, userID, "no-refill@example.com")

	now := time.Date(2025, 1, 20, 9, 0, 0, 0, time.UTC)
	mgr := &CreditManager{
		data: &DBGatewayData{db},
		now:  func() time.Time { return now },
	}

	_, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("initial check failed: %v", err)
	}

	overageDebit := initialFreeCreditNoSubscriptionUSD + 1
	info, err := mgr.DebitCredit(ctx, userID, overageDebit, nil)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}
	if !floatClose(info.Available, -1, 0.000001) {
		t.Fatalf("available after overage debit = %f, want -1", info.Available)
	}

	now = now.Add(6 * time.Hour)
	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != ErrInsufficientCredit {
		t.Fatalf("expected ErrInsufficientCredit, got %v", err)
	}
	if !floatClose(info.Available, 0, 0.000001) {
		t.Fatalf("available after same-month check = %f, want 0 (floored)", info.Available)
	}
}

func TestCreditManager_NoNextMonthRefillForNoSubscription(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := "test-user-month-rollover-default"
	createTestUser(t, db, userID, "month-rollover-default@example.com")

	now := time.Date(2025, 1, 31, 23, 30, 0, 0, time.UTC)
	mgr := &CreditManager{
		data: &DBGatewayData{db},
		now:  func() time.Time { return now },
	}

	_, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("initial check failed: %v", err)
	}

	_, err = mgr.DebitCredit(ctx, userID, initialFreeCreditNoSubscriptionUSD+3, nil)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	now = time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != ErrInsufficientCredit {
		t.Fatalf("month rollover check error = %v, want %v", err, ErrInsufficientCredit)
	}
	if !floatClose(info.Available, 0, 0.000001) {
		t.Fatalf("available after month rollover = %f, want 0 (floored)", info.Available)
	}
	if !floatClose(info.Max, initialFreeCreditNoSubscriptionUSD, 0.000001) {
		t.Fatalf("max after month rollover = %f, want %f", info.Max, initialFreeCreditNoSubscriptionUSD)
	}
	if !floatClose(info.RefreshPerHour, 0, 0.000001) {
		t.Fatalf("refresh_per_hour after month rollover = %f, want 0", info.RefreshPerHour)
	}
}

func TestCreditManager_SubscribedMonthRolloverTopUpToFloor(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := "test-user-month-rollover-topup-floor"
	createTestUser(t, db, userID, "month-rollover-topup-floor@example.com")

	now := time.Date(2025, 1, 31, 23, 30, 0, 0, time.UTC)
	mgr := &CreditManager{
		data: &DBGatewayData{db},
		now:  func() time.Time { return now },
	}

	if _, err := mgr.CheckAndRefreshCredit(ctx, userID); err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	createBillingAccount(t, db, userID, "acct-month-rollover-topup-floor", now)
	if err := mgr.TopUpOnBillingUpgrade(ctx, userID); err != nil {
		t.Fatalf("top up on upgrade failed: %v", err)
	}
	if _, err := mgr.DebitCredit(ctx, userID, 115, nil); err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	now = time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("month rollover check failed: %v", err)
	}
	if !floatClose(info.Available, monthlyTopUpSubscribedUSD, 0.000001) {
		t.Fatalf("available after month rollover = %f, want %f", info.Available, monthlyTopUpSubscribedUSD)
	}
	if !floatClose(info.Max, monthlyTopUpSubscribedUSD, 0.000001) {
		t.Fatalf("max after month rollover = %f, want %f", info.Max, monthlyTopUpSubscribedUSD)
	}
	if info.Plan.Name != "has_billing" {
		t.Fatalf("plan = %q, want %q", info.Plan.Name, "has_billing")
	}
}

func TestCreditManager_SubscribedMonthRolloverResetsToFloor(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := "test-user-month-rollover-reset"
	createTestUser(t, db, userID, "month-rollover-reset@example.com")

	now := time.Date(2025, 1, 31, 23, 30, 0, 0, time.UTC)
	mgr := &CreditManager{
		data: &DBGatewayData{db},
		now:  func() time.Time { return now },
	}

	if _, err := mgr.CheckAndRefreshCredit(ctx, userID); err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	createBillingAccount(t, db, userID, "acct-month-rollover-reset", now)
	if err := mgr.TopUpOnBillingUpgrade(ctx, userID); err != nil {
		t.Fatalf("top up on upgrade failed: %v", err)
	}
	if _, err := mgr.DebitCredit(ctx, userID, 30, nil); err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	now = time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("month rollover check failed: %v", err)
	}
	if !floatClose(info.Available, 20, 0.000001) {
		t.Fatalf("available after month rollover = %f, want 20 (unconditional reset)", info.Available)
	}
	if !floatClose(info.Max, monthlyTopUpSubscribedUSD, 0.000001) {
		t.Fatalf("max after month rollover = %f, want %f", info.Max, monthlyTopUpSubscribedUSD)
	}
	if info.Plan.Name != "has_billing" {
		t.Fatalf("plan = %q, want %q", info.Plan.Name, "has_billing")
	}
}

func TestCreditManager_SubscribedMonthRolloverNegativeBalance(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := "test-user-month-rollover-negative"
	createTestUser(t, db, userID, "month-rollover-negative@example.com")

	now := time.Date(2025, 1, 31, 23, 30, 0, 0, time.UTC)
	mgr := &CreditManager{
		data: &DBGatewayData{db},
		now:  func() time.Time { return now },
	}

	if _, err := mgr.CheckAndRefreshCredit(ctx, userID); err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	createBillingAccount(t, db, userID, "acct-month-rollover-negative", now)
	if err := mgr.TopUpOnBillingUpgrade(ctx, userID); err != nil {
		t.Fatalf("top up on upgrade failed: %v", err)
	}
	// Debit more than available to go negative (will error on insufficient credit)
	// So instead we'll debit all and then manually set to negative using DB
	if _, err := mgr.DebitCredit(ctx, userID, 120, nil); err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	// Manually set balance to negative via database
	now = now.Add(1 * time.Second)
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpdateUserLLMAvailableCredit(ctx, exedb.UpdateUserLLMAvailableCreditParams{
			UserID:          userID,
			AvailableCredit: -5.0,
			LastRefreshAt:   now,
		})
	})
	if err != nil {
		t.Fatalf("update credit failed: %v", err)
	}

	// Verify negative balance (will return ErrInsufficientCredit but still give us info)
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != ErrInsufficientCredit {
		t.Fatalf("check before rollover: got error %v, want ErrInsufficientCredit", err)
	}
	if info.Available >= 0 {
		t.Fatalf("available before rollover = %f, want negative", info.Available)
	}

	// Cross month boundary
	now = time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("month rollover check failed: %v", err)
	}
	if !floatClose(info.Available, 20, 0.000001) {
		t.Fatalf("available after month rollover = %f, want 20 (unconditional reset)", info.Available)
	}
}

func TestCreditManager_SubscribedMonthRolloverExactFloorBalance(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := "test-user-month-rollover-exact"
	createTestUser(t, db, userID, "month-rollover-exact@example.com")

	now := time.Date(2025, 1, 31, 23, 30, 0, 0, time.UTC)
	mgr := &CreditManager{
		data: &DBGatewayData{db},
		now:  func() time.Time { return now },
	}

	if _, err := mgr.CheckAndRefreshCredit(ctx, userID); err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	createBillingAccount(t, db, userID, "acct-month-rollover-exact", now)
	if err := mgr.TopUpOnBillingUpgrade(ctx, userID); err != nil {
		t.Fatalf("top up on upgrade failed: %v", err)
	}
	// User now has $120, debit $100 to leave exactly $20
	if _, err := mgr.DebitCredit(ctx, userID, 100, nil); err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	// Verify exactly $20 balance
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("check before rollover failed: %v", err)
	}
	if !floatClose(info.Available, 20, 0.000001) {
		t.Fatalf("available before rollover = %f, want 20", info.Available)
	}

	// Cross month boundary
	now = time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("month rollover check failed: %v", err)
	}
	if !floatClose(info.Available, 20, 0.000001) {
		t.Fatalf("available after month rollover = %f, want 20 (unconditional reset)", info.Available)
	}
}

func TestCreditManager_SubscribedMonthRolloverZeroBalance(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := "test-user-month-rollover-zero"
	createTestUser(t, db, userID, "month-rollover-zero@example.com")

	now := time.Date(2025, 1, 31, 23, 30, 0, 0, time.UTC)
	mgr := &CreditManager{
		data: &DBGatewayData{db},
		now:  func() time.Time { return now },
	}

	if _, err := mgr.CheckAndRefreshCredit(ctx, userID); err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	createBillingAccount(t, db, userID, "acct-month-rollover-zero", now)
	if err := mgr.TopUpOnBillingUpgrade(ctx, userID); err != nil {
		t.Fatalf("top up on upgrade failed: %v", err)
	}
	// User now has $120, debit all to exactly 0
	if _, err := mgr.DebitCredit(ctx, userID, 120, nil); err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	// Verify zero balance (will return ErrInsufficientCredit but still give us info)
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != ErrInsufficientCredit {
		t.Fatalf("check before rollover: got error %v, want ErrInsufficientCredit", err)
	}
	if !floatClose(info.Available, 0, 0.000001) {
		t.Fatalf("available before rollover = %f, want 0", info.Available)
	}

	// Cross month boundary
	now = time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("month rollover check failed: %v", err)
	}
	if !floatClose(info.Available, 20, 0.000001) {
		t.Fatalf("available after month rollover = %f, want 20 (unconditional reset)", info.Available)
	}
}

func TestCreditManager_MonthRolloverResetsOverrideBucket(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := "test-user-month-rollover-override"
	createTestUser(t, db, userID, "month-rollover-override@example.com")

	start := time.Date(2025, 1, 10, 8, 0, 0, 0, time.UTC)
	maxCredit := 250.0
	refreshPerHour := 25.0
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpsertUserLLMCredit(ctx, exedb.UpsertUserLLMCreditParams{
			UserID:          userID,
			AvailableCredit: 40.0,
			MaxCredit:       &maxCredit,
			RefreshPerHour:  &refreshPerHour,
			LastRefreshAt:   start,
		})
	})
	if err != nil {
		t.Fatalf("failed to set override credit: %v", err)
	}

	now := time.Date(2025, 1, 20, 10, 0, 0, 0, time.UTC)
	mgr := &CreditManager{
		data: &DBGatewayData{db},
		now:  func() time.Time { return now },
	}

	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("same-month check failed: %v", err)
	}
	if !floatClose(info.Available, 40, 0.000001) {
		t.Fatalf("same-month available = %f, want 40", info.Available)
	}
	if !floatClose(info.Max, maxCredit, 0.000001) {
		t.Fatalf("same-month max = %f, want %f", info.Max, maxCredit)
	}
	if !floatClose(info.RefreshPerHour, refreshPerHour, 0.000001) {
		t.Fatalf("same-month refresh_per_hour = %f, want %f", info.RefreshPerHour, refreshPerHour)
	}

	_, err = mgr.DebitCredit(ctx, userID, 50, nil)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != ErrInsufficientCredit {
		t.Fatalf("expected ErrInsufficientCredit after overage, got %v", err)
	}
	if !floatClose(info.Available, 0, 0.000001) {
		t.Fatalf("same-month post-overage available = %f, want 0 (floored)", info.Available)
	}

	now = time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("month rollover check failed: %v", err)
	}
	if !floatClose(info.Available, maxCredit, 0.000001) {
		t.Fatalf("available after month rollover = %f, want %f", info.Available, maxCredit)
	}
	if !floatClose(info.Max, maxCredit, 0.000001) {
		t.Fatalf("max after month rollover = %f, want %f", info.Max, maxCredit)
	}
	if !floatClose(info.RefreshPerHour, refreshPerHour, 0.000001) {
		t.Fatalf("refresh_per_hour after month rollover = %f, want %f", info.RefreshPerHour, refreshPerHour)
	}
}

func TestPlanCategories(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr := NewCreditManager(&DBGatewayData{db})
	mgr.now = func() time.Time { return now }

	t.Run("no_billing", func(t *testing.T) {
		userID := "no-billing-user"
		createTestUser(t, db, userID, "nobilling@example.com")

		info, err := mgr.CheckAndRefreshCredit(ctx, userID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.Plan.Name != "no_billing" {
			t.Fatalf("plan = %q, want %q", info.Plan.Name, "no_billing")
		}
		if !floatClose(info.Available, initialFreeCreditNoSubscriptionUSD, 0.000001) {
			t.Fatalf("available = %f, want %f", info.Available, initialFreeCreditNoSubscriptionUSD)
		}
		if !floatClose(info.Max, initialFreeCreditNoSubscriptionUSD, 0.000001) {
			t.Fatalf("max = %f, want %f", info.Max, initialFreeCreditNoSubscriptionUSD)
		}
		if !floatClose(info.RefreshPerHour, 0, 0.000001) {
			t.Fatalf("refresh_per_hour = %f, want 0", info.RefreshPerHour)
		}
		if info.Plan.CreditExhaustedError != "LLM credits exhausted; credits refresh over time; purchase more at https://exe.dev/user" {
			t.Fatalf("unexpected exhausted error: %s", info.Plan.CreditExhaustedError)
		}
	})

	t.Run("friend", func(t *testing.T) {
		userID := "friend-user"
		createTestUser(t, db, userID, "friend@example.com")
		setUserPlan(t, db, userID, "friend", now)

		info, err := mgr.CheckAndRefreshCredit(ctx, userID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.Plan.Name != "friend" {
			t.Fatalf("plan = %q, want %q", info.Plan.Name, "friend")
		}
		if !floatClose(info.Available, initialFreeCreditNoSubscriptionUSD, 0.000001) {
			t.Fatalf("available = %f, want %f", info.Available, initialFreeCreditNoSubscriptionUSD)
		}
		if !floatClose(info.Max, initialFreeCreditNoSubscriptionUSD, 0.000001) {
			t.Fatalf("max = %f, want %f", info.Max, initialFreeCreditNoSubscriptionUSD)
		}
		if !floatClose(info.RefreshPerHour, 0, 0.000001) {
			t.Fatalf("refresh_per_hour = %f, want 0", info.RefreshPerHour)
		}
	})

	t.Run("has_billing", func(t *testing.T) {
		userID := "has-billing-user"
		createTestUser(t, db, userID, "hasbilling@example.com")
		createBillingAccount(t, db, userID, "acct-has-billing", now)

		info, err := mgr.CheckAndRefreshCredit(ctx, userID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.Plan.Name != "has_billing" {
			t.Fatalf("plan = %q, want %q", info.Plan.Name, "has_billing")
		}
		if !floatClose(info.Available, initialFreeCreditNoSubscriptionUSD+UpgradeBonusCreditUSD, 0.000001) {
			t.Fatalf("available = %f, want %f", info.Available, initialFreeCreditNoSubscriptionUSD+UpgradeBonusCreditUSD)
		}
		if !floatClose(info.Max, monthlyTopUpSubscribedUSD, 0.000001) {
			t.Fatalf("max = %f, want %f", info.Max, monthlyTopUpSubscribedUSD)
		}
		if !floatClose(info.RefreshPerHour, 0, 0.000001) {
			t.Fatalf("refresh_per_hour = %f, want 0", info.RefreshPerHour)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		userID := "override-user"
		createTestUser(t, db, userID, "override@example.com")

		maxCredit := 250.0
		refreshRate := 25.0
		err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
			return q.UpsertUserLLMCredit(ctx, exedb.UpsertUserLLMCreditParams{
				UserID:          userID,
				AvailableCredit: 250.0,
				MaxCredit:       &maxCredit,
				RefreshPerHour:  &refreshRate,
				LastRefreshAt:   now,
			})
		})
		if err != nil {
			t.Fatalf("failed to set credit overrides: %v", err)
		}

		info, err := mgr.CheckAndRefreshCredit(ctx, userID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.Plan.Name != "no_billing" {
			t.Fatalf("plan = %q, want %q", info.Plan.Name, "no_billing")
		}
		if !floatClose(info.Available, 250.0, 0.000001) {
			t.Fatalf("available = %f, want 250", info.Available)
		}
		if !floatClose(info.Max, 250.0, 0.000001) {
			t.Fatalf("max = %f, want 250", info.Max)
		}
		if !floatClose(info.RefreshPerHour, 25.0, 0.000001) {
			t.Fatalf("refresh_per_hour = %f, want 25", info.RefreshPerHour)
		}
	})
}

// TestCreditRefreshDrivenByEntitlement verifies that the monthly credit refresh
// is gated by the credits:refresh plan entitlement, not by the legacy
// "MonthlyLLMCreditUSD >= 100" heuristic. Subscribers (Individual, Team,
// Business) refresh monthly. Friend, Trial, and Basic do not.
func TestCreditRefreshDrivenByEntitlement(t *testing.T) {
	cases := []struct {
		name        string
		planID      string
		wantRefresh bool
	}{
		{"individual", "individual:small:monthly:20260106", true},
		{"team", "team:monthly:20260106", true},
		{"business", "business:monthly:20260106", true},
		{"friend", "friend", false},
		{"trial", "trial:monthly:20260106", false},
		{"basic", "basic:monthly:20260106", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			defer db.Close()

			ctx := context.Background()
			userID := "user-" + tc.name
			createTestUser(t, db, userID, tc.name+"@example.com")

			now := time.Date(2025, 1, 31, 23, 30, 0, 0, time.UTC)
			mgr := &CreditManager{
				data: &DBGatewayData{db},
				now:  func() time.Time { return now },
			}
			setUserPlan(t, db, userID, tc.planID, now)

			// Initialize credit row, then drain to zero.
			info, err := mgr.CheckAndRefreshCredit(ctx, userID)
			if err != nil {
				t.Fatalf("initial check: %v", err)
			}
			if info.Available > 0 {
				if _, err := mgr.DebitCredit(ctx, userID, info.Available, nil); err != nil {
					t.Fatalf("debit drain: %v", err)
				}
			}

			// Cross a UTC month boundary.
			now = time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
			info, err = mgr.CheckAndRefreshCredit(ctx, userID)
			if tc.wantRefresh {
				if err != nil {
					t.Fatalf("after rollover with refresh entitlement: %v", err)
				}
				if !floatClose(info.Available, monthlyTopUpSubscribedUSD, 0.000001) {
					t.Fatalf("available after rollover = %f, want %f", info.Available, monthlyTopUpSubscribedUSD)
				}
			} else {
				if err != ErrInsufficientCredit {
					t.Fatalf("after rollover without refresh entitlement: err = %v, want ErrInsufficientCredit", err)
				}
				if !floatClose(info.Available, 0, 0.000001) {
					t.Fatalf("available after rollover = %f, want 0", info.Available)
				}
			}
		})
	}
}

func TestCreditManager_TopUpOnBillingUpgrade(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr := NewCreditManager(&DBGatewayData{db})
	mgr.now = func() time.Time { return now }

	userID := "upgrade-user"
	createTestUser(t, db, userID, "upgrade@example.com")

	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	if !floatClose(info.Available, initialFreeCreditNoSubscriptionUSD, 0.000001) {
		t.Fatalf("initial available = %f, want %f", info.Available, initialFreeCreditNoSubscriptionUSD)
	}

	if _, err := mgr.DebitCredit(ctx, userID, 10, nil); err != nil {
		t.Fatalf("debit failed: %v", err)
	}
	wantAvailable := info.Available - 10 + UpgradeBonusCreditUSD

	createBillingAccount(t, db, userID, "acct-upgrade", now)
	if err := mgr.TopUpOnBillingUpgrade(ctx, userID); err != nil {
		t.Fatalf("top up failed: %v", err)
	}

	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("check after upgrade failed: %v", err)
	}
	if !floatClose(info.Available, wantAvailable, 0.000001) {
		t.Fatalf("available after top up = %f, want %f", info.Available, wantAvailable)
	}
	if info.Plan.Name != "has_billing" {
		t.Fatalf("plan = %q, want %q", info.Plan.Name, "has_billing")
	}

	if err := mgr.TopUpOnBillingUpgrade(ctx, userID); err != nil {
		t.Fatalf("second top up failed: %v", err)
	}
	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("check after second top up failed: %v", err)
	}
	if !floatClose(info.Available, wantAvailable, 0.000001) {
		t.Fatalf("available after second top up = %f, want %f", info.Available, wantAvailable)
	}

	var credit exedb.UserLlmCredit
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		q := exedb.New(tx.Conn())
		var err error
		credit, err = q.GetUserLLMCredit(ctx, userID)
		return err
	})
	if err != nil {
		t.Fatalf("failed to load user credit: %v", err)
	}
	if credit.BillingUpgradeBonusGranted != 1 {
		t.Fatalf("billing_upgrade_bonus_granted = %d, want 1", credit.BillingUpgradeBonusGranted)
	}
}

func TestCreditManager_TopUpOnBillingUpgrade_NoCreditRecord(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr := NewCreditManager(&DBGatewayData{db})
	mgr.now = func() time.Time { return now }

	userID := "fresh-upgrade-user"
	createTestUser(t, db, userID, "freshupgrade@example.com")
	createBillingAccount(t, db, userID, "acct-fresh-upgrade", now)

	if err := mgr.TopUpOnBillingUpgrade(ctx, userID); err != nil {
		t.Fatalf("top up should not error for user without credit record: %v", err)
	}

	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if !floatClose(info.Available, initialFreeCreditNoSubscriptionUSD+UpgradeBonusCreditUSD, 0.000001) {
		t.Fatalf("available = %f, want %f", info.Available, initialFreeCreditNoSubscriptionUSD+UpgradeBonusCreditUSD)
	}
	if info.Plan.Name != "has_billing" {
		t.Fatalf("plan = %q, want %q", info.Plan.Name, "has_billing")
	}

	var credit exedb.UserLlmCredit
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		q := exedb.New(tx.Conn())
		var err error
		credit, err = q.GetUserLLMCredit(ctx, userID)
		return err
	})
	if err != nil {
		t.Fatalf("failed to load user credit: %v", err)
	}
	if credit.BillingUpgradeBonusGranted != 1 {
		t.Fatalf("billing_upgrade_bonus_granted = %d, want 1", credit.BillingUpgradeBonusGranted)
	}
}

func TestCreditManager_TopUpOnBillingUpgrade_ConcurrentIdempotent(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr := NewCreditManager(&DBGatewayData{db})
	mgr.now = func() time.Time { return now }

	userID := "upgrade-concurrent-user"
	createTestUser(t, db, userID, "upgrade-concurrent@example.com")
	if _, err := mgr.CheckAndRefreshCredit(ctx, userID); err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	createBillingAccount(t, db, userID, "acct-upgrade-concurrent", now)

	const workers = 24
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			errs <- mgr.TopUpOnBillingUpgrade(ctx, userID)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent top up failed: %v", err)
		}
	}

	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if !floatClose(info.Available, initialFreeCreditNoSubscriptionUSD+UpgradeBonusCreditUSD, 0.000001) {
		t.Fatalf("available = %f, want %f", info.Available, initialFreeCreditNoSubscriptionUSD+UpgradeBonusCreditUSD)
	}

	var credit exedb.UserLlmCredit
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		q := exedb.New(tx.Conn())
		var err error
		credit, err = q.GetUserLLMCredit(ctx, userID)
		return err
	})
	if err != nil {
		t.Fatalf("failed to load user credit: %v", err)
	}
	if credit.BillingUpgradeBonusGranted != 1 {
		t.Fatalf("billing_upgrade_bonus_granted = %d, want 1", credit.BillingUpgradeBonusGranted)
	}
}

func TestCreditManager_DebitCredit_FloorsAtZeroInDB(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr := NewCreditManager(&DBGatewayData{db})
	mgr.now = func() time.Time { return now }

	ctx := context.Background()
	userID := "test-user-floor-zero"
	createTestUser(t, db, userID, "floor-zero@example.com")

	if _, err := mgr.CheckAndRefreshCredit(ctx, userID); err != nil {
		t.Fatalf("initial check failed: %v", err)
	}

	// Debit more than available: $20 + $5 overage
	info, err := mgr.DebitCredit(ctx, userID, initialFreeCreditNoSubscriptionUSD+5, nil)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	// Returned value is unfloored so overage math works.
	if !floatClose(info.Available, -5, 0.000001) {
		t.Fatalf("returned available = %f, want -5", info.Available)
	}

	// But the DB value is floored to 0.
	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != ErrInsufficientCredit {
		t.Fatalf("expected ErrInsufficientCredit, got %v", err)
	}
	if !floatClose(info.Available, 0, 0.000001) {
		t.Fatalf("DB available = %f, want 0 (floored)", info.Available)
	}
}

func TestCreditManager_DebitCredit_RepeatedOverageStaysAtZero(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr := NewCreditManager(&DBGatewayData{db})
	mgr.now = func() time.Time { return now }

	ctx := context.Background()
	userID := "test-user-repeated-overage"
	createTestUser(t, db, userID, "repeated-overage@example.com")

	if _, err := mgr.CheckAndRefreshCredit(ctx, userID); err != nil {
		t.Fatalf("initial check failed: %v", err)
	}

	// Exhaust all free credit.
	if _, err := mgr.DebitCredit(ctx, userID, initialFreeCreditNoSubscriptionUSD, nil); err != nil {
		t.Fatalf("exhaust debit failed: %v", err)
	}

	// Now debit repeatedly when already at 0. DB should stay at 0,
	// never accumulating negative balance.
	for i := range 5 {
		info, err := mgr.DebitCredit(ctx, userID, 10, nil)
		if err != nil {
			t.Fatalf("debit %d failed: %v", i, err)
		}
		// Returned value is -10 each time (unfloored, for overage billing).
		if !floatClose(info.Available, -10, 0.000001) {
			t.Fatalf("debit %d: returned available = %f, want -10", i, info.Available)
		}

		// DB always reads 0.
		dbInfo, err := mgr.CheckAndRefreshCredit(ctx, userID)
		if err != ErrInsufficientCredit {
			t.Fatalf("debit %d: expected ErrInsufficientCredit, got %v", i, err)
		}
		if !floatClose(dbInfo.Available, 0, 0.000001) {
			t.Fatalf("debit %d: DB available = %f, want 0 (floored)", i, dbInfo.Available)
		}
	}
}

func TestCreditManager_DebitCredit_TotalUsedStillAccurate(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr := NewCreditManager(&DBGatewayData{db})
	mgr.now = func() time.Time { return now }

	ctx := context.Background()
	userID := "test-user-total-used-accurate"
	createTestUser(t, db, userID, "total-used@example.com")

	if _, err := mgr.CheckAndRefreshCredit(ctx, userID); err != nil {
		t.Fatalf("initial check failed: %v", err)
	}

	// Debit $25 (exceeds $20 free credit by $5).
	if _, err := mgr.DebitCredit(ctx, userID, 25, nil); err != nil {
		t.Fatalf("debit failed: %v", err)
	}
	// Debit another $10 while at 0.
	if _, err := mgr.DebitCredit(ctx, userID, 10, nil); err != nil {
		t.Fatalf("second debit failed: %v", err)
	}

	// total_used should reflect both debits accurately.
	var totalUsed float64
	err := db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		q := exedb.New(tx.Conn())
		credit, err := q.GetUserLLMCredit(ctx, userID)
		if err != nil {
			return err
		}
		totalUsed = credit.TotalUsed
		return nil
	})
	if err != nil {
		t.Fatalf("failed to load user credit: %v", err)
	}
	if !floatClose(totalUsed, 35, 0.000001) {
		t.Fatalf("total_used = %f, want 35", totalUsed)
	}
}

func TestCreditManager_DebitCredit_RecordsBoxUsage(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr := NewCreditManager(&DBGatewayData{db})
	mgr.now = func() time.Time { return now }

	ctx := context.Background()
	userID := "test-user-box-usage"
	createTestUser(t, db, userID, "box-usage@example.com")
	boxID := createTestBox(t, db, "usage-box", userID)

	if _, err := mgr.CheckAndRefreshCredit(ctx, userID); err != nil {
		t.Fatalf("initial check failed: %v", err)
	}

	// First debit with box usage.
	info, err := mgr.DebitCredit(ctx, userID, 1.50, &BoxUsage{
		BoxID:          boxID,
		Provider:       "anthropic",
		Model:          "claude-sonnet-4-20250514",
		CostMicrocents: 1_500_000,
	})
	if err != nil {
		t.Fatalf("first debit failed: %v", err)
	}
	if !floatClose(info.Available, initialFreeCreditNoSubscriptionUSD-1.50, 0.000001) {
		t.Fatalf("available after first debit = %f, want %f", info.Available, initialFreeCreditNoSubscriptionUSD-1.50)
	}

	// Second debit, same model — should aggregate into the same hourly bucket.
	_, err = mgr.DebitCredit(ctx, userID, 0.75, &BoxUsage{
		BoxID:          boxID,
		Provider:       "anthropic",
		Model:          "claude-sonnet-4-20250514",
		CostMicrocents: 750_000,
	})
	if err != nil {
		t.Fatalf("second debit failed: %v", err)
	}

	// Third debit, different model — should create a separate row.
	_, err = mgr.DebitCredit(ctx, userID, 0.25, &BoxUsage{
		BoxID:          boxID,
		Provider:       "openai",
		Model:          "gpt-4o",
		CostMicrocents: 250_000,
	})
	if err != nil {
		t.Fatalf("third debit failed: %v", err)
	}

	// Query usage summary. Use a wide time range since RecordBoxLLMUsage
	// buckets by CURRENT_TIMESTAMP (real wall clock).
	farPast := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	farFuture := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	var summary []exedb.GetBoxLLMUsageSummaryRow
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		q := exedb.New(tx.Conn())
		var err error
		summary, err = q.GetBoxLLMUsageSummary(ctx, exedb.GetBoxLLMUsageSummaryParams{
			BoxID:        boxID,
			HourBucket:   farPast,
			HourBucket_2: farFuture,
		})
		return err
	})
	if err != nil {
		t.Fatalf("failed to get usage summary: %v", err)
	}

	if len(summary) != 2 {
		t.Fatalf("expected 2 summary rows, got %d", len(summary))
	}

	// Rows are ordered by total_cost_microcents DESC.
	if summary[0].Model != "claude-sonnet-4-20250514" || summary[0].Provider != "anthropic" {
		t.Fatalf("row 0: model=%q provider=%q, want claude-sonnet-4-20250514/anthropic", summary[0].Model, summary[0].Provider)
	}
	if summary[0].TotalCostMicrocents != 2_250_000 {
		t.Fatalf("row 0: total_cost_microcents=%d, want 2250000", summary[0].TotalCostMicrocents)
	}
	if summary[0].TotalRequestCount != 2 {
		t.Fatalf("row 0: total_request_count=%d, want 2", summary[0].TotalRequestCount)
	}

	if summary[1].Model != "gpt-4o" || summary[1].Provider != "openai" {
		t.Fatalf("row 1: model=%q provider=%q, want gpt-4o/openai", summary[1].Model, summary[1].Provider)
	}
	if summary[1].TotalCostMicrocents != 250_000 {
		t.Fatalf("row 1: total_cost_microcents=%d, want 250000", summary[1].TotalCostMicrocents)
	}
	if summary[1].TotalRequestCount != 1 {
		t.Fatalf("row 1: total_request_count=%d, want 1", summary[1].TotalRequestCount)
	}

	// Verify that passing nil BoxUsage doesn't record anything extra.
	_, err = mgr.DebitCredit(ctx, userID, 0.10, nil)
	if err != nil {
		t.Fatalf("nil-usage debit failed: %v", err)
	}

	// Re-query: should still be exactly 2 rows, unchanged.
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		q := exedb.New(tx.Conn())
		var err error
		summary, err = q.GetBoxLLMUsageSummary(ctx, exedb.GetBoxLLMUsageSummaryParams{
			BoxID:        boxID,
			HourBucket:   farPast,
			HourBucket_2: farFuture,
		})
		return err
	})
	if err != nil {
		t.Fatalf("failed to get usage summary after nil debit: %v", err)
	}
	if len(summary) != 2 {
		t.Fatalf("expected 2 summary rows after nil debit, got %d", len(summary))
	}
}

func TestCreditManager_DebitCredit_TransferKeepsUsageOnOriginalOwnerRow(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	defer db.Close()

	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr := NewCreditManager(&DBGatewayData{db})
	mgr.now = func() time.Time { return now }

	ctx := context.Background()
	owner1 := "test-user-box-owner-1"
	owner2 := "test-user-box-owner-2"
	createTestUser(t, db, owner1, "box-owner-1@example.com")
	createTestUser(t, db, owner2, "box-owner-2@example.com")
	boxID := createTestBox(t, db, "transfer-box", owner1)

	if _, err := mgr.CheckAndRefreshCredit(ctx, owner1); err != nil {
		t.Fatalf("initial check owner1 failed: %v", err)
	}
	if _, err := mgr.CheckAndRefreshCredit(ctx, owner2); err != nil {
		t.Fatalf("initial check owner2 failed: %v", err)
	}

	if _, err := mgr.DebitCredit(ctx, owner1, 1.00, &BoxUsage{
		BoxID:          boxID,
		Provider:       "anthropic",
		Model:          "claude-sonnet-4-20250514",
		CostMicrocents: 1_000_000,
	}); err != nil {
		t.Fatalf("first debit failed: %v", err)
	}

	err := db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		q := exedb.New(tx.Conn())
		return q.UpdateBoxOwner(ctx, exedb.UpdateBoxOwnerParams{
			CreatedByUserID: owner2,
			ID:              boxID,
		})
	})
	if err != nil {
		t.Fatalf("transfer box: %v", err)
	}

	if _, err := mgr.DebitCredit(ctx, owner2, 0.50, &BoxUsage{
		BoxID:          boxID,
		Provider:       "anthropic",
		Model:          "claude-sonnet-4-20250514",
		CostMicrocents: 500_000,
	}); err != nil {
		t.Fatalf("second debit failed: %v", err)
	}

	farPast := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	farFuture := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	var owner1Rows []exedb.GetUserLLMUsageDailyRow
	var owner2Rows []exedb.GetUserLLMUsageDailyRow
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		q := exedb.New(tx.Conn())
		var err error
		owner1Rows, err = q.GetUserLLMUsageDaily(ctx, exedb.GetUserLLMUsageDailyParams{
			UserID:       owner1,
			HourBucket:   farPast,
			HourBucket_2: farFuture,
		})
		if err != nil {
			return err
		}
		owner2Rows, err = q.GetUserLLMUsageDaily(ctx, exedb.GetUserLLMUsageDailyParams{
			UserID:       owner2,
			HourBucket:   farPast,
			HourBucket_2: farFuture,
		})
		return err
	})
	if err != nil {
		t.Fatalf("query daily usage: %v", err)
	}

	if len(owner1Rows) != 1 {
		t.Fatalf("owner1 rows = %d, want 1", len(owner1Rows))
	}
	if owner1Rows[0].CostMicrocents != 1_500_000 {
		t.Fatalf("owner1 cost_microcents = %d, want 1500000", owner1Rows[0].CostMicrocents)
	}
	if owner1Rows[0].RequestCount != 2 {
		t.Fatalf("owner1 request_count = %d, want 2", owner1Rows[0].RequestCount)
	}
	if len(owner2Rows) != 0 {
		t.Fatalf("owner2 rows = %d, want 0", len(owner2Rows))
	}
}
