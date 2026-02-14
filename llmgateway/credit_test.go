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
		t.Fatalf("Failed to copy template database: %v", err)
	}

	db, err := sqlite.New(dbPath, 1)
	if err != nil {
		t.Fatalf("Failed to create sqlite wrapper: %v", err)
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

// floatClose checks if two floats are close enough (within epsilon)
func floatClose(a, b, epsilon float64) bool {
	return math.Abs(a-b) < epsilon
}

const freeCreditPerMonthUSD = 20.0

func freeCreditPerHour(now time.Time) float64 {
	_ = now
	return freeCreditPerMonthUSD / (30.0 * 24.0)
}

func TestCreditManager_CheckAndRefreshCredit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	mgr := NewCreditManager(&DBGatewayData{db})
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr.now = func() time.Time { return now }
	ctx := context.Background()
	userID := "test-user-123"
	createTestUser(t, db, userID, "test123@example.com")

	// First check should create a default credit record
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected credit info, got nil")
	}
	wantHourly := freeCreditPerHour(now)
	if !floatClose(info.Available, wantHourly, 0.000001) {
		t.Errorf("expected hourly free credit %f, got %f", wantHourly, info.Available)
	}
	if info.Plan.Name != "no_billing" {
		t.Errorf("expected plan name %q, got %q", "no_billing", info.Plan.Name)
	}
}

func TestCreditManager_DebitCredit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	mgr := NewCreditManager(&DBGatewayData{db})
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr.now = func() time.Time { return now }
	ctx := context.Background()
	userID := "test-user-456"
	createTestUser(t, db, userID, "test456@example.com")

	// Create initial credit
	initial, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	wantHourly := freeCreditPerHour(now)
	if !floatClose(initial.Available, wantHourly, 0.000001) {
		t.Fatalf("expected initial hourly free credit %f, got %f", wantHourly, initial.Available)
	}

	// Debit one third of the hour's free allocation.
	debitOne := wantHourly / 3
	info, err := mgr.DebitCredit(ctx, userID, debitOne)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}
	wantAfterOne := wantHourly - debitOne
	if !floatClose(info.Available, wantAfterOne, 0.000001) {
		t.Errorf("expected %f after first debit, got %f", wantAfterOne, info.Available)
	}

	// Debit another quarter of the hour's free allocation.
	debitTwo := wantHourly / 4
	info, err = mgr.DebitCredit(ctx, userID, debitTwo)
	if err != nil {
		t.Fatalf("second debit failed: %v", err)
	}
	wantAfterTwo := wantAfterOne - debitTwo
	if !floatClose(info.Available, wantAfterTwo, 0.000001) {
		t.Errorf("expected %f after second debit, got %f", wantAfterTwo, info.Available)
	}

	// Verify total_used is tracked correctly
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
		t.Fatalf("failed to get credit: %v", err)
	}
	wantTotalUsed := debitOne + debitTwo
	if !floatClose(totalUsed, wantTotalUsed, 0.000001) {
		t.Errorf("expected total_used %f, got %f", wantTotalUsed, totalUsed)
	}
}

func TestCreditManager_InsufficientCredit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	mgr := NewCreditManager(&DBGatewayData{db})
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr.now = func() time.Time { return now }
	ctx := context.Background()
	userID := "test-user-789"
	createTestUser(t, db, userID, "test789@example.com")

	// Create initial credit
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	// Debit more than the current hourly free allocation.
	_, err = mgr.DebitCredit(ctx, userID, info.Available+0.01)
	if err != nil {
		t.Fatalf("debit all failed: %v", err)
	}

	// Now check should return ErrInsufficientCredit
	_, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != ErrInsufficientCredit {
		t.Errorf("expected ErrInsufficientCredit, got %v", err)
	}
}

func TestCreditManager_HourRollover(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := "test-hour-rollover-user"
	createTestUser(t, db, userID, "hour-rollover@example.com")

	now := time.Date(2025, 1, 15, 10, 15, 0, 0, time.UTC)
	mgr := &CreditManager{
		data: &DBGatewayData{db},
		now:  func() time.Time { return now },
	}

	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	wantHourly := freeCreditPerHour(now)
	if !floatClose(info.Available, wantHourly, 0.000001) {
		t.Fatalf("expected %f, got %f", wantHourly, info.Available)
	}

	debit := wantHourly / 2
	info, err = mgr.DebitCredit(ctx, userID, debit)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}
	wantHalfHour := wantHourly - debit
	if !floatClose(info.Available, wantHalfHour, 0.000001) {
		t.Fatalf("expected %f after debit, got %f", wantHalfHour, info.Available)
	}

	now = time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)
	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("rollover check failed: %v", err)
	}
	if !floatClose(info.Available, freeCreditPerHour(now), 0.000001) {
		t.Fatalf("expected full hourly credit after rollover, got %f", info.Available)
	}
}

func TestCreditManager_MonthRollover(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := "test-month-rollover-user"
	createTestUser(t, db, userID, "month-rollover@example.com")

	now := time.Date(2025, 1, 31, 23, 30, 0, 0, time.UTC)
	mgr := &CreditManager{
		data: &DBGatewayData{db},
		now:  func() time.Time { return now },
	}

	janHourly := freeCreditPerHour(now)
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	if !floatClose(info.Available, janHourly, 0.000001) {
		t.Fatalf("expected january hourly credit %f, got %f", janHourly, info.Available)
	}

	_, err = mgr.DebitCredit(ctx, userID, janHourly)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	now = time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	febHourly := freeCreditPerHour(now)

	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("month rollover check failed: %v", err)
	}
	if !floatClose(info.Available, febHourly, 0.000001) {
		t.Fatalf("expected february hourly credit %f after rollover, got %f", febHourly, info.Available)
	}
}

func TestCreditManager_NoCarryHourlyDepletion(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := "test-no-carry-user"
	createTestUser(t, db, userID, "nocarry@example.com")

	now := time.Date(2025, 1, 20, 9, 0, 0, 0, time.UTC)
	mgr := &CreditManager{
		data: &DBGatewayData{db},
		now:  func() time.Time { return now },
	}

	hourly := freeCreditPerHour(now)
	_, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("initial check failed: %v", err)
	}

	_, err = mgr.DebitCredit(ctx, userID, hourly*1.5)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	_, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != ErrInsufficientCredit {
		t.Fatalf("expected ErrInsufficientCredit in depleted hour, got %v", err)
	}

	now = now.Add(time.Hour)
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("next-hour check failed: %v", err)
	}
	if !floatClose(info.Available, freeCreditPerHour(now), 0.000001) {
		t.Fatalf("expected exactly one hour of free credit after rollover, got %f", info.Available)
	}
}

func TestCreditManager_TimestampRoundTrip(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := "test-timestamp-user"
	createTestUser(t, db, userID, "timestamp@example.com")

	// Create a fixed reference time for testing
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	mgr := &CreditManager{
		data: &DBGatewayData{db},
		now:  func() time.Time { return now },
	}

	// Create initial credit record with one hour of free credit.
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	wantHourly := freeCreditPerHour(now)
	if !floatClose(info.Available, wantHourly, 0.000001) {
		t.Fatalf("expected %f, got %f", wantHourly, info.Available)
	}

	// Debit half of the current hour allocation.
	debit := wantHourly / 2
	_, err = mgr.DebitCredit(ctx, userID, debit)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	// Advance time within the same hour; there should be no carry or extra refill.
	now = now.Add(30 * time.Minute)

	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("future check failed: %v", err)
	}

	wantSameHour := wantHourly - debit
	if !floatClose(info.Available, wantSameHour, 0.000001) {
		t.Errorf("expected %f after same-hour check, got %f", wantSameHour, info.Available)
	}

	// Verify that db-stored timestamp was correctly read back
	// by checking another call with the same timestamp is unchanged.
	info2, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("second check failed: %v", err)
	}
	if !floatClose(info2.Available, wantSameHour, 0.000001) {
		t.Errorf("expected %f on second check, got %f", wantSameHour, info2.Available)
	}
}

func TestPlanCategories(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	mgr := NewCreditManager(&DBGatewayData{db})
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr.now = func() time.Time { return now }
	wantHourly := freeCreditPerHour(now)

	// Test no_billing user (no billing, no exemptions)
	t.Run("no_billing", func(t *testing.T) {
		userID := "no-billing-user"
		createTestUser(t, db, userID, "nobilling@example.com")

		info, err := mgr.CheckAndRefreshCredit(ctx, userID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.Plan.Name != "no_billing" {
			t.Errorf("expected plan name %q, got %q", "no_billing", info.Plan.Name)
		}
		if !floatClose(info.Available, wantHourly, 0.000001) {
			t.Errorf("expected hourly free credit %f, got %f", wantHourly, info.Available)
		}
		if !floatClose(info.Max, wantHourly, 0.000001) {
			t.Errorf("expected max %f, got %f", wantHourly, info.Max)
		}
		if !floatClose(info.RefreshPerHour, wantHourly, 0.000001) {
			t.Errorf("expected refresh %f, got %f", wantHourly, info.RefreshPerHour)
		}
		// no_billing error message should include billing link
		if info.Plan.CreditExhaustedError != "LLM credits exhausted; credits refresh over time; for faster refresh, set up a subscription at https://exe.dev/billing/update" {
			t.Errorf("unexpected error message: %s", info.Plan.CreditExhaustedError)
		}
	})

	// Test friend user (billing_exemption = 'free')
	t.Run("friend", func(t *testing.T) {
		userID := "friend-user"
		createTestUser(t, db, userID, "friend@example.com")
		// Set billing exemption to 'free'
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
			t.Errorf("expected plan name %q, got %q", "friend", info.Plan.Name)
		}
		if !floatClose(info.Available, wantHourly, 0.000001) {
			t.Errorf("expected hourly free credit %f, got %f", wantHourly, info.Available)
		}
		if !floatClose(info.Max, wantHourly, 0.000001) {
			t.Errorf("expected max %f, got %f", wantHourly, info.Max)
		}
		if !floatClose(info.RefreshPerHour, wantHourly, 0.000001) {
			t.Errorf("expected refresh %f, got %f", wantHourly, info.RefreshPerHour)
		}
		// Friend user error message should NOT include billing link
		if info.Plan.CreditExhaustedError != "LLM credits exhausted; credits refresh over time" {
			t.Errorf("unexpected error message: %s", info.Plan.CreditExhaustedError)
		}
	})

	// Test has_billing user (active billing account)
	t.Run("has_billing", func(t *testing.T) {
		userID := "has-billing-user"
		createTestUser(t, db, userID, "hasbilling@example.com")
		// Create active billing account
		err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
			if err := q.InsertAccount(ctx, exedb.InsertAccountParams{
				ID:        "acct-has-billing",
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

		info, err := mgr.CheckAndRefreshCredit(ctx, userID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.Plan.Name != "has_billing" {
			t.Errorf("expected plan name %q, got %q", "has_billing", info.Plan.Name)
		}
		if !floatClose(info.Available, wantHourly, 0.000001) {
			t.Errorf("expected hourly free credit %f, got %f", wantHourly, info.Available)
		}
		if !floatClose(info.Max, wantHourly, 0.000001) {
			t.Errorf("expected max %f, got %f", wantHourly, info.Max)
		}
		if !floatClose(info.RefreshPerHour, wantHourly, 0.000001) {
			t.Errorf("expected refresh %f, got %f", wantHourly, info.RefreshPerHour)
		}
		// has_billing user error message should NOT include billing link
		if info.Plan.CreditExhaustedError != "LLM credits exhausted; credits refresh over time" {
			t.Errorf("unexpected error message: %s", info.Plan.CreditExhaustedError)
		}
	})

	// Test overrides on top of base plan.
	t.Run("overrides", func(t *testing.T) {
		userID := "override-user"
		createTestUser(t, db, userID, "override@example.com")
		// Set explicit credit overrides.
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
		// Plan name should still be no_billing (the base plan)
		if info.Plan.Name != "no_billing" {
			t.Errorf("expected plan name %q, got %q", "no_billing", info.Plan.Name)
		}
		if !floatClose(info.Available, 250.0, 0.01) {
			t.Errorf("expected available 250.0, got %f", info.Available)
		}
		if !floatClose(info.Max, 250.0, 0.01) {
			t.Errorf("expected max 250.0, got %f", info.Max)
		}
		if !floatClose(info.RefreshPerHour, 25.0, 0.01) {
			t.Errorf("expected refresh 25.0, got %f", info.RefreshPerHour)
		}
		// Error message should still be the no_billing message (with billing link)
		if info.Plan.CreditExhaustedError != "LLM credits exhausted; credits refresh over time; for faster refresh, set up a subscription at https://exe.dev/billing/update" {
			t.Errorf("unexpected error message: %s", info.Plan.CreditExhaustedError)
		}
	})
}

func TestCreditManager_OverrideRefillArbitraryDuration(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := "override-duration-user"
	createTestUser(t, db, userID, "override-duration@example.com")

	start := time.Date(2025, 1, 10, 8, 0, 0, 0, time.UTC)
	maxCredit := 6.0
	// 6 credits per 3 hours (2 credits/hour).
	refreshPerHour := 2.0

	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpsertUserLLMCredit(ctx, exedb.UpsertUserLLMCreditParams{
			UserID:          userID,
			AvailableCredit: 0.0,
			MaxCredit:       &maxCredit,
			RefreshPerHour:  &refreshPerHour,
			LastRefreshAt:   start,
		})
	})
	if err != nil {
		t.Fatalf("failed to set credit overrides: %v", err)
	}

	now := start.Add(90 * time.Minute)
	mgr := &CreditManager{
		data: &DBGatewayData{db},
		now:  func() time.Time { return now },
	}

	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	// 1.5 hours * 2 credits/hour = 3 credits.
	if !floatClose(info.Available, 3.0, 0.01) {
		t.Fatalf("expected available 3.0, got %f", info.Available)
	}
	if !floatClose(info.Max, 6.0, 0.01) {
		t.Fatalf("expected max 6.0, got %f", info.Max)
	}
	if !floatClose(info.RefreshPerHour, 2.0, 0.01) {
		t.Fatalf("expected refresh 2.0, got %f", info.RefreshPerHour)
	}
}

func TestCreditManager_TopUpOnBillingUpgrade(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	mgr := NewCreditManager(&DBGatewayData{db})
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr.now = func() time.Time { return now }

	// Create a no_billing user who has used some credit
	userID := "upgrade-user"
	createTestUser(t, db, userID, "upgrade@example.com")

	// Initialize their credit record (this happens on first LLM use)
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	wantHourly := freeCreditPerHour(now)
	if !floatClose(info.Available, wantHourly, 0.000001) {
		t.Fatalf("expected %f, got %f", wantHourly, info.Available)
	}

	// User uses half their current hourly free credit.
	_, err = mgr.DebitCredit(ctx, userID, wantHourly/2)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	// Verify remaining free credit before upgrade.
	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("check after debit failed: %v", err)
	}
	if info.Available <= 0 {
		t.Fatalf("expected positive remaining free credit, got %f", info.Available)
	}

	// Now user adds billing
	err = exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		if err := q.InsertAccount(ctx, exedb.InsertAccountParams{
			ID:        "acct-upgrade",
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

	// Call the top-up method
	err = mgr.TopUpOnBillingUpgrade(ctx, userID)
	if err != nil {
		t.Fatalf("top up failed: %v", err)
	}

	// Top-up is a no-op under hourly free allocation; balance remains unchanged.
	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("check after upgrade failed: %v", err)
	}
	if !floatClose(info.Available, wantHourly/2, 0.000001) {
		t.Errorf("expected %f after billing upgrade, got %f", wantHourly/2, info.Available)
	}
	if info.Plan.Name != "has_billing" {
		t.Errorf("expected plan name %q, got %q", "has_billing", info.Plan.Name)
	}
}

func TestCreditManager_TopUpOnBillingUpgrade_NoCreditRecord(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	mgr := NewCreditManager(&DBGatewayData{db})
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	mgr.now = func() time.Time { return now }

	// Create a user who has never used the LLM gateway
	userID := "fresh-upgrade-user"
	createTestUser(t, db, userID, "freshupgrade@example.com")

	// Activate billing without ever using LLM
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		if err := q.InsertAccount(ctx, exedb.InsertAccountParams{
			ID:        "acct-fresh-upgrade",
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

	// TopUp should be a no-op (no credit record exists)
	err = mgr.TopUpOnBillingUpgrade(ctx, userID)
	if err != nil {
		t.Fatalf("top up should not error for user without credit record: %v", err)
	}

	// When they eventually use the gateway, they should get the same hourly free bucket.
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	wantHourly := freeCreditPerHour(now)
	if !floatClose(info.Available, wantHourly, 0.000001) {
		t.Errorf("expected %f for new has_billing user, got %f", wantHourly, info.Available)
	}
	if info.Plan.Name != "has_billing" {
		t.Errorf("expected plan name %q, got %q", "has_billing", info.Plan.Name)
	}
}
