package billing

import (
	"errors"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"exe.dev/billing/stripetest"
	"exe.dev/billing/tender"
	"exe.dev/exedb"
	"exe.dev/tslog"
)

func TestSyncCredits(t *testing.T) {
	m := newTestManager(t)
	clock := m.startClock(t)

	check := func(got, want error) {
		t.Helper()
		if !errors.Is(got, want) {
			t.Fatalf("err = %v, want %v", got, want)
		}
	}

	checkBalance := func(aliceID string, want tender.Value) {
		t.Helper()

		got, err := m.CreditBalance(t.Context(), aliceID)
		check(err, nil)

		if got != want {
			t.Fatalf("balance = %d, want %d (%s)", got, want, got.Sub(want))
		}
	}

	aliceID := "exe_alice_" + clock.ID()

	_, err := m.BuyCredits(t.Context(), aliceID, &BuyCreditsParams{
		Amount:     tender.Mint(10, 0), // $10.00 in cents
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	})
	check(err, ErrNotFound)

	_, err = m.Subscribe(t.Context(), aliceID, &SubscribeParams{
		Email:      "alice@palace.com",
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	})
	check(err, nil)

	// "Click" the link and subscribe exe_alice to create the Stripe
	// customer and price lookup key if they don't exist.
	err = stripeSubscribe(t.Context(), m, aliceID, "pm_card_visa", "individual")
	check(err, nil)

	// BuyCredits should succeed and return a checkout link.
	gotURL, err := m.BuyCredits(t.Context(), aliceID, &BuyCreditsParams{
		Amount:     tender.Mint(1000, 0), // $10.00 in cents
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	})
	check(err, nil)
	if !strings.HasPrefix(gotURL, "https://checkout.stripe.com/") {
		t.Fatalf("unexpected URL prefix (want %q): %q", "https://checkout.stripe.com/", gotURL)
	}

	// Simulate Stripe sending the payment_intent.succeeded event for the credit purchase.
	err = stripeCompleteCreditPurchase(t.Context(), m, aliceID, "pm_card_visa", tender.Mint(100, 0))
	check(err, nil)

	// SyncCredits has not been called yet, so balance should still be 0.
	checkBalance(aliceID, tender.Zero())

	err = m.SyncCredits(t.Context(), aliceID, clock.Now())
	check(err, nil)

	// Check that the credits were added to the ledger.
	balance, err := m.CreditBalance(t.Context(), aliceID)
	check(err, nil)

	if balance != tender.Mint(100, 0) {
		t.Fatalf("unexpected credit balance: got %d, want %d", balance, 100)
	}
}

func TestUseCreditsPreservesFractionalCents(t *testing.T) {
	m := &Manager{
		DB: newTestDB(t),
	}

	ctx := t.Context()
	accountID := "exe_fractional_cents"
	createTestAccount(t, m.DB, accountID, "user_fractional_cents")

	if err := m.SpendCredits(ctx, accountID, 3, tender.Mint(0, 5000)); err != nil {
		t.Fatalf("UseCredits fractional: %v", err)
	}
	balance, err := m.CreditBalance(ctx, accountID)
	if err != nil {
		t.Fatalf("CreditBalance: %v", err)
	}
	if want := tender.Mint(-1, -5000); balance != want {
		t.Fatalf("fractional balance = %v, want %v", balance, want)
	}

	if err := m.SpendCredits(ctx, accountID, 2, tender.Mint(1, 0)); err != nil {
		t.Fatalf("UseCredits whole cents: %v", err)
	}
	balance, err = m.CreditBalance(ctx, accountID)
	if err != nil {
		t.Fatalf("CreditBalance: %v", err)
	}
	if want := tender.Mint(-3, -5000); balance != want {
		t.Fatalf("whole-cent balance = %v, want %v", balance, want)
	}
}

func TestSpendCreditsRejectsNegativeUnitPrice(t *testing.T) {
	m := &Manager{
		DB: newTestDB(t),
	}

	ctx := t.Context()
	accountID := "exe_negative_price"
	createTestAccount(t, m.DB, accountID, "user_negative_price")

	err := m.SpendCredits(ctx, accountID, 1, tender.Mint(0, -1))
	if err == nil {
		t.Fatal("SpendCredits with negative unitPrice error = nil, want non-nil")
	}
	if got := err.Error(); !strings.Contains(got, "unit price must be non-negative") {
		t.Fatalf("SpendCredits with negative unitPrice error = %q, want non-negative unit price error", got)
	}

	balance, err := m.CreditBalance(ctx, accountID)
	if err != nil {
		t.Fatalf("read balance after rejected spend: %v", err)
	}
	if balance != tender.Zero() {
		t.Fatalf("balance after rejected spend = %v, want %v", balance, tender.Zero())
	}
}

func TestGiftCredits(t *testing.T) {
	m := &Manager{DB: newTestDB(t)}
	ctx := t.Context()

	accountID := "exe_gift_test"
	createTestAccount(t, m.DB, accountID, "user_gift_test")

	err := m.GiftCredits(ctx, accountID, &GiftCreditsParams{
		AmountUSD:  50.0,
		GiftPrefix: GiftPrefixDebug,
		Note:       "Thanks for being awesome",
	})
	if err != nil {
		t.Fatalf("GiftCredits: %v", err)
	}

	// Verify the balance reflects the gift.
	balance, err := m.CreditBalance(ctx, accountID)
	if err != nil {
		t.Fatalf("CreditBalance: %v", err)
	}
	if want := tender.Mint(5000, 0); balance != want {
		t.Fatalf("balance = %v, want %v", balance, want)
	}
}

func TestGiftCreditsMultipleCalls(t *testing.T) {
	m := &Manager{DB: newTestDB(t)}
	ctx := t.Context()

	accountID := "exe_gift_multi"
	createTestAccount(t, m.DB, accountID, "user_gift_multi")

	p := &GiftCreditsParams{
		AmountUSD:  25.0,
		GiftPrefix: GiftPrefixDebug,
		Note:       "Repeated gift",
	}

	if err := m.GiftCredits(ctx, accountID, p); err != nil {
		t.Fatalf("GiftCredits first: %v", err)
	}
	if err := m.GiftCredits(ctx, accountID, p); err != nil {
		t.Fatalf("GiftCredits second: %v", err)
	}

	// Each call produces a unique gift_id (timestamp-based), so both insert.
	balance, err := m.CreditBalance(ctx, accountID)
	if err != nil {
		t.Fatalf("CreditBalance: %v", err)
	}
	if want := tender.Mint(5000, 0); balance != want {
		t.Fatalf("balance = %v, want %v (expected two gifts)", balance, want)
	}
}

func TestGiftCreditsDefaultNote(t *testing.T) {
	m := &Manager{DB: newTestDB(t)}
	ctx := t.Context()

	accountID := "exe_gift_default_note"
	createTestAccount(t, m.DB, accountID, "user_gift_default_note")

	err := m.GiftCredits(ctx, accountID, &GiftCreditsParams{
		AmountUSD:  1.0,
		GiftPrefix: GiftPrefixDebug,
		// Note intentionally empty
	})
	if err != nil {
		t.Fatalf("GiftCredits: %v", err)
	}

	// Verify the default note was used via ListGifts.
	gifts, err := m.ListGifts(ctx, accountID)
	if err != nil {
		t.Fatalf("ListGifts: %v", err)
	}
	if len(gifts) != 1 {
		t.Fatalf("len(gifts) = %d, want 1", len(gifts))
	}
	if want := "Credit gift from support@exe.dev"; gifts[0].Note != want {
		t.Fatalf("note = %q, want %q", gifts[0].Note, want)
	}
}

func TestGiftCreditsValidation(t *testing.T) {
	m := &Manager{DB: newTestDB(t)}
	ctx := t.Context()

	accountID := "exe_gift_validation"
	createTestAccount(t, m.DB, accountID, "user_gift_validation")

	t.Run("zero amount", func(t *testing.T) {
		err := m.GiftCredits(ctx, accountID, &GiftCreditsParams{
			AmountUSD:  0,
			GiftPrefix: GiftPrefixDebug,
		})
		if err == nil {
			t.Fatal("GiftCredits with zero amount: error = nil, want non-nil")
		}
	})

	t.Run("negative amount", func(t *testing.T) {
		err := m.GiftCredits(ctx, accountID, &GiftCreditsParams{
			AmountUSD:  -1.0,
			GiftPrefix: GiftPrefixDebug,
		})
		if err == nil {
			t.Fatal("GiftCredits with negative amount: error = nil, want non-nil")
		}
	})

	t.Run("empty prefix", func(t *testing.T) {
		err := m.GiftCredits(ctx, accountID, &GiftCreditsParams{
			AmountUSD:  1.0,
			GiftPrefix: "",
		})
		if err == nil {
			t.Fatal("GiftCredits with empty prefix: error = nil, want non-nil")
		}
	})
}

func TestGetCreditState(t *testing.T) {
	m := &Manager{DB: newTestDB(t)}
	ctx := t.Context()

	accountID := "exe_credit_state"
	createTestAccount(t, m.DB, accountID, "user_credit_state")

	// Add a gift.
	err := m.GiftCredits(ctx, accountID, &GiftCreditsParams{
		AmountUSD:  50.0,
		GiftPrefix: GiftPrefixDebug,
		Note:       "Gift 1",
	})
	if err != nil {
		t.Fatalf("GiftCredits: %v", err)
	}

	// Simulate a paid credit by inserting directly (SyncCredits path).
	stripeEventID := "pi_test_paid"
	err = exedb.WithTx1(m.DB, ctx, (*exedb.Queries).InsertPaidCredits, exedb.InsertPaidCreditsParams{
		AccountID:     accountID,
		Amount:        tender.Mint(3000, 0).Microcents(),
		StripeEventID: &stripeEventID,
	})
	if err != nil {
		t.Fatalf("insert paid credit: %v", err)
	}

	// Spend some credits.
	err = m.SpendCredits(ctx, accountID, 10, tender.Mint(100, 0)) // spend $10
	if err != nil {
		t.Fatalf("SpendCredits: %v", err)
	}

	state, err := m.GetCreditState(ctx, accountID)
	if err != nil {
		t.Fatalf("GetCreditState: %v", err)
	}

	// paid = $30
	if want := tender.Mint(3000, 0); state.Paid != want {
		t.Fatalf("Paid = %v, want %v", state.Paid, want)
	}
	// gift = $50
	if want := tender.Mint(5000, 0); state.Gift != want {
		t.Fatalf("Gift = %v, want %v", state.Gift, want)
	}
	// used = $10 (absolute value)
	if want := tender.Mint(1000, 0); state.Used != want {
		t.Fatalf("Used = %v, want %v", state.Used, want)
	}
	// total = $50 + $30 - $10 = $70
	if want := tender.Mint(7000, 0); state.Total != want {
		t.Fatalf("Total = %v, want %v", state.Total, want)
	}
}

func TestListGifts(t *testing.T) {
	m := &Manager{DB: newTestDB(t)}
	ctx := t.Context()

	accountID := "exe_list_gifts"
	createTestAccount(t, m.DB, accountID, "user_list_gifts")

	// Add multiple gifts.
	notes := []string{"First gift", "Second gift", "Third gift"}
	amounts := []float64{10.0, 20.0, 30.0}
	for i := range notes {
		if err := m.GiftCredits(ctx, accountID, &GiftCreditsParams{
			AmountUSD:  amounts[i],
			GiftPrefix: GiftPrefixDebug,
			Note:       notes[i],
		}); err != nil {
			t.Fatalf("GiftCredits(%s): %v", notes[i], err)
		}
	}

	gifts, err := m.ListGifts(ctx, accountID)
	if err != nil {
		t.Fatalf("ListGifts: %v", err)
	}

	if len(gifts) != 3 {
		t.Fatalf("len(gifts) = %d, want 3", len(gifts))
	}

	// Ordered DESC by created_at, so most recent first.
	if gifts[0].Note != "Third gift" {
		t.Fatalf("gifts[0].Note = %q, want %q", gifts[0].Note, "Third gift")
	}
	if gifts[1].Note != "Second gift" {
		t.Fatalf("gifts[1].Note = %q, want %q", gifts[1].Note, "Second gift")
	}
	if gifts[2].Note != "First gift" {
		t.Fatalf("gifts[2].Note = %q, want %q", gifts[2].Note, "First gift")
	}

	// Verify amount on the first entry ($30 = 3000 cents).
	if want := tender.Mint(3000, 0); gifts[0].Amount != want {
		t.Fatalf("gifts[0].Amount = %v, want %v", gifts[0].Amount, want)
	}
	if gifts[0].CreatedAt.IsZero() {
		t.Fatal("gifts[0].CreatedAt is zero")
	}
}

func TestGiftCreditsSignupPrefix(t *testing.T) {
	m := &Manager{DB: newTestDB(t)}
	ctx := t.Context()

	accountID := "exe_signup_gift"
	createTestAccount(t, m.DB, accountID, "user_signup_gift")

	p := &GiftCreditsParams{
		AmountUSD:  100.0,
		GiftPrefix: GiftPrefixSignup,
		Note:       "Signup bonus",
	}
	if err := m.GiftCredits(ctx, accountID, p); err != nil {
		t.Fatalf("GiftCredits: %v", err)
	}

	// Second call with the same prefix+account is silently ignored
	// because signup gift_id has no timestamp suffix.
	if err := m.GiftCredits(ctx, accountID, p); err != nil {
		t.Fatalf("GiftCredits second call: %v", err)
	}

	gifts, err := m.ListGifts(ctx, accountID)
	if err != nil {
		t.Fatalf("ListGifts: %v", err)
	}
	if len(gifts) != 1 {
		t.Fatalf("len(gifts) = %d, want 1 (signup gift should be idempotent)", len(gifts))
	}
	wantID := GiftPrefixSignup + ":" + accountID
	if gifts[0].GiftID != wantID {
		t.Fatalf("gifts[0].GiftID = %q, want %q", gifts[0].GiftID, wantID)
	}
	if gifts[0].Note != "Signup bonus" {
		t.Fatalf("gifts[0].Note = %q, want %q", gifts[0].Note, "Signup bonus")
	}

	// Balance should reflect only one gift, not two.
	balance, err := m.CreditBalance(ctx, accountID)
	if err != nil {
		t.Fatalf("CreditBalance: %v", err)
	}
	if want := tender.Mint(10000, 0); balance != want {
		t.Fatalf("balance = %v, want %v (one $100 gift)", balance, want)
	}
}

func TestListGiftsEmpty(t *testing.T) {
	m := &Manager{DB: newTestDB(t)}
	ctx := t.Context()

	accountID := "exe_list_gifts_empty"
	createTestAccount(t, m.DB, accountID, "user_list_gifts_empty")

	gifts, err := m.ListGifts(ctx, accountID)
	if err != nil {
		t.Fatalf("ListGifts: %v", err)
	}
	if gifts == nil {
		t.Fatal("ListGifts returned nil, want empty slice")
	}
	if len(gifts) != 0 {
		t.Fatalf("len(gifts) = %d, want 0", len(gifts))
	}
}


func TestBuyCreditsAmountValidation(t *testing.T) {
	m := &Manager{}

	_, err := m.BuyCredits(t.Context(), "cus_123", &BuyCreditsParams{
		Amount: tender.Zero(),
	})
	if err == nil {
		t.Fatal("BuyCredits with zero amount: error = nil, want non-nil")
	}
	if got := err.Error(); !strings.Contains(got, "amount must be positive") {
		t.Fatalf("BuyCredits with zero amount error = %q, want positive amount error", got)
	}
}

func TestBuyCreditsRoundsFractionalAmountForStripe(t *testing.T) {
	traceFile := filepath.Join("testdata", "stripe", t.Name())
	m := &Manager{
		Client: stripetest.Record(t, traceFile),
		Logger: tslog.Slogger(t),
	}
	billingID := "exe_rounding_test"
	if err := m.upsertCustomer(t.Context(), billingID, "rounding@example.com"); err != nil {
		t.Fatalf("upsertCustomer: %v", err)
	}

	gotURL, err := m.BuyCredits(t.Context(), billingID, &BuyCreditsParams{
		Amount:     tender.Mint(100, 1),
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	})
	if err != nil {
		t.Fatalf("BuyCredits: %v", err)
	}
	if !strings.HasPrefix(gotURL, "https://checkout.stripe.com/") {
		t.Fatalf("BuyCredits returned unexpected link: %q", gotURL)
	}
	u, err := url.Parse(gotURL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", gotURL, err)
	}
	sessID := path.Base(u.Path)
	sess, err := m.client().V1CheckoutSessions.Retrieve(t.Context(), sessID, nil)
	if err != nil {
		t.Fatalf("Retrieve checkout session %q: %v", sessID, err)
	}
	if got, want := sess.AmountSubtotal, int64(101); got != want {
		t.Fatalf("checkout amount_subtotal = %d, want %d", got, want)
	}
	if got, want := sess.AmountTotal, int64(101); got != want {
		t.Fatalf("checkout amount_total = %d, want %d", got, want)
	}
}

func TestLookupPriceIDLive(t *testing.T) {
	m := newTestManager(t)

	priceID, err := m.lookupPriceID(t.Context(), "individual")
	if err != nil {
		t.Fatalf("lookupPriceID(individual): %v", err)
	}
	if priceID == "" {
		t.Fatal("lookupPriceID(individual) returned empty price ID")
	}

	_, err = m.lookupPriceID(t.Context(), "missing_lookup_key")
	if err == nil {
		t.Fatal("lookupPriceID(missing_lookup_key) error = nil, want non-nil")
	}
	if got := err.Error(); !strings.Contains(got, `no active price found with lookup key "missing_lookup_key"`) {
		t.Fatalf("lookupPriceID(missing_lookup_key) error = %q, want missing lookup key error", got)
	}
}

func TestLookupPriceIDCachedLive(t *testing.T) {
	m := newTestManager(t)

	got1, err := m.lookupPriceIDCached(t.Context(), "individual")
	if err != nil {
		t.Fatalf("lookupPriceIDCached(individual) first call: %v", err)
	}
	got2, err := m.lookupPriceIDCached(t.Context(), "individual")
	if err != nil {
		t.Fatalf("lookupPriceIDCached(individual) second call: %v", err)
	}
	if got1 == "" || got2 == "" {
		t.Fatalf("lookupPriceIDCached(individual) returned empty IDs: %q %q", got1, got2)
	}
	if got1 != got2 {
		t.Fatalf("lookupPriceIDCached(individual) mismatch: %q vs %q", got1, got2)
	}

	for i := range 2 {
		_, err := m.lookupPriceIDCached(t.Context(), "missing_lookup_key")
		if err == nil {
			t.Fatalf("lookupPriceIDCached(missing_lookup_key) call %d: error = nil, want non-nil", i+1)
		}
	}
}


func TestBuyCreditsLive(t *testing.T) {
	m := newTestManager(t)
	clock := m.startClock(t)
	ctx := t.Context()

	_, err := m.BuyCredits(ctx, "exe_missing_"+clock.ID(), &BuyCreditsParams{
		Amount:     tender.Mint(100, 0),
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("BuyCredits missing customer err = %v, want %v", err, ErrNotFound)
	}

	billingID := "exe_buy_" + clock.ID()
	if err := m.upsertCustomer(ctx, billingID, "buy@example.com"); err != nil {
		t.Fatalf("upsertCustomer: %v", err)
	}

	link, err := m.BuyCredits(ctx, billingID, &BuyCreditsParams{
		Amount:     tender.Mint(100, 0),
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	})
	if err != nil {
		t.Fatalf("BuyCredits: %v", err)
	}
	if !strings.HasPrefix(link, "https://checkout.stripe.com/") {
		t.Fatalf("BuyCredits returned unexpected link: %q", link)
	}
}

func TestSyncCreditsLive(t *testing.T) {
	m := newTestManager(t)
	clock := m.startClock(t)
	ctx := t.Context()

	billingID := "exe_sync_" + clock.ID()
	if err := m.upsertCustomer(ctx, billingID, "sync@example.com"); err != nil {
		t.Fatalf("upsertCustomer: %v", err)
	}

	const purchased = 100
	if err := stripeCompleteCreditPurchase(ctx, m, billingID, "pm_card_visa", tender.Mint(purchased, 0)); err != nil {
		t.Fatalf("stripeCompleteCreditPurchase: %v", err)
	}

	since := clock.Now().Add(-1 * time.Minute)
	if err := m.SyncCredits(ctx, billingID, since); err != nil {
		t.Fatalf("SyncCredits first pass: %v", err)
	}
	if err := m.SyncCredits(ctx, billingID, since); err != nil {
		t.Fatalf("SyncCredits idempotent pass: %v", err)
	}

	got, err := m.CreditBalance(ctx, billingID)
	if err != nil {
		t.Fatalf("CreditBalance: %v", err)
	}
	want := tender.Mint(purchased, 0)
	if got != want {
		t.Fatalf("credit balance = %d, want %d", got, want)
	}
}

func TestSyncCreditsInsertErrorLive(t *testing.T) {
	m := newTestManager(t)
	clock := m.startClock(t)
	ctx := t.Context()

	billingID := "exe_sync_insert_error_" + clock.ID()
	if err := m.upsertCustomer(ctx, billingID, "sync-insert-error@example.com"); err != nil {
		t.Fatalf("upsertCustomer: %v", err)
	}
	if err := stripeCompleteCreditPurchase(ctx, m, billingID, "pm_card_visa", tender.Mint(100, 0)); err != nil {
		t.Fatalf("stripeCompleteCreditPurchase: %v", err)
	}

	m.DB = newEmptyTestDB(t)
	err := m.SyncCredits(ctx, billingID, clock.Now().Add(-1*time.Minute))
	if err == nil {
		t.Fatal("SyncCredits insert error = nil, want non-nil")
	}
	if got := err.Error(); !strings.Contains(got, "insert credit ledger entry:") {
		t.Fatalf("SyncCredits insert error = %q, want wrapped insert error", got)
	}
}

func TestUseCreditsAndExecQueryErrorPaths(t *testing.T) {
	db := newEmptyTestDB(t)
	m := &Manager{DB: db}

	err := m.SpendCredits(t.Context(), "acct_1", 1, tender.Mint(100, 0))
	if err == nil {
		t.Fatal("UseCredits error = nil, want non-nil")
	}
}

