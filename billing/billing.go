// Package billing provides subscription and payment management for exe.dev accounts.
package billing

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"exe.dev/backoff"
	"github.com/stripe/stripe-go/v82"
)

// Errors
var (
	// ErrIncomplete is returned when a billing operation is incomplete.
	ErrIncomplete = errors.New("incomplete")
)

// TestAPIKey is the Stripe test API key. It is safe to check into source code
// and easy to revoke should someone want to spam our test account.
const TestAPIKey = "rk_test_51SjuBkGpGU0hqBfTf92SNWOBza7zn6pZygtbG7kRdquppHsnJGVZtPfwpZFt9PjoAUCegMS1JCwtawjbWXMx2fPZ008Jgd7CKi"

var stripeKey = os.Getenv("STRIPE_API_KEY")

const DefaultPlan = "individual"

// Manager handles billing operations.
type Manager struct {
	// APIKey specifies the Stripe API key to use for requests.
	// If empty, it will use any of the following in order of precedence:
	//
	//   1. The STRIPE_API_KEY environment variable
	//   2. The sandboxAPIKey
	APIKey string

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
}

// Profile contains account profile information.
type Profile struct {
	Email string
}

func (m *Manager) client() *stripe.Client {
	apiKey := m.APIKey
	if apiKey == "" {
		apiKey = stripeKey
	}
	return stripe.NewClient(apiKey)
}

// upsertCustomer creates a customer in Stripe if one does not already exist for the provided billing ID.
// If the customer already exists, nothing happens.
func (m *Manager) upsertCustomer(ctx context.Context, billingID, email string) error {
	c := m.client()
	custParams := &stripe.CustomerCreateParams{
		Email: &email,
	}
	custParams.AddExtra("id", billingID)

	_, err := c.V1Customers.Create(ctx, custParams)
	if err != nil && !isExists(err) {
		return err
	}
	return nil
}

func isExists(err error) bool {
	var stripeErr *stripe.Error
	return errors.As(err, &stripeErr) && stripeErr.Code == stripe.ErrorCodeResourceAlreadyExists
}

// isRetryable returns true if the error is a transient error that should be retried.
// 4xx errors (except 429 rate limit) are not retryable.
func isRetryable(err error) bool {
	var stripeErr *stripe.Error
	if !errors.As(err, &stripeErr) {
		// Network errors and other non-Stripe errors are retryable
		return true
	}
	// Rate limit errors (429) are retryable
	if stripeErr.HTTPStatusCode == 429 {
		return true
	}
	// Other 4xx errors are not retryable (bad request, not found, etc.)
	if stripeErr.HTTPStatusCode >= 400 && stripeErr.HTTPStatusCode < 500 {
		return false
	}
	// 5xx errors are retryable
	return true
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

	// Look up the price by its lookup key
	priceID, err := m.lookupPriceID(ctx, c, plan)
	if err != nil {
		return "", fmt.Errorf("lookup price %q: %w", plan, err)
	}

	for err := range backoff.Loop(ctx, 1*time.Second) {
		if err != nil {
			return "", err
		}
		err := m.upsertCustomer(ctx, billingID, p.Email)
		if err != nil {
			m.slog().ErrorContext(ctx, "upsert customer", "error", err)
			continue
		}
		break
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

	// Set trial end if specified (defers first billing until that date)
	if !p.TrialEnd.IsZero() {
		params.SubscriptionData = &stripe.CheckoutSessionCreateSubscriptionDataParams{
			TrialEnd: stripe.Int64(p.TrialEnd.Unix()),
		}
	}

	for err := range backoff.Loop(ctx, 1*time.Second) {
		if err != nil {
			m.slog().ErrorContext(ctx, "create checkout session", "error", err)
			return "", err
		}

		sess, err := c.V1CheckoutSessions.Create(ctx, params)
		if err != nil {
			if !isRetryable(err) {
				// Client errors (4xx) won't succeed on retry
				m.slog().WarnContext(ctx, "create checkout session", "error", err)
				return "", err
			}
			m.slog().ErrorContext(ctx, "create checkout session", "error", err)
			continue
		}
		return sess.URL, nil
	}

	panic("unreachable")
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
			return "", err
		}
		m.priceIDCache.Store(cacheKey, price.ID)
		return price.ID, nil
	}
	return "", fmt.Errorf("no active price found with lookup key %q", lookupKey)
}

// UpdateProfile updates the account's profile information.
func (m *Manager) UpdateProfile(ctx context.Context, billingID string, p *Profile) error {
	if p == nil {
		return nil
	}

	c := m.client()

	params := &stripe.CustomerUpdateParams{}
	if p.Email != "" {
		params.Email = &p.Email
	}

	for range backoff.Loop(ctx, 1*time.Second) {
		_, err := c.V1Customers.Update(ctx, billingID, params)
		if err == nil {
			return nil
		}
		m.slog().ErrorContext(ctx, "update customer profile", "error", err)
	}
	return ctx.Err()
}

// DashboardURL returns the Stripe dashboard URL for a customer.
func (m *Manager) DashboardURL(billingID string) string {
	return "https://dashboard.stripe.com/customers/" + billingID
}

// VerifyCheckout verifies that a checkout session was completed successfully.
// It returns the billing ID if the session is valid, or an error if the account is not in good standing.
func (m *Manager) VerifyCheckout(ctx context.Context, sessionID string) (billingID string, _ error) {
	// TODO(bmizerany): This could take the URL string landed on and get all the
	// info from there. This whould be nicer if Manager kept the success/cancel
	// URLs, and we removed them from SubscribeParams?
	if sessionID == "" {
		return "", errors.New("session ID is required")
	}

	c := m.client()

	for range backoff.Loop(ctx, 1*time.Second) {
		sess, err := c.V1CheckoutSessions.Retrieve(ctx, sessionID, nil)
		if err != nil {
			if !isRetryable(err) {
				return "", fmt.Errorf("failed to retrieve checkout session: %w", err)
			}
			m.slog().ErrorContext(ctx, "retrieve checkout session", "error", err)
			continue
		}

		// Verify the session status is complete
		if sess.Status != stripe.CheckoutSessionStatusComplete {
			m.slog().ErrorContext(ctx, "checkout session not complete", "status", sess.Status)
			return "", fmt.Errorf("%s: status: %q", ErrIncomplete, sess.Status)
		}

		// Verify payment status - for subscriptions with trials, this may be "no_payment_required"
		switch sess.PaymentStatus {
		case stripe.CheckoutSessionPaymentStatusPaid, stripe.CheckoutSessionPaymentStatusNoPaymentRequired:
			// Valid payment statuses
			if sess.Customer == nil || sess.Customer.ID == "" {
				return "", errors.New("checkout session has no customer")
			}
			return sess.Customer.ID, nil
		default:
			return "", fmt.Errorf("checkout session payment not confirmed: payment_status=%s", sess.PaymentStatus)
		}

	}

	return "", ctx.Err()
}

// PortalSession creates a Stripe billing portal session for a customer.
// The portal allows customers to manage their subscription, update payment methods,
// and view billing history. Returns the portal URL to redirect the customer to.
func (m *Manager) PortalSession(ctx context.Context, billingID, returnURL string) (portalURL string, _ error) {
	if billingID == "" {
		return "", errors.New("billing ID is required")
	}

	c := m.client()

	params := &stripe.BillingPortalSessionCreateParams{
		Customer:  &billingID,
		ReturnURL: &returnURL,
	}

	for err := range backoff.Loop(ctx, 1*time.Second) {
		if err != nil {
			return "", err
		}

		sess, err := c.V1BillingPortalSessions.Create(ctx, params)
		if err != nil {
			if !isRetryable(err) {
				m.slog().WarnContext(ctx, "create billing portal session", "error", err)
				return "", fmt.Errorf("failed to create billing portal session: %w", err)
			}
			m.slog().ErrorContext(ctx, "create billing portal session", "error", err)
			continue
		}
		return sess.URL, nil
	}

	return "", ctx.Err()
}
