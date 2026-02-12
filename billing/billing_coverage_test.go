package billing

import (
	"errors"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"exe.dev/billing/tender"
	exesqlite "exe.dev/sqlite"
	"exe.dev/tslog"
	"github.com/stripe/stripe-go/v82"
)

func newEmptyTestDB(t *testing.T) *exesqlite.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "billing_empty.db")
	db, err := exesqlite.New(dbPath, 1)
	if err != nil {
		t.Fatalf("exesqlite.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMakeCustomerDashboardURL(t *testing.T) {
	const billingID = "cus_123"
	want := "https://dashboard.stripe.com/customers/" + billingID

	if got := MakeCustomerDashboardURL(billingID); got != want {
		t.Fatalf("MakeCustomerDashboardURL(%q) = %q, want %q", billingID, got, want)
	}
}

func TestManagerSlog(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		m := &Manager{}
		if got := m.slog(); got == nil {
			t.Fatal("m.slog() = nil, want non-nil logger")
		}
	})

	t.Run("custom", func(t *testing.T) {
		l := tslog.Slogger(t)
		m := &Manager{Logger: l}
		if got := m.slog(); got != l {
			t.Fatalf("m.slog() = %p, want %p", got, l)
		}
	})
}

func TestManagerClient(t *testing.T) {
	t.Run("provided client", func(t *testing.T) {
		c := stripe.NewClient(TestAPIKey)
		m := &Manager{Client: c}
		if got := m.client(); got != c {
			t.Fatalf("m.client() = %p, want %p", got, c)
		}
	})

	t.Run("default client", func(t *testing.T) {
		m := &Manager{}
		if got := m.client(); got == nil {
			t.Fatal("m.client() = nil, want non-nil client")
		}
	})
}

func TestWithRequestID(t *testing.T) {
	t.Run("stripe error with request id", func(t *testing.T) {
		attr := withRequestID(&stripe.Error{
			APIResource: stripe.APIResource{
				LastResponse: &stripe.APIResponse{
					RequestID: "req_123",
				},
			},
		})

		if attr.Key != "stripe_request_id" {
			t.Fatalf("attr.Key = %q, want %q", attr.Key, "stripe_request_id")
		}
		if got := attr.Value.String(); got != "req_123" {
			t.Fatalf("attr.Value = %q, want %q", got, "req_123")
		}
	})

	t.Run("non-stripe error", func(t *testing.T) {
		attr := withRequestID(errors.New("boom"))
		if got := attr.Value.String(); got != "" {
			t.Fatalf("attr.Value = %q, want empty string", got)
		}
	})
}

func TestVerifyCheckoutStory(t *testing.T) {
	m := newTestManager(t)
	clock := m.startClock(t)

	billingID := "exe_test_" + clock.ID()

	link, err := m.Subscribe(t.Context(), billingID, &SubscribeParams{
		Email:      "user@example.com",
		SuccessURL: "https://example.com/return",
		CancelURL:  "https://example.com/cancel",
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	t.Run("missing session id", func(t *testing.T) {
		gotBillingID, err := m.VerifyCheckout(t.Context(), "")
		if err == nil {
			t.Fatal(`VerifyCheckout("") error = nil, want non-nil`)
		}
		if gotBillingID != "" {
			t.Fatalf(`VerifyCheckout("") billingID = %q, want empty string`, gotBillingID)
		}
		if got, want := err.Error(), "session ID is required"; got != want {
			t.Fatalf(`VerifyCheckout("") error = %q, want %q`, got, want)
		}
	})

	t.Run("incomplete checkout session", func(t *testing.T) {
		u, err := url.Parse(link)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", link, err)
		}

		sessionID := path.Base(u.Path)
		gotBillingID, err := m.VerifyCheckout(t.Context(), sessionID)
		if !errors.Is(err, ErrIncomplete) {
			t.Fatalf("VerifyCheckout(%q) err = %v, want %v", sessionID, err, ErrIncomplete)
		}
		if gotBillingID != "" {
			t.Fatalf("VerifyCheckout(%q) billingID = %q, want empty string", sessionID, gotBillingID)
		}
		if err == nil || !strings.Contains(err.Error(), `status: "open"`) {
			t.Fatalf("VerifyCheckout(%q) error = %v, want status open detail", sessionID, err)
		}
	})

	t.Run("missing checkout session", func(t *testing.T) {
		gotBillingID, err := m.VerifyCheckout(t.Context(), "cs_missing_"+clock.ID())
		if err == nil {
			t.Fatal("VerifyCheckout(missing) error = nil, want non-nil")
		}
		if gotBillingID != "" {
			t.Fatalf("VerifyCheckout(missing) billingID = %q, want empty string", gotBillingID)
		}
	})
}

func TestOpenPortalValidation(t *testing.T) {
	m := &Manager{}

	_, err := m.openPortal(t.Context(), "", "https://example.com/return")
	if err == nil {
		t.Fatal("openPortal with empty billing ID: error = nil, want non-nil")
	}
	if got, want := err.Error(), "billing ID is required"; got != want {
		t.Fatalf("openPortal empty billing ID error = %q, want %q", got, want)
	}

	_, err = m.openPortal(t.Context(), "cus_123", "")
	if err == nil {
		t.Fatal("openPortal with empty return URL: error = nil, want non-nil")
	}
	if got, want := err.Error(), "return URL is required"; got != want {
		t.Fatalf("openPortal empty return URL error = %q, want %q", got, want)
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

func TestSubscribeLookupPriceError(t *testing.T) {
	m := newTestManager(t)

	_, err := m.Subscribe(t.Context(), "exe_test_missing_price", &SubscribeParams{
		Email:      "user@example.com",
		Plan:       "missing_lookup_key",
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	})
	if err == nil {
		t.Fatal("Subscribe missing price error = nil, want non-nil")
	}
	if got := err.Error(); !strings.Contains(got, `lookup price "missing_lookup_key"`) {
		t.Fatalf("Subscribe missing price error = %q, want lookup price wrapper", got)
	}
}

func TestOpenPortalLive(t *testing.T) {
	m := newTestManager(t)
	clock := m.startClock(t)
	ctx := t.Context()

	billingID := "exe_portal_" + clock.ID()

	_, err := m.openPortal(ctx, billingID, "https://example.com/return")
	if err == nil {
		t.Fatal("openPortal missing customer error = nil, want non-nil")
	}

	if err := m.upsertCustomer(ctx, billingID, "portal@example.com"); err != nil {
		t.Fatalf("upsertCustomer: %v", err)
	}

	link, err := m.openPortal(ctx, billingID, "https://example.com/return")
	if err != nil {
		t.Fatalf("openPortal: %v", err)
	}
	if !strings.HasPrefix(link, "https://billing.stripe.com/p/session/") {
		t.Fatalf("openPortal returned unexpected link: %q", link)
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
	if err := m.SyncCredits(ctx, since); err != nil {
		t.Fatalf("SyncCredits first pass: %v", err)
	}
	if err := m.SyncCredits(ctx, since); err != nil {
		t.Fatalf("SyncCredits idempotent pass: %v", err)
	}

	got, err := m.UseCredits(ctx, billingID, 0, tender.Zero())
	if err != nil {
		t.Fatalf("UseCredits read balance: %v", err)
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
	err := m.SyncCredits(ctx, clock.Now().Add(-1*time.Minute))
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

	_, err := m.UseCredits(t.Context(), "acct_1", 1, tender.Mint(100, 0))
	if err == nil {
		t.Fatal("UseCredits error = nil, want non-nil")
	}
}

func TestSyncSubscriptionsLive(t *testing.T) {
	m := newTestManager(t)
	clock := m.startClock(t)
	ctx := t.Context()

	billingID := "exe_events_" + clock.ID()
	if err := m.upsertCustomer(ctx, billingID, "events@example.com"); err != nil {
		t.Fatalf("upsertCustomer: %v", err)
	}
	if err := stripeSubscribe(ctx, m, billingID, "pm_card_visa", "individual"); err != nil {
		t.Fatalf("stripeSubscribe: %v", err)
	}

	var subID string
	for sub, err := range m.client().V1Subscriptions.List(ctx, &stripe.SubscriptionListParams{
		Customer: &billingID,
		Status:   stripe.String("all"),
	}) {
		if err != nil {
			t.Fatalf("list subscriptions: %v", err)
		}
		subID = sub.ID
		break
	}
	if subID == "" {
		t.Fatal("subscription not found")
	}

	if _, err := m.client().V1Subscriptions.Cancel(ctx, subID, nil); err != nil {
		t.Fatalf("cancel subscription: %v", err)
	}

	since := clock.Now().Add(-1 * time.Minute)
	nextSince, err := m.SyncSubscriptions(ctx, since)
	if err != nil {
		t.Fatalf("SyncSubscriptions: %v", err)
	}
	if !nextSince.After(since) {
		t.Fatalf("SyncSubscriptions nextSince = %v, want > %v", nextSince, since)
	}

	events, err := m.SubscriptionEvents(ctx, billingID)
	if err != nil {
		t.Fatalf("SubscriptionEvents: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("billing events for %q = %v, want at least active and canceled", billingID, events)
	}
	if events[0].EventType != "active" || events[len(events)-1].EventType != "canceled" {
		t.Fatalf("billing events order = %v, want active then canceled", events)
	}
}
