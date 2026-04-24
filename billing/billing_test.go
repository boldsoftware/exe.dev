package billing

import (
	"errors"
	"net/url"
	"path"
	"strings"
	"testing"
	"time"

	"exe.dev/tslog"
	"github.com/stripe/stripe-go/v85"
)

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

func TestSubscriptionLookupKey(t *testing.T) {
	tests := []struct {
		name string
		sub  *stripe.Subscription
		want string
	}{
		{
			name: "nil subscription",
			sub:  nil,
			want: "",
		},
		{
			name: "no items",
			sub:  &stripe.Subscription{},
			want: "",
		},
		{
			name: "returns lookup key from non-metered price",
			sub: &stripe.Subscription{
				Items: &stripe.SubscriptionItemList{
					Data: []*stripe.SubscriptionItem{{
						Price: &stripe.Price{
							LookupKey: "individual",
							Recurring: &stripe.PriceRecurring{Interval: stripe.PriceRecurringIntervalMonth},
						},
					}},
				},
			},
			want: "individual",
		},
		{
			name: "skips metered items",
			sub: &stripe.Subscription{
				Items: &stripe.SubscriptionItemList{
					Data: []*stripe.SubscriptionItem{
						{
							Price: &stripe.Price{
								LookupKey: "individual:usage-disk:20260106",
								Recurring: &stripe.PriceRecurring{UsageType: stripe.PriceRecurringUsageTypeMetered},
							},
						},
						{
							Price: &stripe.Price{
								LookupKey: "individual:medium:monthly:20160102",
								Recurring: &stripe.PriceRecurring{Interval: stripe.PriceRecurringIntervalMonth},
							},
						},
					},
				},
			},
			want: "individual:medium:monthly:20160102",
		},
		{
			name: "no lookup key returns empty",
			sub: &stripe.Subscription{
				Items: &stripe.SubscriptionItemList{
					Data: []*stripe.SubscriptionItem{{
						Price: &stripe.Price{
							Recurring: &stripe.PriceRecurring{Interval: stripe.PriceRecurringIntervalMonth},
						},
					}},
				},
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := subscriptionLookupKey(tt.sub)
			if got != tt.want {
				t.Errorf("subscriptionLookupKey() = %q, want %q", got, tt.want)
			}
		})
	}
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
