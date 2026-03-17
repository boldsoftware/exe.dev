package execore

import (
	"context"
	"path/filepath"
	"testing"

	"exe.dev/billing"
	"exe.dev/billing/tender"
	"exe.dev/exedb"
	exesqlite "exe.dev/sqlite"
	"exe.dev/tslog"
)

func newGiftTestDB(t *testing.T) *exesqlite.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "gift_test.db")
	if err := exedb.CopyTemplateDB(tslog.Slogger(t), dbPath); err != nil {
		t.Fatalf("failed to copy template database: %v", err)
	}
	db, err := exesqlite.New(dbPath, 1)
	if err != nil {
		t.Fatalf("failed to create sqlite wrapper: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newGiftTestManager(t *testing.T) *billing.Manager {
	t.Helper()
	return &billing.Manager{
		DB:     newGiftTestDB(t),
		Logger: tslog.Slogger(t),
	}
}

// createTestBillingAccount inserts a billing account and user into the DB for testing.
func createTestBillingAccount(t *testing.T, db *exesqlite.DB, accountID, userID string) {
	t.Helper()
	err := db.Tx(context.Background(), func(ctx context.Context, tx *exesqlite.Tx) error {
		// Create user first (accounts has FK to users)
		_, err := tx.Exec(
			`INSERT OR IGNORE INTO users (user_id, email) VALUES (?, ?)`,
			userID, userID+"@test.com",
		)
		if err != nil {
			return err
		}
		_, err = tx.Exec(
			`INSERT INTO accounts (id, created_by) VALUES (?, ?)`,
			accountID, userID,
		)
		return err
	})
	if err != nil {
		t.Fatalf("createTestBillingAccount: %v", err)
	}
}

func TestListGifts_NoGifts(t *testing.T) {
	m := newGiftTestManager(t)

	gifts, err := m.ListGifts(t.Context(), "acct_test1")
	if err != nil {
		t.Fatalf("ListGifts: %v", err)
	}
	if len(gifts) != 0 {
		t.Fatalf("expected 0 gifts, got %d", len(gifts))
	}
}

func TestListGifts_WithGifts(t *testing.T) {
	m := newGiftTestManager(t)

	// Insert two gifts
	if err := m.GiftCredits(t.Context(), "acct_test2", &billing.GiftCreditsParams{
		Amount: tender.Mint(1000, 0),
		GiftID: "gift1",
		Note:   "First gift",
	}); err != nil {
		t.Fatalf("GiftCredits: %v", err)
	}
	if err := m.GiftCredits(t.Context(), "acct_test2", &billing.GiftCreditsParams{
		Amount: tender.Mint(500, 0),
		GiftID: "gift2",
		Note:   "Second gift",
	}); err != nil {
		t.Fatalf("GiftCredits: %v", err)
	}

	gifts, err := m.ListGifts(t.Context(), "acct_test2")
	if err != nil {
		t.Fatalf("ListGifts: %v", err)
	}
	if len(gifts) != 2 {
		t.Fatalf("expected 2 gifts, got %d", len(gifts))
	}
	// Gifts are returned most recent first
	if gifts[0].GiftID != "gift2" {
		t.Errorf("expected first gift to be gift2, got %s", gifts[0].GiftID)
	}
	if gifts[0].Note != "Second gift" {
		t.Errorf("expected note 'Second gift', got %q", gifts[0].Note)
	}
	if gifts[1].GiftID != "gift1" {
		t.Errorf("expected second gift to be gift1, got %s", gifts[1].GiftID)
	}
}

func TestGiftCredits_Success(t *testing.T) {
	m := newGiftTestManager(t)

	err := m.GiftCredits(t.Context(), "acct_gift", &billing.GiftCreditsParams{
		Amount: tender.Mint(2500, 0),
		GiftID: "ssh_gift:acct_gift:123",
		Note:   "Support gift",
	})
	if err != nil {
		t.Fatalf("GiftCredits: %v", err)
	}

	// Verify the credit state reflects the gift
	state, err := m.GetCreditState(t.Context(), "acct_gift")
	if err != nil {
		t.Fatalf("GetCreditState: %v", err)
	}
	if state.Gift.Microcents() != tender.Mint(2500, 0).Microcents() {
		t.Errorf("expected gift %d microcents, got %d", tender.Mint(2500, 0).Microcents(), state.Gift.Microcents())
	}
	if state.Total.Microcents() != tender.Mint(2500, 0).Microcents() {
		t.Errorf("expected total %d microcents, got %d", tender.Mint(2500, 0).Microcents(), state.Total.Microcents())
	}
}

func TestGiftCredits_DefaultNote(t *testing.T) {
	m := newGiftTestManager(t)

	err := m.GiftCredits(t.Context(), "acct_defnote", &billing.GiftCreditsParams{
		Amount: tender.Mint(100, 0),
		GiftID: "ssh_gift:acct_defnote:456",
		// Note intentionally empty -- GiftCredits should use default note
	})
	if err != nil {
		t.Fatalf("GiftCredits: %v", err)
	}

	gifts, err := m.ListGifts(t.Context(), "acct_defnote")
	if err != nil {
		t.Fatalf("ListGifts: %v", err)
	}
	if len(gifts) != 1 {
		t.Fatalf("expected 1 gift, got %d", len(gifts))
	}
	// When note is empty, GiftCredits fills in a default
	if gifts[0].Note != "Credit gift from support@exe.dev" {
		t.Errorf("expected default note, got %q", gifts[0].Note)
	}
}

func TestGiftCredits_ZeroAmount(t *testing.T) {
	m := newGiftTestManager(t)

	err := m.GiftCredits(t.Context(), "acct_zero", &billing.GiftCreditsParams{
		Amount: tender.Zero(),
		GiftID: "ssh_gift:acct_zero:789",
		Note:   "Zero gift",
	})
	if err == nil {
		t.Fatal("expected error for zero amount gift, got nil")
	}
}

func TestGiftCredits_NegativeAmount(t *testing.T) {
	m := newGiftTestManager(t)

	err := m.GiftCredits(t.Context(), "acct_neg", &billing.GiftCreditsParams{
		Amount: tender.Mint(-100, 0),
		GiftID: "ssh_gift:acct_neg:neg",
		Note:   "Negative gift",
	})
	if err == nil {
		t.Fatal("expected error for negative amount gift, got nil")
	}
}

func TestGiftCredits_MissingGiftID(t *testing.T) {
	m := newGiftTestManager(t)

	err := m.GiftCredits(t.Context(), "acct_noid", &billing.GiftCreditsParams{
		Amount: tender.Mint(100, 0),
		// GiftID intentionally empty
	})
	if err == nil {
		t.Fatal("expected error for missing gift_id, got nil")
	}
}

func TestGetCreditState_Empty(t *testing.T) {
	m := newGiftTestManager(t)

	state, err := m.GetCreditState(t.Context(), "acct_empty")
	if err != nil {
		t.Fatalf("GetCreditState: %v", err)
	}
	if state.Paid.Microcents() != 0 {
		t.Errorf("expected paid 0, got %d", state.Paid.Microcents())
	}
	if state.Gift.Microcents() != 0 {
		t.Errorf("expected gift 0, got %d", state.Gift.Microcents())
	}
	if state.Used.Microcents() != 0 {
		t.Errorf("expected used 0, got %d", state.Used.Microcents())
	}
	if state.Total.Microcents() != 0 {
		t.Errorf("expected total 0, got %d", state.Total.Microcents())
	}
}

func TestGetCreditState_MixedCredits(t *testing.T) {
	m := newGiftTestManager(t)

	// Add a gift
	if err := m.GiftCredits(t.Context(), "acct_mixed", &billing.GiftCreditsParams{
		Amount: tender.Mint(1000, 0),
		GiftID: "gift_mixed_1",
		Note:   "Gift",
	}); err != nil {
		t.Fatalf("GiftCredits: %v", err)
	}

	// Add paid credits (via sync credit ledger - simulates Stripe purchase)
	err := m.DB.Tx(t.Context(), func(ctx context.Context, tx *exesqlite.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO billing_credits (account_id, amount, stripe_event_id) VALUES (?, ?, ?)`,
			"acct_mixed", tender.Mint(500, 0).Microcents(), "evt_paid_1",
		)
		return err
	})
	if err != nil {
		t.Fatalf("insert paid credit: %v", err)
	}

	// Spend some credits
	_, err = m.SpendCredits(t.Context(), "acct_mixed", 1, tender.Mint(200, 0))
	if err != nil {
		t.Fatalf("SpendCredits: %v", err)
	}

	state, err := m.GetCreditState(t.Context(), "acct_mixed")
	if err != nil {
		t.Fatalf("GetCreditState: %v", err)
	}

	// Paid = 500 cents = 5_000_000 microcents
	if state.Paid.Microcents() != tender.Mint(500, 0).Microcents() {
		t.Errorf("expected paid %d, got %d", tender.Mint(500, 0).Microcents(), state.Paid.Microcents())
	}
	// Gift = 1000 cents = 10_000_000 microcents
	if state.Gift.Microcents() != tender.Mint(1000, 0).Microcents() {
		t.Errorf("expected gift %d, got %d", tender.Mint(1000, 0).Microcents(), state.Gift.Microcents())
	}
	// Used = 200 cents = 2_000_000 microcents
	if state.Used.Microcents() != tender.Mint(200, 0).Microcents() {
		t.Errorf("expected used %d, got %d", tender.Mint(200, 0).Microcents(), state.Used.Microcents())
	}
	// Total = paid + gift - used = 500 + 1000 - 200 = 1300 cents
	expectedTotal := tender.Mint(500, 0).Microcents() + tender.Mint(1000, 0).Microcents() - tender.Mint(200, 0).Microcents()
	if state.Total.Microcents() != expectedTotal {
		t.Errorf("expected total %d, got %d", expectedTotal, state.Total.Microcents())
	}
}
