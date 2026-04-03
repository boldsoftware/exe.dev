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
	exesqlite "exe.dev/sqlite"
	"exe.dev/tslog"
	"github.com/stripe/stripe-go/v85"
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

	err := m.SpendCredits(t.Context(), "acct_1", 1, tender.Mint(100, 0))
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
	}).All(ctx) {
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

func TestExtractPaymentMethodInfo(t *testing.T) {
	tests := []struct {
		name string
		pm   *stripe.PaymentMethod
		want *PaymentMethodInfo
	}{
		{name: "nil", pm: nil, want: nil},
		{
			name: "visa card",
			pm: &stripe.PaymentMethod{
				Type: stripe.PaymentMethodTypeCard,
				Card: &stripe.PaymentMethodCard{Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2025},
			},
			want: &PaymentMethodInfo{Type: "card", Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2025, DisplayLabel: "Visa •••• 4242"},
		},
		{
			name: "amex",
			pm: &stripe.PaymentMethod{
				Type: stripe.PaymentMethodTypeCard,
				Card: &stripe.PaymentMethodCard{Brand: "amex", Last4: "1234", ExpMonth: 6, ExpYear: 2026},
			},
			want: &PaymentMethodInfo{Type: "card", Brand: "amex", Last4: "1234", ExpMonth: 6, ExpYear: 2026, DisplayLabel: "American Express •••• 1234"},
		},
		{
			name: "link",
			pm: &stripe.PaymentMethod{
				Type: stripe.PaymentMethodTypeLink,
				Link: &stripe.PaymentMethodLink{Email: "user@example.com"},
			},
			want: &PaymentMethodInfo{Type: "link", Email: "user@example.com", DisplayLabel: "Link (user@example.com)"},
		},
		{
			name: "paypal",
			pm: &stripe.PaymentMethod{
				Type:   stripe.PaymentMethodTypePaypal,
				Paypal: &stripe.PaymentMethodPaypal{PayerEmail: "pay@example.com"},
			},
			want: &PaymentMethodInfo{Type: "paypal", Email: "pay@example.com", DisplayLabel: "PayPal (pay@example.com)"},
		},
		{
			name: "unknown sepa_debit",
			pm:   &stripe.PaymentMethod{Type: "sepa_debit"},
			want: &PaymentMethodInfo{Type: "sepa_debit", DisplayLabel: "Sepa Debit"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPaymentMethodInfo(tt.pm)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("got %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want %+v", tt.want)
			}
			if got.Type != tt.want.Type || got.Brand != tt.want.Brand || got.Last4 != tt.want.Last4 ||
				got.ExpMonth != tt.want.ExpMonth || got.ExpYear != tt.want.ExpYear ||
				got.Email != tt.want.Email || got.DisplayLabel != tt.want.DisplayLabel {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReceiptURLsAfterLive(t *testing.T) {
	m := newTestManager(t)
	clock := m.startClock(t)
	ctx := t.Context()

	billingID := "exe_receipt_after_" + clock.ID()
	if err := m.upsertCustomer(ctx, billingID, "receipts@example.com"); err != nil {
		t.Fatalf("upsertCustomer: %v", err)
	}

	before := time.Now()

	// Before purchase: no receipts.
	got, err := m.ReceiptURLsAfter(ctx, billingID, before.Add(-time.Minute))
	if err != nil {
		t.Fatalf("ReceiptURLsAfter before purchase: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 receipts before purchase, got %d", len(got))
	}

	// Create a credit purchase charge.
	if err := stripeCompleteCreditPurchase(ctx, m, billingID, "pm_card_visa", tender.Mint(100, 0)); err != nil {
		t.Fatalf("stripeCompleteCreditPurchase: %v", err)
	}

	// Since before the purchase: should return 1 receipt.
	got, err = m.ReceiptURLsAfter(ctx, billingID, before.Add(-time.Minute))
	if err != nil {
		t.Fatalf("ReceiptURLsAfter after purchase: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 receipt, got %d", len(got))
	}
	if got[0].URL == "" {
		t.Fatal("receipt URL is empty")
	}
	if got[0].Created.IsZero() {
		t.Fatal("receipt Created is zero")
	}

	// Since well after the purchase: should return 0 receipts.
	got, err = m.ReceiptURLsAfter(ctx, billingID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ReceiptURLsAfter with future since: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 receipts for future since, got %d", len(got))
	}
}

func TestFormatCardLabel(t *testing.T) {
	tests := []struct{ brand, last4, want string }{
		{"visa", "4242", "Visa •••• 4242"},
		{"mastercard", "5555", "Mastercard •••• 5555"},
		{"amex", "1234", "American Express •••• 1234"},
		{"diners", "9999", "Diners Club •••• 9999"},
		{"jcb", "0000", "JCB •••• 0000"},
		{"unknown", "1111", "Unknown •••• 1111"},
		{"visa", "", "Visa"},
	}
	for _, tt := range tests {
		if got := formatCardLabel(tt.brand, tt.last4); got != tt.want {
			t.Errorf("formatCardLabel(%q,%q) = %q, want %q", tt.brand, tt.last4, got, tt.want)
		}
	}
}

func TestFormatPaymentTypeLabel(t *testing.T) {
	tests := []struct{ in, want string }{
		{"sepa_debit", "Sepa Debit"},
		{"us_bank_account", "Us Bank Account"},
		{"card", "Card"},
		{"amazon_pay", "Amazon Pay"},
	}
	for _, tt := range tests {
		if got := formatPaymentTypeLabel(tt.in); got != tt.want {
			t.Errorf("formatPaymentTypeLabel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
