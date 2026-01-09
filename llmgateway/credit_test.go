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

	// First check should create a default credit record
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected credit info, got nil")
	}
	// Default is $100
	if !floatClose(info.Available, 100.0, 0.01) {
		t.Errorf("expected ~100.0, got %f", info.Available)
	}
	if !floatClose(info.Max, 100.0, 0.01) {
		t.Errorf("expected max ~100.0, got %f", info.Max)
	}
	if !floatClose(info.RefreshPerHour, 10.0, 0.01) {
		t.Errorf("expected refresh ~10.0/hr, got %f", info.RefreshPerHour)
	}
}

func TestCreditManager_DebitCredit(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	mgr := NewCreditManager(db)
	ctx := context.Background()
	userID := "test-user-456"

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
	if !floatClose(info.Available, 95.0, 0.1) {
		t.Errorf("expected ~95.0 after debit, got %f", info.Available)
	}

	// Debit another $10
	info, err = mgr.DebitCredit(ctx, userID, 10.0)
	if err != nil {
		t.Fatalf("second debit failed: %v", err)
	}
	if !floatClose(info.Available, 85.0, 0.1) {
		t.Errorf("expected ~85.0 after second debit, got %f", info.Available)
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

	// Create a fixed reference time for testing
	refTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	mgr := &CreditManager{
		db:  db,
		now: func() time.Time { return refTime },
	}

	// Create initial credit record at refTime
	info, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("initial check failed: %v", err)
	}
	if !floatClose(info.Available, 100.0, 0.01) {
		t.Fatalf("expected 100.0, got %f", info.Available)
	}

	// Debit to set available to 50
	_, err = mgr.DebitCredit(ctx, userID, 50.0)
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

	// 50 available + (10/hr * 0.5hr) = 55
	if !floatClose(info.Available, 55.0, 0.1) {
		t.Errorf("expected ~55.0 after 30min refresh, got %f", info.Available)
	}

	// Verify that db-stored timestamp was correctly read back
	// by checking another refresh would be minimal (same "now" time)
	info2, err := mgr.CheckAndRefreshCredit(ctx, userID)
	if err != nil {
		t.Fatalf("second check failed: %v", err)
	}
	if !floatClose(info2.Available, 55.0, 0.1) {
		t.Errorf("expected ~55.0 on second check (no time elapsed), got %f", info2.Available)
	}
}
