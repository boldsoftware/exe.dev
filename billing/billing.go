// Package billing provides subscription and payment management for exe.dev accounts.
package billing

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"os"
	"sync"
	"time"

	"exe.dev/billing/tender"
	"exe.dev/errorz"
	"exe.dev/sqlite"
	"github.com/stripe/stripe-go/v82"
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

const (
	DefaultPlan         = "individual"
	productIndividualID = "prod_individual"
	productIndividual   = "Individual"

	// TestAPIKey is the Stripe test API key. It is safe to check into source code
	// and easy to revoke should someone want to spam our test account.
	TestAPIKey = "sk_test_51SzRtTKBUWL0n1QN0OSXVllXJLOeM2JfcFDRLNJHeMpKVTgjaif5cDBhZ1jIcCv8cZFRoMb1YBnbYeXedaD1oQ3w00tOHZd9cF"
)

type managedPrice struct {
	lookupKey   string
	currency    stripe.Currency
	unitAmount  int64
	interval    stripe.PriceRecurringInterval
	productID   string
	productName string
}

var managedPrices = []managedPrice{
	{
		lookupKey:   DefaultPlan,
		currency:    stripe.CurrencyUSD,
		unitAmount:  2000,
		interval:    stripe.PriceRecurringIntervalMonth,
		productID:   productIndividualID,
		productName: productIndividual,
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

	Logger *slog.Logger

	priceIDCache syncs.Map[string, func() result.Of[string]]

	// testClockID is the ID of the Stripe test clock to use for requests
	// that need to be associated with the clock. This field is primarily
	// for testing and owned by test_test.go
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
	return stripe.NewClient(stripeKey)
}

// InstallPrices creates the Stripe products and prices used by billing if they
// do not already exist.
func (m *Manager) InstallPrices(ctx context.Context) error {
	c := m.client()
	for _, p := range managedPrices {
		if err := m.ensureProduct(ctx, c, p.productID, p.productName); err != nil {
			return err
		}

		found := false
		for got, err := range c.V1Prices.List(ctx, &stripe.PriceListParams{
			LookupKeys: []*string{new(p.lookupKey)},
			Active:     new(true),
		}) {
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

		recurringInterval := string(p.interval)
		created, err := c.V1Prices.Create(ctx, &stripe.PriceCreateParams{
			LookupKey:  new(p.lookupKey),
			Currency:   new(string(p.currency)),
			UnitAmount: new(p.unitAmount),
			Product:    new(p.productID),
			Recurring: &stripe.PriceCreateRecurringParams{
				Interval: &recurringInterval,
			},
		})
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

func (m *Manager) ensureProduct(ctx context.Context, c *stripe.Client, id, name string) error {
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

	created, err := c.V1Products.Create(ctx, &stripe.ProductCreateParams{
		ID:   new(id),
		Name: new(name),
	})
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
	stripeErr, ok := errorz.AsType[*stripe.Error](err)
	return ok && stripeErr.Code == stripe.ErrorCodeResourceAlreadyExists
}

func isNotExists(err error) bool {
	stripeErr, ok := errorz.AsType[*stripe.Error](err)
	return ok && stripeErr.Code == stripe.ErrorCodeResourceMissing
}

func isRateLimited(err error) bool {
	stripeErr, ok := errorz.AsType[*stripe.Error](err)
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

	plan := cmp.Or(p.Plan, DefaultPlan)
	priceID, err := m.lookupPriceIDCached(ctx, plan)
	if err != nil {
		return "", fmt.Errorf("lookup price %q: %w", plan, err)
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

func (m *Manager) hasActiveSubscription(ctx context.Context, c *stripe.Client, customerID string) (bool, error) {
	params := &stripe.SubscriptionListParams{
		Customer: &customerID,
		Status:   new("all"),
	}
	for sub, err := range c.V1Subscriptions.List(ctx, params) {
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
	if stripeErr, ok := errorz.AsType[*stripe.Error](err); ok && stripeErr.LastResponse != nil {
		requestID = stripeErr.LastResponse.RequestID
	}
	return slog.String("stripe_request_id", requestID)
}

// lookupPriceID finds the price ID for a given lookup key, caching results.
func (m *Manager) lookupPriceID(ctx context.Context, lookupKey string) (string, error) {
	for price, err := range m.client().V1Prices.List(ctx, &stripe.PriceListParams{
		LookupKeys: []*string{&lookupKey},
		Active:     new(true),
	}) {
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

	const q = `
		INSERT OR IGNORE INTO billing_events (account_id, event_type, event_at)
		VALUES (@accountID, @eventType, @eventAt)
	`

	maxEventAt := sinceUnix
	for event, err := range c.V1Events.List(ctx, params) {
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
		err := m.exec(ctx, q,
			sql.Named("accountID", sub.Customer.ID),
			sql.Named("eventType", eventType),
			sql.Named("eventAt", sqlite.NormalizeTime(eventAt)),
		)
		if err != nil {
			return since, fmt.Errorf("insert billing event: %w", err)
		}
	}

	if maxEventAt == sinceUnix {
		return since, nil
	}
	return time.Unix(maxEventAt, 0).UTC(), nil
}

// SubscriptionEvents returns subscription events for an account, ordered by time.
func (m *Manager) SubscriptionEvents(ctx context.Context, billingID string) ([]SubscriptionEvent, error) {
	const q = `
		SELECT account_id, event_type, event_at
		FROM billing_events
		WHERE account_id = @accountID
		ORDER BY event_at ASC
	`
	var events []SubscriptionEvent
	for rows, err := range m.query(ctx, q, sql.Named("accountID", billingID)) {
		if err != nil {
			return nil, fmt.Errorf("query billing events: %w", err)
		}
		var event SubscriptionEvent
		if err := rows.Scan(&event.AccountID, &event.EventType, &event.EventAt); err != nil {
			return nil, fmt.Errorf("scan billing event: %w", err)
		}
		events = append(events, event)
	}
	return events, nil
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

func (m *Manager) UseCredits(ctx context.Context, billingID string, quantity int, unitPrice tender.Value) (remaining tender.Value, _ error) {
	const q = `
		-- Insert a new credit deduction for the current hour and credit type,
		-- or update the existing one if it already exists,
		-- and return the new total balance for the account after the deduction for all types.
		INSERT INTO billing_credits (account_id, amount, hour_bucket, credit_type)
		VALUES (@accountID, @amount, @hourBucket, @creditType)
		ON CONFLICT(account_id, hour_bucket, credit_type) DO
			UPDATE SET amount = billing_credits.amount + excluded.amount
		RETURNING (
			-- Return the new balance after deduction
			SELECT CAST(COALESCE(SUM(amount), 0) AS INTEGER)
			FROM billing_credits
			WHERE account_id = @accountID
		)
	`

	for rows, err := range m.query(ctx, q,
		sql.Named("accountID", billingID),
		sql.Named("amount", unitPrice.Times(quantity)),
		sql.Named("hourBucket", time.Now().UTC().Truncate(time.Hour).Format("2006-01-02 15:00:00")),
		sql.Named("creditType", "usage"),
	) {
		if err != nil {
			return tender.Zero(), err
		}
		var rem tender.Value
		if err := rows.Scan(&rem); err != nil {
			return tender.Zero(), err
		}
		return rem, nil
	}

	return tender.Zero(), errors.New("UseCredits: query returned no rows (but should have)")
}

// BuyCreditsParams contains the parameters for purchasing credits.
type BuyCreditsParams struct {
	// Email is the customer's email address.
	Email string

	// The amount of credits to purchase, in microcents. Must be positive.
	Amount tender.Value

	// SuccessURL is the URL to redirect to after successful checkout.
	SuccessURL string

	// CancelURL is the URL to redirect to if checkout is canceled.
	CancelURL string
}

// BuyCredits creates a Stripe checkout session for a one-time credit purchase.
// It returns the checkout URL for the customer to complete payment.
// The amount is specified in cents and stored as microcents in the database.
func (m *Manager) BuyCredits(ctx context.Context, billingID string, p *BuyCreditsParams) (checkoutURL string, _ error) {
	if p.Amount.IsWorthless() {
		return "", fmt.Errorf("amount must be positive, got %d", p.Amount)
	}

	c := m.client()

	params := &stripe.CheckoutSessionCreateParams{
		Customer:           &billingID,
		Mode:               new("payment"),
		PaymentMethodTypes: []*string{new("card")},
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{
			{
				PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
					Currency: new("usd"),
					ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{
						Name: new("Account Credits"),
					},
					UnitAmount: new(p.Amount.Cents()),
				},
				Quantity: new(int64(1)),
			},
		},
		SuccessURL: &p.SuccessURL,
		CancelURL:  &p.CancelURL,
	}
	params.AddMetadata("type", "credit_purchase")
	params.PaymentIntentData = &stripe.CheckoutSessionCreatePaymentIntentDataParams{
		Metadata: map[string]string{
			"type": "credit_purchase",
		},
	}

	sess, err := c.V1CheckoutSessions.Create(ctx, params)
	if isNotExists(err) {
		return "", fmt.Errorf("%w: stripe customer with billing ID %q", ErrNotFound, billingID)
	}
	if err != nil {
		return "", err
	}

	var requestID string
	if sess.LastResponse != nil {
		requestID = sess.LastResponse.RequestID
	}
	m.slog().InfoContext(ctx, "credit checkout session created",
		"stripe_request_id", requestID,
		"session_id", sess.ID,
		"billing_id", billingID,
		"cents", p.Amount,
	)
	return sess.URL, nil
}

// SyncCredits polls Stripe for completed credit-purchase payments
// and records them in the database. Each event is processed idempotently.
// It looks for payment_intent.succeeded events with credit_purchase metadata,
// which are generated when a BuyCredits checkout session is completed.
func (m *Manager) SyncCredits(ctx context.Context, since time.Time) error {
	c := m.client()

	params := &stripe.PaymentIntentListParams{
		CreatedRange: &stripe.RangeQueryParams{
			GreaterThan: since.Unix(),
		},
	}

	for intent, err := range c.V1PaymentIntents.List(ctx, params) {
		if err != nil {
			return fmt.Errorf("list payment intents: %w", err)
		}

		if intent.Status != stripe.PaymentIntentStatusSucceeded {
			continue
		}
		if intent.Metadata["type"] != "credit_purchase" {
			continue
		}

		// TODO(bmizrany): bulk insert?
		const q = `
			INSERT OR IGNORE INTO billing_credits (account_id, amount, stripe_event_id)
			VALUES (@accountID, @amount, @stripeEventID)
		`

		amount := tender.Mint(intent.Amount, 0)
		err := m.exec(ctx, q,
			sql.Named("accountID", intent.Customer.ID),
			sql.Named("amount", amount),
			sql.Named("stripeEventID", intent.ID),
		)
		if err != nil {
			return fmt.Errorf("insert credit ledger entry: %w", err)
		}

		m.slog().InfoContext(ctx, "credit purchase synced",
			"pi_id", intent.ID,
			"billing_id", intent.Customer.ID,
			"amount", amount,
		)
	}
	return nil
}

func (m *Manager) exec(ctx context.Context, q string, args ...any) error {
	for _, err := range m.query(ctx, q, args...) {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	return nil
}

func (m *Manager) query(ctx context.Context, q string, args ...any) iter.Seq2[*sql.Rows, error] {
	return func(yield func(*sql.Rows, error) bool) {
		err := m.DB.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			rows, err := tx.Query(q, args...)
			if err != nil {
				return err
			}
			defer rows.Close()

			for rows.Next() {
				if !yield(rows, nil) {
					break
				}
			}
			return rows.Err()
		})
		if err != nil {
			yield(nil, err)
		}
	}
}
