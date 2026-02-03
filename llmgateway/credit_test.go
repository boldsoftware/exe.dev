package llmgateway

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/sqlite"
	"exe.dev/tslog"
	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sqlite.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "credit_test.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	if err := exedb.RunMigrations(tslog.Slogger(t), rawDB); err != nil {
		rawDB.Close()
		t.Fatalf("Failed to run migrations: %v", err)
	}
	rawDB.Close()

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

func TestCreditManager_CheckAndRefreshCredit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	mgr := NewCreditManager(db)
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
	// Default for unpaid user is $50
	if !floatClose(info.Available, 50.0, 0.01) {
		t.Errorf("expected ~50.0, got %f", info.Available)
	}
	if !floatClose(info.Max, 50.0, 0.01) {
		t.Errorf("expected max ~50.0, got %f", info.Max)
	}
	if !floatClose(info.RefreshPerHour, 1.0, 0.01) {
		t.Errorf("expected refresh ~1.0/hr, got %f", info.RefreshPerHour)
	}
	if info.Plan.Name != "no_billing" {
		t.Errorf("expected plan name %q, got %q", "no_billing", info.Plan.Name)
	}
}

func TestCreditManager_DebitCredit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	mgr := NewCreditManager(db)
	ctx := context.Background()
	userID := "test-user-456"
	createTestUser(t, db, userID, "test456@example.com")

	// Create initial credit
	_, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	// Debit $5
	info, err := mgr.DebitCredit(ctx, userID, 5.0)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}
	if !floatClose(info.Available, 45.0, 0.1) {
		t.Errorf("expected ~45.0 after debit, got %f", info.Available)
	}

	// Debit another $10
	info, err = mgr.DebitCredit(ctx, userID, 10.0)
	if err != nil {
		t.Fatalf("second debit failed: %v", err)
	}
	if !floatClose(info.Available, 35.0, 0.1) {
		t.Errorf("expected ~35.0 after second debit, got %f", info.Available)
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
	// Total used should be 5 + 10 = 15
	if !floatClose(totalUsed, 15.0, 0.1) {
		t.Errorf("expected total_used ~15.0, got %f", totalUsed)
	}
}

func TestCreditManager_InsufficientCredit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	mgr := NewCreditManager(db)
	ctx := context.Background()
	userID := "test-user-789"
	createTestUser(t, db, userID, "test789@example.com")

	// Create initial credit
	_, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	// Debit more than available to force negative
	_, err = mgr.DebitCredit(ctx, userID, 150.0)
	if err != nil {
		t.Fatalf("debit all failed: %v", err)
	}

	// Now check should return ErrInsufficientCredit
	_, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != ErrInsufficientCredit {
		t.Errorf("expected ErrInsufficientCredit, got %v", err)
	}
}

func TestCalculateRefreshedCredit(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name           string
		available      float64
		max            float64
		refreshPerHour float64
		lastRefresh    time.Time
		wantAvailable  float64
	}{
		{
			name:           "no refresh needed at max",
			available:      100.0,
			max:            100.0,
			refreshPerHour: 50.0,
			lastRefresh:    now.Add(-time.Hour),
			wantAvailable:  100.0,
		},
		{
			name:           "partial refresh 30min",
			available:      50.0,
			max:            100.0,
			refreshPerHour: 50.0,
			lastRefresh:    now.Add(-30 * time.Minute),
			wantAvailable:  75.0, // 50 + (50 * 0.5)
		},
		{
			name:           "full hour refresh",
			available:      0.0,
			max:            100.0,
			refreshPerHour: 50.0,
			lastRefresh:    now.Add(-time.Hour),
			wantAvailable:  50.0,
		},
		{
			name:           "refresh capped at max",
			available:      80.0,
			max:            100.0,
			refreshPerHour: 50.0,
			lastRefresh:    now.Add(-2 * time.Hour),
			wantAvailable:  100.0, // capped at max
		},
		{
			name:           "negative credit with refresh",
			available:      -10.0, // user went into debt
			max:            100.0,
			refreshPerHour: 50.0,
			lastRefresh:    now.Add(-time.Hour),
			wantAvailable:  40.0, // -10 + 50
		},
		{
			name:           "no time elapsed",
			available:      50.0,
			max:            100.0,
			refreshPerHour: 50.0,
			lastRefresh:    now,
			wantAvailable:  50.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := CalculateRefreshedCredit(tt.available, tt.max, tt.refreshPerHour, tt.lastRefresh, now)
			if !floatClose(got, tt.wantAvailable, 0.01) {
				t.Errorf("CalculateRefreshedCredit() = %f, want %f", got, tt.wantAvailable)
			}
		})
	}
}

func TestCreditManager_TimestampRoundTrip(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	userID := "test-timestamp-user"
	createTestUser(t, db, userID, "timestamp@example.com")

	// Create a fixed reference time for testing
	refTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	mgr := &CreditManager{
		db:  db,
		now: func() time.Time { return refTime },
	}

	// Create initial credit record at refTime (no_billing plan: max=50, refresh=1/hr)
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	if !floatClose(info.Available, 50.0, 0.01) {
		t.Fatalf("expected 50.0, got %f", info.Available)
	}

	// Debit to set available to 10
	_, err = mgr.DebitCredit(ctx, userID, 40.0)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	// Now advance time by 30 minutes and verify refresh calculation
	futureTime := refTime.Add(30 * time.Minute)
	mgr.now = func() time.Time { return futureTime }

	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("future check failed: %v", err)
	}

	// 10 available + (1/hr * 0.5hr) = 10.5
	if !floatClose(info.Available, 10.5, 0.1) {
		t.Errorf("expected ~10.5 after 30min refresh, got %f", info.Available)
	}

	// Verify that db-stored timestamp was correctly read back
	// by checking another refresh would be minimal (same "now" time)
	info2, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("second check failed: %v", err)
	}
	if !floatClose(info2.Available, 10.5, 0.1) {
		t.Errorf("expected ~10.5 on second check (no time elapsed), got %f", info2.Available)
	}
}

func TestPlanCategories(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	mgr := NewCreditManager(db)

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
		if !floatClose(info.Max, 50.0, 0.01) {
			t.Errorf("expected max 50.0, got %f", info.Max)
		}
		if !floatClose(info.RefreshPerHour, 1.0, 0.01) {
			t.Errorf("expected refresh 1.0, got %f", info.RefreshPerHour)
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
				BillingExemption: strPtr("free"),
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
		if !floatClose(info.Max, 100.0, 0.01) {
			t.Errorf("expected max 100.0, got %f", info.Max)
		}
		if !floatClose(info.RefreshPerHour, 5.0, 0.01) {
			t.Errorf("expected refresh 5.0, got %f", info.RefreshPerHour)
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
				EventAt:   sqlite.FormatTime(time.Now()),
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
		if !floatClose(info.Max, 100.0, 0.01) {
			t.Errorf("expected max 100.0, got %f", info.Max)
		}
		if !floatClose(info.RefreshPerHour, 5.0, 0.01) {
			t.Errorf("expected refresh 5.0, got %f", info.RefreshPerHour)
		}
		// has_billing user error message should NOT include billing link
		if info.Plan.CreditExhaustedError != "LLM credits exhausted; credits refresh over time" {
			t.Errorf("unexpected error message: %s", info.Plan.CreditExhaustedError)
		}
	})

	// Test overrides on top of base plan
	t.Run("overrides", func(t *testing.T) {
		userID := "override-user"
		createTestUser(t, db, userID, "override@example.com")
		// Set explicit credit overrides - user has no billing, so base plan is no_billing
		maxCredit := 250.0
		refreshRate := 25.0
		err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
			return q.UpsertUserLLMCredit(ctx, exedb.UpsertUserLLMCreditParams{
				UserID:          userID,
				AvailableCredit: 250.0,
				MaxCredit:       &maxCredit,
				RefreshPerHour:  &refreshRate,
				LastRefreshAt:   time.Now(),
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
		// But limits should be the overridden values
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

func strPtr(s string) *string {
	return &s
}

func TestCreditManager_TopUpOnBillingUpgrade(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	mgr := NewCreditManager(db)

	// Create a no_billing user who has used some credit
	userID := "upgrade-user"
	createTestUser(t, db, userID, "upgrade@example.com")

	// Initialize their credit record (this happens on first LLM use)
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	if !floatClose(info.Available, 50.0, 0.01) {
		t.Fatalf("expected 50.0, got %f", info.Available)
	}

	// User uses some credit
	_, err = mgr.DebitCredit(ctx, userID, 30.0)
	if err != nil {
		t.Fatalf("debit failed: %v", err)
	}

	// Verify they're at $20
	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("check after debit failed: %v", err)
	}
	if !floatClose(info.Available, 20.0, 0.1) {
		t.Fatalf("expected ~20.0, got %f", info.Available)
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
			EventAt:   sqlite.FormatTime(time.Now()),
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

	// User should now be at $100 (has_billing max)
	info, err = mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("check after upgrade failed: %v", err)
	}
	if !floatClose(info.Available, 100.0, 0.01) {
		t.Errorf("expected 100.0 after billing upgrade, got %f", info.Available)
	}
	if info.Plan.Name != "has_billing" {
		t.Errorf("expected plan name %q, got %q", "has_billing", info.Plan.Name)
	}
}

func TestCreditManager_TopUpOnBillingUpgrade_NoCreditRecord(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()
	mgr := NewCreditManager(db)

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
			EventAt:   sqlite.FormatTime(time.Now()),
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

	// When they eventually use the gateway, they should get has_billing limits
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if !floatClose(info.Available, 100.0, 0.01) {
		t.Errorf("expected 100.0 for new has_billing user, got %f", info.Available)
	}
}
