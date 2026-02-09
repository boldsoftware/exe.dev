package billing

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"github.com/stripe/stripe-go/v82"
)

// stripeSubscribe is a helper function that creates a subscription for the given
// customer ID and price lookup key using the Stripe API directly to simulate checkout/portal subscription creation.
func stripeSubscribe(ctx context.Context, m *Manager, customerID, paymentMethodID, priceLookupKey string) error {
	c := m.client()
	priceID, err := m.lookupPriceID(ctx, priceLookupKey)
	if err != nil {
		return err
	}

	// Attach payment method to customer
	pm, err := c.V1PaymentMethods.Attach(ctx, paymentMethodID, &stripe.PaymentMethodAttachParams{
		Customer: &customerID,
	})
	if err != nil {
		return err
	}

	_, err = c.V1Subscriptions.Create(ctx, &stripe.SubscriptionCreateParams{
		DefaultPaymentMethod: &pm.ID,
		Customer:             &customerID,
		Items: []*stripe.SubscriptionCreateItemParams{
			{
				Price: &priceID,
			},
		},
	})
	return err
}

func TestSubscribeNewThenActive(t *testing.T) {
	m := newTestManager(t)
	clock := m.startClock(t)

	ctx := t.Context()

	// Use a unique customer ID that includes the test clock ID
	customerID := "exe_test_" + clock.ID()

	link, err := m.Subscribe(ctx, customerID, &SubscribeParams{
		Email: "user@example.com",

		// Use first.com and second.com so our second request for a
		// checkout link does not clobber the first recording.
		SuccessURL: "https://first.com/return",
		CancelURL:  "https://first.com/cancel",
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// New user goes right to checkout.
	if !strings.HasPrefix(link, "https://checkout.stripe.com/") {
		t.Fatalf("unexpected link prefix (want %q): %q", "https://checkout.stripe.com/", link)
	}

	// Simulate that the user completed checkout and now has a subscription with a valid card.
	if err := stripeSubscribe(ctx, m, customerID, "pm_card_visa", "individual"); err != nil {
		t.Fatalf("stripeSubscribe: %v", err)
	}

	// If RedirectToPortal is true, link should go to the portal for existing subscribers.
	link, err = m.Subscribe(ctx, customerID, &SubscribeParams{
		Email:            "user@example.com",
		SuccessURL:       "https://second.com/return",
		CancelURL:        "https://second.com/cancel",
		RedirectToPortal: true,
	})
	if err != nil {
		t.Fatalf("Subscribe existing: %v", err)
	}
	if !strings.HasPrefix(link, "https://billing.stripe.com/p/session/") {
		t.Fatalf("Subscribe existing: unexpected link %q", link)
	}

	// If RedirectToPortal is false, link should go to SuccessURL for existing subscribers.
	link, err = m.Subscribe(ctx, customerID, &SubscribeParams{
		Email:            "user@example.com",
		SuccessURL:       "https://example.com/return",
		CancelURL:        "https://example.com/cancel",
		RedirectToPortal: false,
	})
	if err != nil {
		t.Fatalf("Subscribe existing no redirect: %v", err)
	}
	if link != "https://example.com/return" {
		t.Fatalf("Subscribe existing no redirect: unexpected link %q", link)
	}
}

func TestUseCredits(t *testing.T) {
	db := newTestDB(t)
	accountID := "exe_test_credits"
	userID := "usr_test_credits"
	createTestAccount(t, db, accountID, userID)

	m := &Manager{DB: db}
	ctx := t.Context()

	// Seed 1000 microcents into the ledger so UseCredits has something to deduct from.
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		_, err := q.UseCredits(ctx, exedb.UseCreditsParams{
			AccountID: accountID,
			Amount:    1000,
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed credits: %v", err)
	}

	// Deduct 400 microcents; expect 600 remaining.
	remaining, err := m.UseCredits(ctx, accountID, 400)
	if err != nil {
		t.Fatalf("UseCredits(400): %v", err)
	}
	if remaining != 600 {
		t.Fatalf("UseCredits(400): remaining = %d, want 600", remaining)
	}

	// Deduct 800 more; balance goes negative to -200. Negative balances are allowed.
	remaining, err = m.UseCredits(ctx, accountID, 800)
	if err != nil {
		t.Fatalf("UseCredits(800): %v", err)
	}
	if remaining != -200 {
		t.Fatalf("UseCredits(800): remaining = %d, want -200", remaining)
	}
}

func TestBuyCredits(t *testing.T) {
	m := newTestManager(t)
	clock := m.startClock(t)
	ctx := t.Context()

	customerID := "exe_test_" + clock.ID()

	link, err := m.BuyCredits(ctx, customerID, &BuyCreditsParams{
		Email:      "buyer@example.com",
		Amount:     500_000_000, // 500M microcents = $50
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	})
	if err != nil {
		t.Fatalf("BuyCredits: %v", err)
	}
	if !strings.HasPrefix(link, "https://checkout.stripe.com/") {
		t.Fatalf("BuyCredits: unexpected link %q", link)
	}
}

func TestBuyCreditsValidation(t *testing.T) {
	m := &Manager{} // no Stripe client needed; validation happens before any API call
	ctx := t.Context()

	tests := []struct {
		name   string
		amount int64
		errStr string
	}{
		{"zero", 0, "must be positive"},
		{"negative", -10000, "must be positive"},
		{"not cent-aligned", 5000, "cent-aligned"},
		{"fractional cents", 10001, "cent-aligned"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := m.BuyCredits(ctx, "cus_test", &BuyCreditsParams{
				Email:      "test@example.com",
				Amount:     tt.amount,
				SuccessURL: "https://example.com/success",
				CancelURL:  "https://example.com/cancel",
			})
			if err == nil {
				t.Fatalf("expected error for amount %d", tt.amount)
			}
			if !strings.Contains(err.Error(), tt.errStr) {
				t.Fatalf("error %q does not contain %q", err, tt.errStr)
			}
		})
	}
}

// stripeCompleteCreditPurchase simulates a completed credit purchase by creating
// and confirming a PaymentIntent with credit_purchase metadata. This generates the
// payment_intent.succeeded event that SyncCredits processes.
func stripeCompleteCreditPurchase(ctx context.Context, m *Manager, customerID, paymentMethodID string, microcents int64) error {
	c := m.client()

	pm, err := c.V1PaymentMethods.Attach(ctx, paymentMethodID, &stripe.PaymentMethodAttachParams{
		Customer: &customerID,
	})
	if err != nil {
		return err
	}

	cents := microcents / 10000
	piParams := &stripe.PaymentIntentCreateParams{
		Amount:             &cents,
		Currency:           stripe.String("usd"),
		Customer:           &customerID,
		PaymentMethod:      &pm.ID,
		PaymentMethodTypes: []*string{stripe.String("card")},
		Confirm:            stripe.Bool(true),
	}
	piParams.AddMetadata("type", "credit_purchase")
	piParams.AddMetadata("microcents", fmt.Sprintf("%d", microcents))

	_, err = c.V1PaymentIntents.Create(ctx, piParams)
	return err
}

func TestSyncCredits(t *testing.T) {
	m := newTestManager(t)
	db := newTestDB(t)
	m.DB = db
	clock := m.startClock(t)
	ctx := t.Context()

	customerID := "exe_test_" + clock.ID()
	createTestAccount(t, db, customerID, "usr_sync_"+clock.ID())

	// Create the customer in Stripe (on test clock for unique ID).
	if err := m.upsertCustomer(ctx, customerID, "sync@example.com"); err != nil {
		t.Fatalf("upsertCustomer: %v", err)
	}

	// Simulate a completed credit purchase.
	microcents := int64(100_000_000) // 100M microcents = $10
	if err := stripeCompleteCreditPurchase(ctx, m, customerID, "pm_card_visa", microcents); err != nil {
		t.Fatalf("stripeCompleteCreditPurchase: %v", err)
	}

	// Sync credits from Stripe to DB.
	epoch := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := m.SyncCredits(ctx, customerID, epoch); err != nil {
		t.Fatalf("SyncCredits: %v", err)
	}

	// Verify the balance.
	balance, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetCreditBalance, customerID)
	if err != nil {
		t.Fatalf("GetCreditBalance: %v", err)
	}
	if balance != microcents {
		t.Fatalf("balance = %d, want %d", balance, microcents)
	}

	// Sync again — should be idempotent, balance unchanged.
	if err := m.SyncCredits(ctx, customerID, epoch); err != nil {
		t.Fatalf("SyncCredits (idempotent): %v", err)
	}
	balance, err = exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetCreditBalance, customerID)
	if err != nil {
		t.Fatalf("GetCreditBalance (idempotent): %v", err)
	}
	if balance != microcents {
		t.Fatalf("balance after idempotent sync = %d, want %d", balance, microcents)
	}
}
