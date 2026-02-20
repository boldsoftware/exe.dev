package billing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"exe.dev/billing/tender"
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

		got, err := m.SpendCredits(t.Context(), aliceID, 0, tender.Zero())
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

	err = m.SyncCredits(t.Context(), clock.Now())
	check(err, nil)

	// Check that the credits were added to the ledger.
	balance, err := m.SpendCredits(t.Context(), aliceID, 0, tender.Zero())
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

	balance, err := m.SpendCredits(ctx, accountID, 3, tender.Mint(0, 5000))
	if err != nil {
		t.Fatalf("UseCredits fractional: %v", err)
	}
	if want := tender.Mint(-1, -5000); balance != want {
		t.Fatalf("fractional balance = %v, want %v", balance, want)
	}

	balance, err = m.SpendCredits(ctx, accountID, 2, tender.Mint(1, 0))
	if err != nil {
		t.Fatalf("UseCredits whole cents: %v", err)
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

	_, err := m.SpendCredits(ctx, accountID, 1, tender.Mint(0, -1))
	if err == nil {
		t.Fatal("SpendCredits with negative unitPrice error = nil, want non-nil")
	}
	if got := err.Error(); !strings.Contains(got, "unit price must be non-negative") {
		t.Fatalf("SpendCredits with negative unitPrice error = %q, want non-negative unit price error", got)
	}

	balance, err := m.SpendCredits(ctx, accountID, 0, tender.Zero())
	if err != nil {
		t.Fatalf("read balance after rejected spend: %v", err)
	}
	if balance != tender.Zero() {
		t.Fatalf("balance after rejected spend = %v, want %v", balance, tender.Zero())
	}
}

// stripeCompleteCreditPurchase simulates a completed credit purchase by creating
// and confirming a PaymentIntent with credit_purchase metadata. This generates the
// payment_intent.succeeded event that SyncCredits processes.
// cents is the amount in cents (1/100 USD), matching what BuyCredits sends to Stripe.
func stripeCompleteCreditPurchase(ctx context.Context, m *Manager, customerID, paymentMethodID string, amount tender.Value) error {
	c := m.client()

	pm, err := c.V1PaymentMethods.Attach(ctx, paymentMethodID, &stripe.PaymentMethodAttachParams{
		Customer: &customerID,
	})
	if err != nil {
		return err
	}

	p := &stripe.PaymentIntentCreateParams{
		Amount:             new(amount.Cents()),
		Currency:           stripe.String("usd"),
		Customer:           &customerID,
		PaymentMethod:      &pm.ID,
		PaymentMethodTypes: []*string{stripe.String("card")},
		Confirm:            new(true),
	}
	p.AddMetadata("type", "credit_purchase")

	_, err = c.V1PaymentIntents.Create(ctx, p)
	return err
}
