package llmgateway

import (
	"context"
	"math"
	"path/filepath"
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
	err := exedb.WithTx(db, context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		return q.InsertUser(ctx, exedb.InsertUserParams{
			UserID: userID,
			Email:  email,
			Region: "pdx",
		})
	})
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
}

func createBillingAccount(t *testing.T, db *sqlite.DB, userID, accountID string, now time.Time) {
	t.Helper()
	err := exedb.WithTx(db, context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		if err := q.InsertAccount(ctx, exedb.InsertAccountParams{
			ID:        accountID,
			CreatedBy: userID,
		}); err != nil {
			return err
		}
		return q.ActivateAccount(ctx, exedb.ActivateAccountParams{
			CreatedBy: userID,
			EventAt:   sqlite.NormalizeTime(now),
		})
	})
	if err != nil {
		t.Fatalf("failed to create billing account: %v", err)
	}
}

func floatClose(a, b, epsilon float64) bool {
	return math.Abs(a-b) < epsilon
}

func TestCreditManager_CheckAndRefreshCredit_DefaultMonthlyBucket(t *testing.T) {
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
	if !floatClose(info.Available, freeCreditPerUTCMonthUSD, 0.000001) {
		t.Fatalf("available = %f, want %f", info.Available, freeCreditPerUTCMonthUSD)
	}
	if !floatClose(info.Max, freeCreditPerUTCMonthUSD, 0.000001) {
		t.Fatalf("max = %f, want %f", info.Max, freeCreditPerUTCMonthUSD)
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
	if !floatClose(info.Available, freeCreditPerUTCMonthUSD, 0.000001) {
		t.Fatalf("initial available = %f, want %f", info.Available, freeCreditPerUTCMonthUSD)
	}

	firstDebit := 4.5
	info, err = mgr.DebitCredit(ctx, userID, firstDebit)
	if err != nil {
		t.Fatalf("first debit failed: %v", err)
	}
	if !floatClose(info.Available, freeCreditPerUTCMonthUSD-firstDebit, 0.000001) {
		t.Fatalf("available after first debit = %f, want %f", info.Available, freeCreditPerUTCMonthUSD-firstDebit)
	}

	secondDebit := 2.25
	info, err = mgr.DebitCredit(ctx, userID, secondDebit)
	if err != nil {
		t.Fatalf("second debit failed: %v", err)
	}
	wantAfterSecond := freeCreditPerUTCMonthUSD - firstDebit - secondDebit
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

	overageDebit := freeCreditPerUTCMonthUSD + 1
	info, err := mgr.DebitCredit(ctx, userID, overageDebit)
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
	if !floatClose(info.Available, -1, 0.000001) {
		t.Fatalf("available after same-month check = %f, want -1", info.Available)
	}
}

func TestCreditManager_MonthRolloverResetsDefaultBucket(t *testing.T) {
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

	_, err = mgr.DebitCredit(ctx, userID, freeCreditPerUTCMonthUSD+3)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	now = time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("month rollover check failed: %v", err)
	}
	if !floatClose(info.Available, freeCreditPerUTCMonthUSD, 0.000001) {
		t.Fatalf("available after month rollover = %f, want %f", info.Available, freeCreditPerUTCMonthUSD)
	}
	if !floatClose(info.Max, freeCreditPerUTCMonthUSD, 0.000001) {
		t.Fatalf("max after month rollover = %f, want %f", info.Max, freeCreditPerUTCMonthUSD)
	}
	if !floatClose(info.RefreshPerHour, 0, 0.000001) {
		t.Fatalf("refresh_per_hour after month rollover = %f, want 0", info.RefreshPerHour)
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

	_, err = mgr.DebitCredit(ctx, userID, 50)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != ErrInsufficientCredit {
		t.Fatalf("expected ErrInsufficientCredit after overage, got %v", err)
	}
	if !floatClose(info.Available, -10, 0.000001) {
		t.Fatalf("same-month post-overage available = %f, want -10", info.Available)
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
		if !floatClose(info.Available, freeCreditPerUTCMonthUSD, 0.000001) {
			t.Fatalf("available = %f, want %f", info.Available, freeCreditPerUTCMonthUSD)
		}
		if !floatClose(info.Max, freeCreditPerUTCMonthUSD, 0.000001) {
			t.Fatalf("max = %f, want %f", info.Max, freeCreditPerUTCMonthUSD)
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
		err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
			return q.SetUserBillingExemption(ctx, exedb.SetUserBillingExemptionParams{
				BillingExemption: new("free"),
				UserID:           userID,
			})
		})
		if err != nil {
			t.Fatalf("failed to set billing exemption: %v", err)
		}

		info, err := mgr.CheckAndRefreshCredit(ctx, userID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.Plan.Name != "friend" {
			t.Fatalf("plan = %q, want %q", info.Plan.Name, "friend")
		}
		if !floatClose(info.Available, freeCreditPerUTCMonthUSD, 0.000001) {
			t.Fatalf("available = %f, want %f", info.Available, freeCreditPerUTCMonthUSD)
		}
		if !floatClose(info.Max, freeCreditPerUTCMonthUSD, 0.000001) {
			t.Fatalf("max = %f, want %f", info.Max, freeCreditPerUTCMonthUSD)
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
		if !floatClose(info.Available, freeCreditPerUTCMonthUSD, 0.000001) {
			t.Fatalf("available = %f, want %f", info.Available, freeCreditPerUTCMonthUSD)
		}
		if !floatClose(info.Max, freeCreditPerUTCMonthUSD, 0.000001) {
			t.Fatalf("max = %f, want %f", info.Max, freeCreditPerUTCMonthUSD)
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
	_, err = mgr.DebitCredit(ctx, userID, 10)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}
	wantAvailable := info.Available - 10

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
	if !floatClose(info.Available, freeCreditPerUTCMonthUSD, 0.000001) {
		t.Fatalf("available = %f, want %f", info.Available, freeCreditPerUTCMonthUSD)
	}
	if info.Plan.Name != "has_billing" {
		t.Fatalf("plan = %q, want %q", info.Plan.Name, "has_billing")
	}
}
