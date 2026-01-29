// Package billing provides subscription and payment management for exe.dev accounts.
package billing

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"exe.dev/errorz"
	"github.com/stripe/stripe-go/v82"
)

// MakeCustomerDashboardURL returns the Stripe dashboard URL for a customer.
func MakeCustomerDashboardURL(billingID string) string {
	return "https://dashboard.stripe.com/customers/" + billingID
}

var ErrIncomplete = errors.New("incomplete")

var stripeKey = os.Getenv("STRIPE_SECRET_KEY")

const (
	DefaultPlan = "individual"

	// TestAPIKey is the Stripe test API key. It is safe to check into source code
	// and easy to revoke should someone want to spam our test account.
	TestAPIKey = "rk_test_51SjuBkGpGU0hqBfTf92SNWOBza7zn6pZygtbG7kRdquppHsnJGVZtPfwpZFt9PjoAUCegMS1JCwtawjbWXMx2fPZ008Jgd7CKi"
)

// Manager handles billing operations.
type Manager struct {
	// APIKey specifies the Stripe API key to use for requests.
	// If empty, it will use any of the following in order of precedence:
	//
	//   1. The STRIPE_SECRET_KEY environment variable
	//   2. The sandboxAPIKey
	APIKey string

	// StripeURL is the base URL for Stripe API requests.
	// If empty, the default Stripe API URL is used.
	StripeURL string

	// Client is the Stripe client to use for requests.
	// If nil, a new client is created using APIKey and StripeURL.
	// This field is primarily for testing.
	Client *stripe.Client

	Logger *slog.Logger

	priceIDCache sync.Map // "apiKey:lookupKey" -> price ID
}

func (m *Manager) slog() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

// SubscribeParams contains the parameters for subscribing an account to a plan.
type SubscribeParams struct {
	// Email is the account's email address.
	Email string

	// Plan is the Stripe price lookup key for the plan the account is
	// signing up for. If empty, DefaultPlan is used.
	//
	// Lookup keys can be found in the Stripe dashboard under
	// https://dashboard.stripe.com/products.
	Plan string

	// SuccessURL is the URL to redirect to after successful checkout.
	SuccessURL string

	// CancelURL is the URL to redirect to if checkout is canceled.
	CancelURL string

	// TrialEnd specifies when the trial period ends. If set, billing will
	// not start until this time. If zero, billing starts immediately.
	TrialEnd time.Time

	// RedirectToPortal, when true, redirects existing subscribers to the
	// billing portal instead of creating a new checkout session.
	RedirectToPortal bool

	// PortalReturnURL is the URL to return to after visiting the billing portal.
	// Only used when RedirectToPortal is true.
	PortalReturnURL string
}

// Profile contains account profile information.
type Profile struct {
	Email string
}

// SubscriptionEvent represents a subscription state change from Stripe.
type SubscriptionEvent struct {
	// AccountID is the Stripe customer ID (billing ID).
	AccountID string
	// EventType is the type of event: "active" or "canceled".
	EventType string
	// EventAt is when the event occurred in Stripe.
	EventAt time.Time
}

func (m *Manager) client() *stripe.Client {
	if m.Client != nil {
		return m.Client
	}
	var opts []stripe.ClientOption
	if m.StripeURL != "" {
		backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
			URL: &m.StripeURL,
		})
		opts = append(opts, stripe.WithBackends(backends))
	}
	apiKey := cmp.Or(m.APIKey, stripeKey)
	return stripe.NewClient(apiKey, opts...)
}

func (m *Manager) upsertCustomer(ctx context.Context, billingID, email string) error {
	c := m.client()
	params := &stripe.CustomerCreateParams{
		Email: &email,
	}
	params.AddExtra("id", billingID)

	customer, err := c.V1Customers.Create(ctx, params)

	var requestID string
	if err != nil {
		if stripeErr, ok := errorz.AsType[*stripe.Error](err); ok && stripeErr.LastResponse != nil {
			requestID = stripeErr.LastResponse.RequestID
		}
	} else if customer.LastResponse != nil {
		requestID = customer.LastResponse.RequestID
	}

	if isExists(err) {
		m.slog().InfoContext(ctx, "customer already exists",
			"stripe_request_id", requestID,
			"billing_id", billingID,
		)
		return nil
	}
	if err != nil {
		m.slog().ErrorContext(ctx, "failed to create customer",
			"stripe_request_id", requestID,
			"billing_id", billingID,
			"error", err,
		)
		return err
	}

	m.slog().InfoContext(ctx, "customer created",
		"stripe_request_id", requestID,
		"billing_id", billingID,
		"email", email,
	)
	return nil
}

func isExists(err error) bool {
	stripeErr, ok := errorz.AsType[*stripe.Error](err)
	return ok && stripeErr.Code == stripe.ErrorCodeResourceAlreadyExists
}

// Subscribe generates a payment link for subscribing an account to a plan.
//
// It returns a payment link URL for the account to complete the subscription.
func (m *Manager) Subscribe(ctx context.Context, billingID string, p *SubscribeParams) (paymentLink string, _ error) {
	if p == nil {
		p = &SubscribeParams{}
	}

	c := m.client()

	plan := cmp.Or(p.Plan, DefaultPlan)
	priceID, err := m.lookupPriceID(ctx, c, plan)
	if err != nil {
		return "", fmt.Errorf("lookup price %q: %w", plan, err)
	}

	err = m.upsertCustomer(ctx, billingID, p.Email)
	if err != nil {
		return "", fmt.Errorf("upsert customer: %w", err)
	}

	if p.RedirectToPortal {
		hasSubscription, err := m.hasActiveSubscription(ctx, c, billingID)
		if err != nil {
			return "", fmt.Errorf("check active subscription: %w", err)
		}
		if hasSubscription {
			returnURL := cmp.Or(p.PortalReturnURL, p.SuccessURL)
			return m.openPortal(ctx, billingID, returnURL)
		}
	}

	params := &stripe.CheckoutSessionCreateParams{
		Customer: &billingID,
		Mode:     stripe.String("subscription"),
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{
			{
				Price:    &priceID,
				Quantity: stripe.Int64(1),
			},
		},
		SuccessURL: &p.SuccessURL,
		CancelURL:  &p.CancelURL,
	}

	if !p.TrialEnd.IsZero() {
		params.SubscriptionData = &stripe.CheckoutSessionCreateSubscriptionDataParams{
			TrialEnd: stripe.Int64(p.TrialEnd.Unix()),
		}
	}

	sess, err := c.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return "", err
	}

	var requestID string
	if sess.LastResponse != nil {
		requestID = sess.LastResponse.RequestID
	}
	m.slog().InfoContext(ctx, "checkout session created",
		"stripe_request_id", requestID,
		"session_id", sess.ID,
		"billing_id", billingID,
	)

	return sess.URL, nil
}

func (m *Manager) hasActiveSubscription(ctx context.Context, c *stripe.Client, customerID string) (bool, error) {
	params := &stripe.SubscriptionListParams{
		Customer: &customerID,
		Status:   stripe.String("all"),
	}
	for sub, err := range c.V1Subscriptions.List(ctx, params) {
		if err != nil {
			var requestID string
			if stripeErr, ok := errorz.AsType[*stripe.Error](err); ok && stripeErr.LastResponse != nil {
				requestID = stripeErr.LastResponse.RequestID
			}
			m.slog().ErrorContext(ctx, "failed to list subscriptions",
				"stripe_request_id", requestID,
				"customer_id", customerID,
				"error", err,
			)
			return false, err
		}
		switch sub.Status {
		case stripe.SubscriptionStatusActive,
			stripe.SubscriptionStatusTrialing,
			stripe.SubscriptionStatusPastDue:
			return true, nil
		}
	}
	return false, nil
}

// lookupPriceID finds the price ID for a given lookup key, caching results.
func (m *Manager) lookupPriceID(ctx context.Context, c *stripe.Client, lookupKey string) (string, error) {
	cacheKey := m.APIKey + ":" + lookupKey
	if v, ok := m.priceIDCache.Load(cacheKey); ok {
		return v.(string), nil
	}

	for price, err := range c.V1Prices.List(ctx, &stripe.PriceListParams{
		LookupKeys: []*string{&lookupKey},
		Active:     stripe.Bool(true),
	}) {
		if err != nil {
			var requestID string
			if stripeErr, ok := errorz.AsType[*stripe.Error](err); ok && stripeErr.LastResponse != nil {
				requestID = stripeErr.LastResponse.RequestID
			}
			m.slog().ErrorContext(ctx, "failed to lookup price",
				"stripe_request_id", requestID,
				"lookup_key", lookupKey,
				"error", err,
			)
			return "", err
		}
		m.priceIDCache.Store(cacheKey, price.ID)
		return price.ID, nil
	}
	return "", fmt.Errorf("no active price found with lookup key %q", lookupKey)
}

// VerifyCheckout verifies that a checkout session was completed successfully.
// It returns the billing ID if the session is valid, or an error if the account is not in good standing.
func (m *Manager) VerifyCheckout(ctx context.Context, sessionID string) (billingID string, _ error) {
	if sessionID == "" {
		return "", errors.New("session ID is required")
	}

	c := m.client()
	sess, err := c.V1CheckoutSessions.Retrieve(ctx, sessionID, nil)
	if err != nil {
		return "", err
	}

	var requestID string
	if sess.LastResponse != nil {
		requestID = sess.LastResponse.RequestID
	}

	if sess.Status != stripe.CheckoutSessionStatusComplete {
		m.slog().ErrorContext(ctx, "checkout session incomplete",
			"stripe_request_id", requestID,
			"session_id", sessionID,
			"status", sess.Status,
		)
		return "", fmt.Errorf("%w: status: %q", ErrIncomplete, sess.Status)
	}

	switch sess.PaymentStatus {
	case stripe.CheckoutSessionPaymentStatusPaid, stripe.CheckoutSessionPaymentStatusNoPaymentRequired:
		if sess.Customer == nil || sess.Customer.ID == "" {
			m.slog().ErrorContext(ctx, "checkout session has no customer",
				"stripe_request_id", requestID,
				"session_id", sessionID,
			)
			return "", errors.New("checkout session has no customer")
		}
		m.slog().InfoContext(ctx, "checkout session verified",
			"stripe_request_id", requestID,
			"session_id", sessionID,
			"billing_id", sess.Customer.ID,
			"payment_status", sess.PaymentStatus,
		)
		return sess.Customer.ID, nil
	default:
		m.slog().ErrorContext(ctx, "checkout payment not confirmed",
			"stripe_request_id", requestID,
			"session_id", sessionID,
			"payment_status", sess.PaymentStatus,
		)
		return "", fmt.Errorf("checkout session payment not confirmed: payment_status=%s", sess.PaymentStatus)
	}
}

// openPortal creates a billing portal session using PortalParams.
// This is a convenience wrapper around PortalSession that validates the return URL.
func (m *Manager) openPortal(ctx context.Context, billingID, returnURL string) (portalURL string, _ error) {
	if billingID == "" {
		return "", errors.New("billing ID is required")
	}
	if returnURL == "" {
		return "", errors.New("return URL is required")
	}

	c := m.client()
	params := &stripe.BillingPortalSessionCreateParams{
		Customer:  &billingID,
		ReturnURL: &returnURL,
	}
	sess, err := c.V1BillingPortalSessions.Create(ctx, params)
	if err != nil {
		return "", err
	}

	var requestID string
	if sess.LastResponse != nil {
		requestID = sess.LastResponse.RequestID
	}
	m.slog().InfoContext(ctx, "billing portal session created",
		"stripe_request_id", requestID,
		"billing_id", billingID,
	)
	return sess.URL, nil
}

// SubscriptionEvents polls the Stripe Events API for subscription changes.
// Events are yielded in chronological order (sorted by Stripe event created time).
// The iterator stops when the context is canceled.
func (m *Manager) SubscriptionEvents(ctx context.Context, since time.Time) iter.Seq[SubscriptionEvent] {
	return func(yield func(SubscriptionEvent) bool) {
		c := m.client()
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		sinceUnix := since.Unix()

		logErr := func(err error) {
			stripeErr, _ := errorz.AsType[*stripe.Error](err)
			var requestID string
			if stripeErr != nil && stripeErr.LastResponse != nil {
				requestID = stripeErr.LastResponse.RequestID
			}

			if stripeErr != nil && stripeErr.HTTPStatusCode == 429 {
				m.slog().WarnContext(ctx, "rate limited listing subscription events",
					"stripe_request_id", requestID,
					"error", err,
				)
			} else {
				m.slog().ErrorContext(ctx, "error listing subscription events",
					"stripe_request_id", requestID,
					"error", err,
				)
			}
		}

		poll := func() (stop bool, err error) {
			params := &stripe.EventListParams{
				Types: []*string{
					stripe.String("customer.subscription.created"),
					stripe.String("customer.subscription.updated"),
					stripe.String("customer.subscription.deleted"),
				},
				CreatedRange: &stripe.RangeQueryParams{
					GreaterThan: sinceUnix,
				},
			}

			var events []SubscriptionEvent
			for event, err := range c.V1Events.List(ctx, params) {
				if err != nil {
					return false, err
				}

				// Parse subscription from event data
				var sub stripe.Subscription
				if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
					continue
				}
				if sub.Customer == nil || sub.Customer.ID == "" {
					continue
				}

				// Determine event type based on subscription status
				var eventType string
				switch event.Type {
				case "customer.subscription.deleted":
					eventType = "canceled"
				case "customer.subscription.created":
					// Only record created events for active subscriptions
					// Trialing subscriptions count as active
					switch sub.Status {
					case stripe.SubscriptionStatusActive,
						stripe.SubscriptionStatusTrialing:
						eventType = "active"
					default:
						// Skip non-active created events
						continue
					}
				case "customer.subscription.updated":
					// Record status changes for updated events
					switch sub.Status {
					case stripe.SubscriptionStatusActive,
						stripe.SubscriptionStatusTrialing:
						eventType = "active"
					case stripe.SubscriptionStatusCanceled,
						stripe.SubscriptionStatusIncomplete,
						stripe.SubscriptionStatusIncompleteExpired,
						stripe.SubscriptionStatusUnpaid:
						eventType = "canceled"
					case stripe.SubscriptionStatusPastDue:
						// Past due still has access until it becomes unpaid
						eventType = "active"
					default:
						// Skip unknown statuses
						continue
					}
				default:
					// Skip unknown event types
					continue
				}

				events = append(events, SubscriptionEvent{
					AccountID: sub.Customer.ID,
					EventType: eventType,
					EventAt:   time.Unix(event.Created, 0),
				})
			}

			// Sort by EventAt (chronological order)
			slices.SortFunc(events, func(a, b SubscriptionEvent) int {
				return a.EventAt.Compare(b.EventAt)
			})

			var maxEventAt int64
			for _, e := range events {
				maxEventAt = max(maxEventAt, e.EventAt.Unix())
				if !yield(e) {
					if maxEventAt > sinceUnix {
						sinceUnix = maxEventAt
					}
					return true, nil
				}
			}
			if maxEventAt > sinceUnix {
				sinceUnix = maxEventAt
			}
			return false, nil
		}

		// Continue polling on interval
		for {
			_, err := poll()
			if err != nil && !errors.Is(err, context.Canceled) {
				logErr(err)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}
}
