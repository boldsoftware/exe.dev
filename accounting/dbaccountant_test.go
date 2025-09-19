package accounting

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/sqlite"
	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) (*sqlite.DB, func()) {
	// Create temp database file
	tmpFile, err := os.CreateTemp("", "accounting-test-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()
	dbPath := tmpFile.Name()

	// Run migrations
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}

	if err := exedb.RunMigrations(rawDB); err != nil {
		rawDB.Close()
		os.Remove(dbPath)
		t.Fatalf("Failed to run migrations: %v", err)
	}
	rawDB.Close()

	// Open with sqlite wrapper
	db, err := sqlite.New(dbPath, 1)
	if err != nil {
		os.Remove(dbPath)
		t.Fatalf("Failed to open sqlite database: %v", err)
	}

	cleanup := func() {
		db.Close()
		os.Remove(dbPath)
	}

	return db, cleanup
}

func TestDBAccountant_GetUserBalance(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	accountant := NewDBAccountant(db)
	ctx := context.Background()
	billingAccountID := "test-account-1"

	// Setup test billing account
	err := db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.InsertBillingAccount(ctx, exedb.InsertBillingAccountParams{
			BillingAccountID: billingAccountID,
			Name:             "Test Account",
			BillingEmail:     nil,
			StripeCustomerID: nil,
		})
	})
	if err != nil {
		t.Fatalf("Failed to create test billing account: %v", err)
	}

	// Initial balance should be 0
	balance, err := accountant.GetUserBalance(ctx, billingAccountID)
	if err != nil {
		t.Fatalf("Failed to get balance: %v", err)
	}
	if balance != 0 {
		t.Errorf("Expected initial balance to be 0, got %f", balance)
	}

	// Add a credit
	credit := UsageCredit{
		BillingAccountID: billingAccountID,
		Amount:           10.0,
		PaymentMethod:    "test",
		PaymentID:        "test-payment-1",
		Status:           "completed",
	}
	err = accountant.CreditUsage(ctx, credit)
	if err != nil {
		t.Fatalf("Failed to add credit: %v", err)
	}

	// Balance should now be 10.0
	balance, err = accountant.GetUserBalance(ctx, billingAccountID)
	if err != nil {
		t.Fatalf("Failed to get balance after credit: %v", err)
	}
	if balance != 10.0 {
		t.Errorf("Expected balance to be 10.0 after credit, got %f", balance)
	}

	// Add a debit
	debit := UsageDebit{
		Usage: Usage{
			InputTokens:  1000,
			OutputTokens: 500,
			CostUSD:      2.5,
		},
		Model:            "claude-3-sonnet",
		MessageID:        "test-message-1",
		BillingAccountID: billingAccountID,
		Created:          time.Now(),
	}
	err = accountant.DebitUsage(ctx, debit)
	if err != nil {
		t.Fatalf("Failed to add debit: %v", err)
	}

	// Balance should now be 7.5 (10.0 - 2.5)
	balance, err = accountant.GetUserBalance(ctx, billingAccountID)
	if err != nil {
		t.Fatalf("Failed to get balance after debit: %v", err)
	}
	if balance != 7.5 {
		t.Errorf("Expected balance to be 7.5 after debit, got %f", balance)
	}
}

func TestDBAccountant_DebitUsage_DuplicateMessageID(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	accountant := NewDBAccountant(db)
	ctx := context.Background()
	billingAccountID := "test-account-2"

	// Setup test billing account
	err := db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.InsertBillingAccount(ctx, exedb.InsertBillingAccountParams{
			BillingAccountID: billingAccountID,
			Name:             "Test Account 2",
		})
	})
	if err != nil {
		t.Fatalf("Failed to create test billing account: %v", err)
	}

	debit := UsageDebit{
		Usage: Usage{
			InputTokens:  1000,
			OutputTokens: 500,
			CostUSD:      2.5,
		},
		Model:            "claude-3-sonnet",
		MessageID:        "duplicate-message-id",
		BillingAccountID: billingAccountID,
		Created:          time.Now(),
	}

	// First debit should succeed
	err = accountant.DebitUsage(ctx, debit)
	if err != nil {
		t.Fatalf("Failed to add first debit: %v", err)
	}

	// Second debit with same message ID should be ignored (no error)
	err = accountant.DebitUsage(ctx, debit)
	if err != nil {
		t.Fatalf("Second debit with duplicate message ID should not error: %v", err)
	}

	// Balance should only reflect one debit
	balance, err := accountant.GetUserBalance(ctx, billingAccountID)
	if err != nil {
		t.Fatalf("Failed to get balance: %v", err)
	}
	if balance != -2.5 {
		t.Errorf("Expected balance to be -2.5 (only one debit), got %f", balance)
	}
}

func TestDBAccountant_HasNewUserCredits(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	accountant := NewDBAccountant(db)
	ctx := context.Background()
	billingAccountID := "test-account-3"

	// Setup test billing account
	err := db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.InsertBillingAccount(ctx, exedb.InsertBillingAccountParams{
			BillingAccountID: billingAccountID,
			Name:             "Test Account 3",
		})
	})
	if err != nil {
		t.Fatalf("Failed to create test billing account: %v", err)
	}

	// New user should be eligible for credits
	hasCredits, data := accountant.HasNewUserCredits(ctx, billingAccountID)
	if !hasCredits {
		t.Errorf("New user should be eligible for credits, got hasCredits=%v", hasCredits)
	}
	if data == nil {
		t.Error("Credit data should not be nil")
	}

	// Apply the credits
	accountant.ApplyNewUserCredits(ctx, billingAccountID)

	// User should no longer be eligible for credits
	hasCredits, _ = accountant.HasNewUserCredits(ctx, billingAccountID)
	if hasCredits {
		t.Error("User should not be eligible for credits after applying them")
	}
}

func TestDBAccountant_ApplyNewUserCredits(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	accountant := NewDBAccountant(db)
	ctx := context.Background()
	billingAccountID := "test-account-4"

	// Setup test billing account
	err := db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.InsertBillingAccount(ctx, exedb.InsertBillingAccountParams{
			BillingAccountID: billingAccountID,
			Name:             "Test Account 4",
		})
	})
	if err != nil {
		t.Fatalf("Failed to create test billing account: %v", err)
	}

	// Initial balance should be 0
	balance, err := accountant.GetUserBalance(ctx, billingAccountID)
	if err != nil {
		t.Fatalf("Failed to get initial balance: %v", err)
	}
	if balance != 0 {
		t.Errorf("Expected initial balance to be 0, got %f", balance)
	}

	// Apply new user credits
	accountant.ApplyNewUserCredits(ctx, billingAccountID)

	// Balance should now include the new user credit
	balance, err = accountant.GetUserBalance(ctx, billingAccountID)
	if err != nil {
		t.Fatalf("Failed to get balance after applying credits: %v", err)
	}
	if balance != 10.0 {
		t.Errorf("Expected balance to be 10.0 after applying new user credits, got %f", balance)
	}
}
