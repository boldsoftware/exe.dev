// Package billing provides subscription and payment management for exe.dev accounts.
package billing

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
	"exe.dev/logging"
	"exe.dev/sqlite"
	"github.com/stripe/stripe-go/v85"
	"tailscale.com/syncs"
	"tailscale.com/types/result"
)

// Errors
var (
	ErrNotFound   = errors.New("not found")
	ErrIncomplete = errors.New("incomplete")
)

// MakeCustomerDashboardURL returns the Stripe dashboard URL for a customer.
func MakeCustomerDashboardURL(billingID string) string {
	return "https://dashboard.stripe.com/customers/" + billingID
}

var stripeKey = os.Getenv("STRIPE_SECRET_KEY")

// Gift ID prefix constants.
//
// Callers construct a gift_id as "<prefix>:<account_id>:<detail>".
// The unique index on gift_id provides idempotency — a duplicate
// gift_id is silently ignored (INSERT OR IGNORE).
//
// For signup gifts the format is "signup:<account_id>" (no detail
// suffix), which ensures at most one signup bonus per account.
const (
	GiftPrefixDebug  = "debug_gift"
	GiftPrefixSignup = "signup"
	GiftPrefixSSH    = "ssh_gift"
)

const (
	DefaultPlan               = "individual"
	productIndividualID       = "prod_individual"
	productIndividual         = "Individual"
	productIndividualMediumID = "prod_individual_medium"
	productIndividualMedium   = "Individual Plan (Medium)"
	productIndividualLargeID  = "prod_individual_large"
	productIndividualLarge    = "Individual Plan (Large)"
	productIndividualXlargeID = "prod_individual_xlarge"
	productIndividualXlarge   = "Individual Plan (XLarge)"
	productTeamID             = "prod_team"
	productTeam               = "Team"

	// TestAPIKey is the Stripe test API key. It is safe to check into source code
	// and easy to revoke should someone want to spam our test account.
	TestAPIKey = "sk_test_51SzRtTKBUWL0n1QN0OSXVllXJLOeM2JfcFDRLNJHeMpKVTgjaif5cDBhZ1jIcCv8cZFRoMb1YBnbYeXedaD1oQ3w00tOHZd9cF"
)

type managedPrice struct {
	lookupKey          string
	currency           stripe.Currency
	unitAmount         int64
	interval           stripe.PriceRecurringInterval
	productID          string
	productName        string
	productDescription string
	metered            bool
	usageType          string
}

var managedPrices = []managedPrice{
	{
		lookupKey:          DefaultPlan,
		currency:           stripe.CurrencyUSD,
		unitAmount:         2000,
		interval:           stripe.PriceRecurringIntervalMonth,
		productID:          productIndividualID,
		productName:        productIndividual,
		productDescription: "2 vCPUs, 8 GB memory",
	},
	{
		lookupKey:          "individual:annual:20260106",
		currency:           stripe.CurrencyUSD,
		unitAmount:         20000,
		interval:           stripe.PriceRecurringIntervalYear,
		productID:          productIndividualID,
		productName:        productIndividual,
		productDescription: "2 vCPUs, 8 GB memory",
	},
	{
		lookupKey:          "individual:medium:monthly:20160102",
		currency:           stripe.CurrencyUSD,
		unitAmount:         4000,
		interval:           stripe.PriceRecurringIntervalMonth,
		productID:          productIndividualMediumID,
		productName:        productIndividualMedium,
		productDescription: "4 vCPUs, 16 GB memory",
	},
	{
		lookupKey:          "individual:large:monthly:20160102",
		currency:           stripe.CurrencyUSD,
		unitAmount:         8000,
		interval:           stripe.PriceRecurringIntervalMonth,
		productID:          productIndividualLargeID,
		productName:        productIndividualLarge,
		productDescription: "8 vCPUs, 32 GB memory",
	},
	{
		lookupKey:          "individual:xlarge:monthly:20160102",
		currency:           stripe.CurrencyUSD,
		unitAmount:         16000,
		interval:           stripe.PriceRecurringIntervalMonth,
		productID:          productIndividualXlargeID,
		productName:        productIndividualXlarge,
		productDescription: "16 vCPUs, 64 GB memory",
	},
	{
		lookupKey:          "individual:usage-disk:20260106",
		currency:           stripe.CurrencyUSD,
		unitAmount:         8, // $0.08 in cents
		productID:          productIndividualID,
		productName:        productIndividual,
		productDescription: "2 vCPUs, 8 GB memory",
		metered:            true,
		usageType:          "metered",
	},
	{
		lookupKey:          "individual:usage-bandwidth:20260106",
		currency:           stripe.CurrencyUSD,
		unitAmount:         7, // $0.07 in cents
		productID:          productIndividualID,
		productName:        productIndividual,
		productDescription: "2 vCPUs, 8 GB memory",
		metered:            true,
		usageType:          "metered",
	},
	{
		lookupKey:          "team:monthly:20260106",
		currency:           stripe.CurrencyUSD,
		unitAmount:         2500,
		interval:           stripe.PriceRecurringIntervalMonth,
		productID:          productTeamID,
		productName:        productTeam,
		productDescription: "Team plan",
	},
	{
		lookupKey:          "team:annual:20260106",
		currency:           stripe.CurrencyUSD,
		unitAmount:         25000,
		interval:           stripe.PriceRecurringIntervalYear,
		productID:          productTeamID,
		productName:        productTeam,
		productDescription: "Team plan",
	},
	{
		lookupKey:          "team:usage-disk:20260106",
		currency:           stripe.CurrencyUSD,
		unitAmount:         8,
		productID:          productTeamID,
		productName:        productTeam,
		productDescription: "Team plan",
		metered:            true,
		usageType:          "metered",
	},
	{
		lookupKey:          "team:usage-bandwidth:20260106",
		currency:           stripe.CurrencyUSD,
		unitAmount:         7,
		productID:          productTeamID,
		productName:        productTeam,
		productDescription: "Team plan",
		metered:            true,
		usageType:          "metered",
	},
}

// Manager handles billing operations.
type Manager struct {
	// Client is the Stripe client to use for requests.
	// If nil, a new client is created using the STRIPE_SECRET_KEY
	// environment variable and Stripe's default API URL.
	// This field is primarily for testing.
	Client *stripe.Client

	// DB is the database connection for credit ledger operations.
	DB *sqlite.DB

	Logger        *slog.Logger
	WebhookSecret string
	SlackFeed     *logging.SlackFeed

	// StripeAPIURL overrides the Stripe API base URL.
	// When non-empty, requests go to this URL instead of https://api.stripe.com.
	// Used in e1e tests to route through an httprr proxy.
	StripeAPIURL string

	priceIDCache syncs.Map[string, func() result.Of[string]]

	// OnPlanDowngrade is called when an account's plan is downgraded to basic
	// (e.g., subscription canceled, payment failed, trial expired).
	// The callback receives the account ID. If nil, no callback is made.
	OnPlanDowngrade func(ctx context.Context, accountID string)

	// testClockID is the ID of the Stripe test clock to use for requests
	// that need to be associated with the clock. This field is primarily
	// for testing and owned by helpers_test.go
	testClockID string
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
	if m.StripeAPIURL != "" {
		backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
			URL: &m.StripeAPIURL,
		})
		return stripe.NewClient(stripeKey, stripe.WithBackends(backends))
	}
	return stripe.NewClient(stripeKey)
}

// InstallPrices creates the Stripe products and prices used by billing if they
// do not already exist.
func (m *Manager) InstallPrices(ctx context.Context) error {
	c := m.client()
	for _, p := range managedPrices {
		if p.metered {
			continue // TODO: metered prices require Stripe Meters as of API version 2025-03-31.basil
		}
		if err := m.ensureProduct(ctx, c, p.productID, p.productName, p.productDescription); err != nil {
			return err
		}

		found := false
		for got, err := range c.V1Prices.List(ctx, &stripe.PriceListParams{
			LookupKeys: []*string{new(p.lookupKey)},
			Active:     new(true),
		}).All(ctx) {
			if err != nil {
				return fmt.Errorf("list active price %q: %w", p.lookupKey, err)
			}
			found = true
			m.slog().InfoContext(ctx, "billing price already installed",
				"lookup_key", p.lookupKey,
				"price_id", got.ID,
				"product_id", p.productID,
			)
			break
		}
		if found {
			continue
		}

		params := &stripe.PriceCreateParams{
			LookupKey:  new(p.lookupKey),
			Nickname:   new(p.lookupKey),
			Currency:   new(string(p.currency)),
			UnitAmount: new(p.unitAmount),
			Product:    new(p.productID),
		}

		if p.metered {
			params.Recurring = &stripe.PriceCreateRecurringParams{
				UsageType: &p.usageType,
			}
		} else {
			recurringInterval := string(p.interval)
			params.Recurring = &stripe.PriceCreateRecurringParams{
				Interval: &recurringInterval,
			}
		}

		created, err := c.V1Prices.Create(ctx, params)
		if err != nil {
			return fmt.Errorf("create price %q: %w", p.lookupKey, err)
		}
		m.priceIDCache.Delete(p.lookupKey)

		var requestID string
		if created.LastResponse != nil {
			requestID = created.LastResponse.RequestID
		}
		m.slog().InfoContext(ctx, "billing price installed",
			"stripe_request_id", requestID,
			"lookup_key", p.lookupKey,
			"price_id", created.ID,
			"product_id", p.productID,
		)
	}
	return nil
}

func (m *Manager) ensureProduct(ctx context.Context, c *stripe.Client, id, name, description string) error {
	product, err := c.V1Products.Retrieve(ctx, id, nil)
	if err == nil {
		var requestID string
		if product.LastResponse != nil {
			requestID = product.LastResponse.RequestID
		}
		m.slog().InfoContext(ctx, "billing product already installed",
			"stripe_request_id", requestID,
			"product_id", id,
		)
		return nil
	}
	if !isNotExists(err) {
		return fmt.Errorf("retrieve product %q: %w", id, err)
	}

	params := &stripe.ProductCreateParams{
		ID:   new(id),
		Name: new(name),
	}
	if description != "" {
		params.Description = new(description)
	}
	created, err := c.V1Products.Create(ctx, params)
	if isExists(err) {
		m.slog().InfoContext(ctx, "billing product already installed",
			"product_id", id,
		)
		return nil
	}
	if err != nil {
		return fmt.Errorf("create product %q: %w", id, err)
	}

	var requestID string
	if created.LastResponse != nil {
		requestID = created.LastResponse.RequestID
	}
	m.slog().InfoContext(ctx, "billing product installed",
		"stripe_request_id", requestID,
		"product_id", id,
	)
	return nil
}

func (m *Manager) upsertCustomer(ctx context.Context, billingID, email string) error {
	c := m.client()
	params := &stripe.CustomerCreateParams{
		Email: &email,
	}
	params.AddExtra("id", billingID)
	if m.testClockID != "" {
		m.slog().DebugContext(ctx, "using test clock for customer creation",
			"billing_id", billingID,
			"test_clock_id", m.testClockID,
		)
		params.TestClock = &m.testClockID
	}

	customer, err := c.V1Customers.Create(ctx, params)

	if isExists(err) {
		m.slog().InfoContext(ctx, "customer already exists",
			withRequestID(err),
			"billing_id", billingID,
		)
		return nil
	}
	if err != nil {
		m.slog().ErrorContext(ctx, "failed to create customer",
			withRequestID(err),
			"billing_id", billingID,
			"error", err,
		)
		return err
	}

	var requestID string
	if customer.LastResponse != nil {
		requestID = customer.LastResponse.RequestID
	}
	m.slog().InfoContext(ctx, "customer created",
		"stripe_request_id", requestID,
		"billing_id", billingID,
		"email", email,
	)
	return nil
}

func isExists(err error) bool {
	stripeErr, ok := errors.AsType[*stripe.Error](err)
	return ok && stripeErr.Code == stripe.ErrorCodeResourceAlreadyExists
}

func isNotExists(err error) bool {
	stripeErr, ok := errors.AsType[*stripe.Error](err)
	return ok && stripeErr.Code == stripe.ErrorCodeResourceMissing
}

func isRateLimited(err error) bool {
	stripeErr, ok := errors.AsType[*stripe.Error](err)
	return ok && stripeErr.HTTPStatusCode == 429
}

// Subscribe generates a payment link for subscribing an account to a plan.
//
// It returns a payment link URL for the account to complete the subscription.
func (m *Manager) Subscribe(ctx context.Context, billingID string, p *SubscribeParams) (paymentLink string, _ error) {
	if p == nil {
		p = &SubscribeParams{}
	}

	c := m.client()

	lookupKey := cmp.Or(p.Plan, DefaultPlan)
	priceID, err := m.lookupPriceIDCached(ctx, lookupKey)
	if err != nil {
		return "", fmt.Errorf("lookup price %q: %w", lookupKey, err)
	}

	err = m.upsertCustomer(ctx, billingID, p.Email)
	if err != nil {
		return "", fmt.Errorf("upsert customer: %w", err)
	}

	hasSubscription, err := m.hasActiveSubscription(ctx, c, billingID)
	if err != nil {
		return "", fmt.Errorf("check active subscription: %w", err)
	}
	if hasSubscription {
		if p.RedirectToPortal {
			returnURL := cmp.Or(p.PortalReturnURL, p.SuccessURL)
			return m.openPortal(ctx, billingID, returnURL)
		} else {
			return p.SuccessURL, nil
		}
	}

	params := &stripe.CheckoutSessionCreateParams{
		Customer: &billingID,
		Mode:     new("subscription"),
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{
			{
				Price:    &priceID,
				Quantity: new(int64(1)),
			},
		},
		SuccessURL: &p.SuccessURL,
		CancelURL:  &p.CancelURL,
	}

	if !p.TrialEnd.IsZero() {
		params.SubscriptionData = &stripe.CheckoutSessionCreateSubscriptionDataParams{
			TrialEnd: new(p.TrialEnd.Unix()),
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

// activeSubscriptionID returns the ID of the customer's active subscription,
// or "" if there isn't one.
func (m *Manager) activeSubscriptionID(ctx context.Context, c *stripe.Client, customerID string) (string, error) {
	params := &stripe.SubscriptionListParams{
		Customer: &customerID,
		Status:   new("all"),
	}
	for sub, err := range c.V1Subscriptions.List(ctx, params).All(ctx) {
		if err != nil {
			return "", err
		}
		switch sub.Status {
		case stripe.SubscriptionStatusActive,
			stripe.SubscriptionStatusTrialing,
			stripe.SubscriptionStatusPastDue:
			return sub.ID, nil
		}
	}
	return "", nil
}

func (m *Manager) hasActiveSubscription(ctx context.Context, c *stripe.Client, customerID string) (bool, error) {
	params := &stripe.SubscriptionListParams{
		Customer: &customerID,
		Status:   new("all"),
	}
	for sub, err := range c.V1Subscriptions.List(ctx, params).All(ctx) {
		if err != nil {
			m.slog().ErrorContext(ctx, "failed to list subscriptions",
				withRequestID(err),
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

func withRequestID(err error) slog.Attr {
	var requestID string
	if stripeErr, ok := errors.AsType[*stripe.Error](err); ok && stripeErr.LastResponse != nil {
		requestID = stripeErr.LastResponse.RequestID
	}
	return slog.String("stripe_request_id", requestID)
}

// lookupPriceID finds the price ID for a given lookup key, caching results.
func (m *Manager) lookupPriceID(ctx context.Context, lookupKey string) (string, error) {
	for price, err := range m.client().V1Prices.List(ctx, &stripe.PriceListParams{
		LookupKeys: []*string{&lookupKey},
		Active:     new(true),
	}).All(ctx) {
		if err != nil {
			return "", err
		}
		return price.ID, nil
	}
	return "", fmt.Errorf("no active price found with lookup key %q", lookupKey)
}

func (m *Manager) lookupPriceIDCached(ctx context.Context, lookupKey string) (string, error) {
	res, _ := m.priceIDCache.LoadOrInit(lookupKey, func() func() result.Of[string] {
		return sync.OnceValue(func() result.Of[string] {
			priceID, err := m.lookupPriceID(ctx, lookupKey)
			if err != nil {
				m.priceIDCache.Delete(lookupKey) // enable retries
				return result.Error[string](err)
			}
			return result.Value(priceID)
		})
	})
	return res().Value()
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

// OpenPortalToUpdateSubscription creates a billing portal session that deep-links
// to the subscription update flow for the customer's active subscription.
// Returns the generic portal if no active subscription is found.
func (m *Manager) OpenPortalToUpdateSubscription(ctx context.Context, billingID, returnURL string) (string, error) {
	c := m.client()
	subID, err := m.activeSubscriptionID(ctx, c, billingID)
	if err != nil || subID == "" {
		return m.openPortal(ctx, billingID, returnURL)
	}

	flowType := string(stripe.BillingPortalSessionFlowTypeSubscriptionUpdate)
	params := &stripe.BillingPortalSessionCreateParams{
		Customer:  &billingID,
		ReturnURL: &returnURL,
		FlowData: &stripe.BillingPortalSessionCreateFlowDataParams{
			Type: &flowType,
			SubscriptionUpdate: &stripe.BillingPortalSessionCreateFlowDataSubscriptionUpdateParams{
				Subscription: &subID,
			},
			AfterCompletion: &stripe.BillingPortalSessionCreateFlowDataAfterCompletionParams{
				Type:     stripe.String(string(stripe.BillingPortalSessionFlowAfterCompletionTypeRedirect)),
				Redirect: &stripe.BillingPortalSessionCreateFlowDataAfterCompletionRedirectParams{ReturnURL: &returnURL},
			},
		},
	}
	sess, err := c.V1BillingPortalSessions.Create(ctx, params)
	if err != nil {
		// Fall back to generic portal if flow_data fails (e.g. portal config doesn't allow updates).
		m.slog().WarnContext(ctx, "failed to create subscription update portal, falling back to generic",
			"error", err, "billing_id", billingID)
		return m.openPortal(ctx, billingID, returnURL)
	}
	return sess.URL, nil
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

// SyncSubscriptions runs a single blocking sync of subscription state changes
// from Stripe into billing_events and returns the next cursor time.
func (m *Manager) SyncSubscriptions(ctx context.Context, since time.Time) (time.Time, error) {
	c := m.client()
	sinceUnix := since.Unix()

	params := &stripe.EventListParams{
		Types: []*string{
			new("customer.subscription.created"),
			new("customer.subscription.updated"),
			new("customer.subscription.deleted"),
		},
		CreatedRange: &stripe.RangeQueryParams{
			GreaterThan: sinceUnix,
		},
	}

	maxEventAt := sinceUnix
	for event, err := range c.V1Events.List(ctx, params).All(ctx) {
		if err != nil {
			if isRateLimited(err) {
				m.slog().WarnContext(ctx, "rate limited listing subscription events",
					withRequestID(err),
					"error", err,
				)
			} else {
				m.slog().ErrorContext(ctx, "error listing subscription events",
					withRequestID(err),
					"error", err,
				)
			}
			return since, fmt.Errorf("list subscription events: %w", err)
		}

		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
			continue
		}
		if sub.Customer == nil || sub.Customer.ID == "" {
			continue
		}

		eventType, ok := subscriptionEventType(string(event.Type), sub.Status)
		if !ok {
			continue
		}

		eventAt := time.Unix(event.Created, 0)
		maxEventAt = max(maxEventAt, eventAt.Unix())
		stripeEventID := event.ID
		err := exedb.WithTx1(m.DB, ctx, (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
			AccountID:     sub.Customer.ID,
			EventType:     eventType,
			EventAt:       eventAt,
			StripeEventID: &stripeEventID,
		})
		if err != nil {
			return since, fmt.Errorf("insert billing event: %w", err)
		}

		// Update account_plans to reflect the subscription change.
		// On "active": close current plan and insert "individual".
		// On "canceled": close current plan and insert "basic".
		// For trialing subscriptions, also record when the trial ends.
		var trialEnd *time.Time
		if sub.TrialEnd > 0 && sub.Status == stripe.SubscriptionStatusTrialing {
			t := time.Unix(sub.TrialEnd, 0).UTC()
			trialEnd = &t
		}
		if err := m.syncAccountPlan(ctx, sub.Customer.ID, eventType, eventAt, trialEnd, &sub); err != nil {
			m.slog().WarnContext(ctx, "failed to sync account plan",
				"account_id", sub.Customer.ID,
				"event_type", eventType,
				"error", err,
			)
		}

		if sub.Status == stripe.SubscriptionStatusTrialing && m.SlackFeed != nil {
			m.SlackFeed.TrialStarted(ctx, sub.Customer.ID)
		}
	}

	if maxEventAt == sinceUnix {
		return since, nil
	}
	return time.Unix(maxEventAt, 0).UTC(), nil
}

// syncAccountPlan updates account_plans when a subscription event is processed.
// "active" -> close current plan, insert plan ID derived from the subscription's product.
// "canceled" -> close current plan, insert versioned "basic" plan ID.
//
// When trialEnd is non-nil (trialing subscription), trial_expires_at is written
// so we can distinguish trialing users from paid ones in queries.
//
// Versioned plan IDs use the format "{plan}:{interval}:{YYYYMMDD}".
func (m *Manager) syncAccountPlan(ctx context.Context, accountID, eventType string, eventAt time.Time, trialEnd *time.Time, sub *stripe.Subscription) error {
	var newPlanID string
	if eventType != "active" {
		newPlanID = plan.ID(plan.CategoryBasic)
	} else {
		// Resolve the tier from the subscription's price lookup key.
		newPlanID = plan.TierIDFromStripePriceKey(subscriptionLookupKey(sub))
	}

	// Skip if the plan hasn't changed — avoids duplicate rows from poller replays.
	activePlan, err := exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check current plan: %w", err)
	}
	if err == nil && activePlan.PlanID == newPlanID {
		return nil
	}
	// Skip stale events: if the current plan was set by a newer event,
	// don't let an older event overwrite it. This prevents the 60-day
	// replay from applying old cancellation events to accounts that have
	// since resubscribed (different subscription ID, newer timestamp).
	if err == nil && eventAt.Before(activePlan.StartedAt) {
		return nil
	}

	changedBy := "stripe:event"

	if err := exedb.WithTx(m.DB, ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
			AccountID:      accountID,
			PlanID:         newPlanID,
			At:             eventAt,
			TrialExpiresAt: trialEnd,
			ChangedBy:      changedBy,
		})
	}); err != nil {
		return fmt.Errorf("sync account plan: %w", err)
	}

	// TODO: OnPlanDowngrade is disabled. The subscription poller replays
	// 60 days of Stripe events on every deploy, and stale cancellation
	// events from old subscriptions can fire the downgrade callback for
	// customers who have a current active subscription (different sub ID).
	// This caused 81 paying users' VMs to be stopped on 2026-03-29.
	// Re-enable once syncAccountPlan is subscription-aware or the poller
	// persists its cursor.
	// if basePlan == plan.CategoryBasic && m.OnPlanDowngrade != nil {
	// 	m.OnPlanDowngrade(ctx, accountID)
	// }

	// Notify Slack when an individual plan tier changes (e.g. Small → Medium).
	if m.SlackFeed != nil && err == nil {
		oldTier, oldErr := plan.GetTierByID(activePlan.PlanID)
		newTier, newErr := plan.GetTierByID(newPlanID)
		if oldErr == nil && newErr == nil &&
			oldTier.Category == plan.CategoryIndividual &&
			newTier.Category == plan.CategoryIndividual &&
			oldTier.ID != newTier.ID {
			direction := "upgrade"
			if newTier.MonthlyPriceCents < oldTier.MonthlyPriceCents {
				direction = "downgrade"
			}
			email := m.emailForAccount(ctx, accountID)
			m.SlackFeed.PlanTierChanged(ctx, email, oldTier.Name, newTier.Name, direction)
		}
	}

	return nil
}

// emailForAccount resolves the email address for a billing account ID.
// Returns the account ID as a fallback if resolution fails.
func (m *Manager) emailForAccount(ctx context.Context, accountID string) string {
	userID, err := exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).GetUserIDByAccountID, accountID)
	if err != nil {
		return accountID
	}
	email, err := exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).GetEmailByUserID, userID)
	if err != nil {
		return accountID
	}
	return email
}

// BillingPeriod holds the start and end of a billing period.
type BillingPeriod struct {
	Start time.Time
	End   time.Time
}

// CurrentBillingPeriod returns the current billing period for an account
// by looking up the active Stripe subscription.
// Returns nil if there is no active subscription.
func (m *Manager) CurrentBillingPeriod(ctx context.Context, customerID string) (*BillingPeriod, error) {
	c := m.client()
	params := &stripe.SubscriptionListParams{
		Customer: &customerID,
		Status:   stripe.String(string(stripe.SubscriptionStatusActive)),
	}
	for sub, err := range c.V1Subscriptions.List(ctx, params).All(ctx) {
		if err != nil {
			return nil, fmt.Errorf("list subscriptions: %w", err)
		}
		// In Stripe v85, period lives on subscription items, not the subscription.
		if sub.Items != nil {
			for _, item := range sub.Items.Data {
				if item.CurrentPeriodStart > 0 {
					return &BillingPeriod{
						Start: time.Unix(item.CurrentPeriodStart, 0).UTC(),
						End:   time.Unix(item.CurrentPeriodEnd, 0).UTC(),
					}, nil
				}
			}
		}
	}
	return nil, nil
}

// SubscriptionEvents returns subscription events for an account, ordered by time.
func (m *Manager) SubscriptionEvents(ctx context.Context, billingID string) ([]SubscriptionEvent, error) {
	rows, err := exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).ListSubscriptionEvents, billingID)
	if err != nil {
		return nil, fmt.Errorf("query billing events: %w", err)
	}
	var events []SubscriptionEvent
	for _, r := range rows {
		events = append(events, SubscriptionEvent{
			AccountID: r.AccountID,
			EventType: r.EventType,
			EventAt:   r.EventAt,
		})
	}
	return events, nil
}

func subscriptionLookupKey(sub *stripe.Subscription) string {
	if sub == nil || sub.Items == nil || len(sub.Items.Data) == 0 {
		return ""
	}
	for _, item := range sub.Items.Data {
		if item.Price != nil && item.Price.LookupKey != "" && item.Price.Recurring != nil && item.Price.Recurring.UsageType != stripe.PriceRecurringUsageTypeMetered {
			return item.Price.LookupKey
		}
	}
	return ""
}

func subscriptionEventType(eventType string, status stripe.SubscriptionStatus) (string, bool) {
	switch eventType {
	case "customer.subscription.deleted":
		return "canceled", true
	case "customer.subscription.created":
		// Only record created events for active subscriptions.
		switch status {
		case stripe.SubscriptionStatusActive, stripe.SubscriptionStatusTrialing:
			return "active", true
		default:
			return "", false
		}
	case "customer.subscription.updated":
		switch status {
		case stripe.SubscriptionStatusActive, stripe.SubscriptionStatusTrialing:
			return "active", true
		case stripe.SubscriptionStatusCanceled,
			stripe.SubscriptionStatusIncomplete,
			stripe.SubscriptionStatusIncompleteExpired,
			stripe.SubscriptionStatusUnpaid:
			return "canceled", true
		case stripe.SubscriptionStatusPastDue:
			// Past due still has access until it becomes unpaid.
			return "active", true
		default:
			return "", false
		}
	default:
		return "", false
	}
}

// PlanVersionGroup represents a group of subscribers on the same plan_id version.
type PlanVersionGroup struct {
	PlanID   string
	BasePlan string
	Interval string
	Version  string
	Count    int
}

// ListPlanVersions returns all active plan versions with subscriber counts,
// grouped by the full plan_id value in account_plans.
func (m *Manager) ListPlanVersions(ctx context.Context) ([]PlanVersionGroup, error) {
	rows, err := exedb.WithRxRes0(m.DB, ctx, (*exedb.Queries).ListPlanVersionCounts)
	if err != nil {
		return nil, fmt.Errorf("list plan versions: %w", err)
	}
	var groups []PlanVersionGroup
	for _, row := range rows {
		g := PlanVersionGroup{
			PlanID: row.PlanID,
			Count:  int(row.Cnt),
		}
		p, i, v := plan.ParseID(g.PlanID)
		g.BasePlan, g.Interval, g.Version = string(p), i, v
		groups = append(groups, g)
	}
	return groups, nil
}

// ListSubscribersByPlan returns all account IDs with the given plan_id.
func (m *Manager) ListSubscribersByPlan(ctx context.Context, planID string) ([]string, error) {
	return exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).ListActiveSubscribersByPlanID, planID)
}

// MigratePlan batch-migrates all active subscribers from one plan_id
// to another, closing the old plan and inserting the new one within a single
// transaction. Returns the number of accounts migrated.
func (m *Manager) MigratePlan(ctx context.Context, fromPlanID, toPlanID string) (int, error) {
	now := time.Now()
	changedBy := fmt.Sprintf("admin:migrate:%s->%s", fromPlanID, toPlanID)

	// Collect account IDs first.
	accountIDs, err := m.ListSubscribersByPlan(ctx, fromPlanID)
	if err != nil {
		return 0, fmt.Errorf("select accounts to migrate: %w", err)
	}
	if len(accountIDs) == 0 {
		return 0, nil
	}

	// Close all old plans and insert new ones in a single tx.
	if err := exedb.WithTx(m.DB, ctx, func(ctx context.Context, q *exedb.Queries) error {
		if err := q.CloseAccountPlansByPlanID(ctx, exedb.CloseAccountPlansByPlanIDParams{
			PlanID:  fromPlanID,
			EndedAt: &now,
		}); err != nil {
			return fmt.Errorf("close old plans: %w", err)
		}
		for _, accountID := range accountIDs {
			if err := q.InsertAccountPlanMigration(ctx, exedb.InsertAccountPlanMigrationParams{
				AccountID: accountID,
				PlanID:    toPlanID,
				StartedAt: now,
				ChangedBy: &changedBy,
			}); err != nil {
				return fmt.Errorf("insert new plan for %s: %w", accountID, err)
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}

	return len(accountIDs), nil
}
