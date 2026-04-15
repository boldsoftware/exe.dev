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
	"strings"
	"sync"
	"time"

	"exe.dev/billing/plan"
	"exe.dev/billing/tender"
	"exe.dev/errorz"
	"exe.dev/exedb"
	"exe.dev/logging"
	"exe.dev/sqlite"
	"github.com/stripe/stripe-go/v85"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
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
	DefaultPlan         = "individual"
	productIndividualID = "prod_individual"
	productIndividual   = "Individual"
	productTeamID       = "prod_team"
	productTeam         = "Team"

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
	metered     bool
	usageType   string
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
	{
		lookupKey:   "individual:annual:20260106",
		currency:    stripe.CurrencyUSD,
		unitAmount:  20000,
		interval:    stripe.PriceRecurringIntervalYear,
		productID:   productIndividualID,
		productName: productIndividual,
	},
	{
		lookupKey:   "individual:usage-disk:20260106",
		currency:    stripe.CurrencyUSD,
		unitAmount:  8, // $0.08 in cents
		productID:   productIndividualID,
		productName: productIndividual,
		metered:     true,
		usageType:   "metered",
	},
	{
		lookupKey:   "individual:usage-bandwidth:20260106",
		currency:    stripe.CurrencyUSD,
		unitAmount:  7, // $0.07 in cents
		productID:   productIndividualID,
		productName: productIndividual,
		metered:     true,
		usageType:   "metered",
	},
	{
		lookupKey:   "team:monthly:20260106",
		currency:    stripe.CurrencyUSD,
		unitAmount:  2500,
		interval:    stripe.PriceRecurringIntervalMonth,
		productID:   productTeamID,
		productName: productTeam,
	},
	{
		lookupKey:   "team:annual:20260106",
		currency:    stripe.CurrencyUSD,
		unitAmount:  25000,
		interval:    stripe.PriceRecurringIntervalYear,
		productID:   productTeamID,
		productName: productTeam,
	},
	{
		lookupKey:   "team:usage-disk:20260106",
		currency:    stripe.CurrencyUSD,
		unitAmount:  8,
		productID:   productTeamID,
		productName: productTeam,
		metered:     true,
		usageType:   "metered",
	},
	{
		lookupKey:   "team:usage-bandwidth:20260106",
		currency:    stripe.CurrencyUSD,
		unitAmount:  7,
		productID:   productTeamID,
		productName: productTeam,
		metered:     true,
		usageType:   "metered",
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

	priceIDCache syncs.Map[string, func() result.Of[string]]

	// OnPlanDowngrade is called when an account's plan is downgraded to basic
	// (e.g., subscription canceled, payment failed, trial expired).
	// The callback receives the account ID. If nil, no callback is made.
	OnPlanDowngrade func(ctx context.Context, accountID string)

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
		if p.metered {
			continue // TODO: metered prices require Stripe Meters as of API version 2025-03-31.basil
		}
		if err := m.ensureProduct(ctx, c, p.productID, p.productName); err != nil {
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
		if err := m.syncAccountPlan(ctx, sub.Customer.ID, eventType, eventAt, trialEnd); err != nil {
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
// "active" -> close current plan, insert versioned "individual" plan ID.
// "canceled" -> close current plan, insert versioned "basic" plan ID.
//
// When trialEnd is non-nil (trialing subscription), trial_expires_at is written
// so we can distinguish trialing users from paid ones in queries.
//
// Versioned plan IDs use the format "{plan}:{interval}:{YYYYMMDD}".
func (m *Manager) syncAccountPlan(ctx context.Context, accountID, eventType string, eventAt time.Time, trialEnd *time.Time) error {
	basePlan := plan.CategoryBasic
	if eventType == "active" {
		basePlan = plan.CategoryIndividual
	}
	newPlanID := plan.ID(basePlan)

	// Skip if the active plan's base matches — avoids duplicate rows from poller replays.
	activePlan, err := exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check current plan: %w", err)
	}
	// Compare base plans so that both bare ("individual") and versioned
	// ("individual:monthly:20260325") are treated as equivalent.
	if err == nil && plan.Base(activePlan.PlanID) == basePlan {
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

	return nil
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

// Credits defines the interface for credit operations on billing accounts.
type Credits interface {
	GiftCredits(ctx context.Context, billingID string, p *GiftCreditsParams) error
	SpendCredits(ctx context.Context, billingID string, quantity int, unitPrice tender.Value) error
	CreditBalance(ctx context.Context, billingID string) (tender.Value, error)
	GetCreditState(ctx context.Context, billingID string) (*CreditState, error)
	ListGifts(ctx context.Context, billingID string) ([]GiftEntry, error)
}

var _ Credits = (*Manager)(nil)

// GiftCreditsParams contains the parameters for gifting credits to an account.
// Callers provide a dollar amount and a gift prefix; the billing package handles
// conversion to microcents and construction of the full gift_id.
type GiftCreditsParams struct {
	// AmountUSD is the gift amount in US dollars. Must be positive.
	AmountUSD float64
	// GiftPrefix is the gift type prefix (e.g. GiftPrefixSignup, GiftPrefixDebug).
	GiftPrefix string
	// Note is an optional human-readable reason for the gift.
	// Defaults to "Credit gift from support@exe.dev" if empty.
	Note string
}

// GiftCredits inserts a gift credit for the given billing account.
// The gift_id is constructed as "<prefix>:<billingID>:<nanos>" for uniqueness.
// The operation is idempotent: a duplicate gift_id is silently ignored.
func (m *Manager) GiftCredits(ctx context.Context, billingID string, p *GiftCreditsParams) error {
	if p.AmountUSD <= 0 {
		return fmt.Errorf("gift amount must be positive, got %v", p.AmountUSD)
	}
	if p.GiftPrefix == "" {
		return errors.New("gift prefix is required")
	}

	amount := tender.Mint(int64(p.AmountUSD*100), 0)
	giftID := fmt.Sprintf("%s:%s:%d", p.GiftPrefix, billingID, time.Now().UnixNano())

	note := p.Note
	if note == "" {
		note = "Credit gift from support@exe.dev"
	}

	return exedb.WithTx1(m.DB, ctx, (*exedb.Queries).GiftCredits, exedb.GiftCreditsParams{
		AccountID: billingID,
		Amount:    amount.Microcents(),
		GiftID:    &giftID,
		Note:      &note,
	})
}

// CreditState holds the breakdown of credits for an account.
type CreditState struct {
	Paid  tender.Value
	Gift  tender.Value
	Used  tender.Value
	Total tender.Value
}

// GetCreditState returns the credit breakdown for the given billing account.
// Used is stored as an absolute (positive) value even though usage rows are negative in the DB.
func (m *Manager) GetCreditState(ctx context.Context, billingID string) (*CreditState, error) {
	row, err := exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).GetCreditState, billingID)
	if err != nil {
		return nil, fmt.Errorf("get credit state: %w", err)
	}
	s := &CreditState{
		Gift:  tender.Mint(0, row.Gift),
		Paid:  tender.Mint(0, row.Paid),
		Total: tender.Mint(0, row.Total),
		// Used is stored as negative in DB; return absolute value.
		Used: tender.Mint(0, -row.Used),
	}
	return s, nil
}

// GiftEntry represents a single gift credit entry.
type GiftEntry struct {
	Amount    tender.Value
	Note      string
	GiftID    string
	CreatedAt time.Time
}

// ListGifts returns all gift credits for the given billing account, ordered by
// most recent first. Returns an empty (non-nil) slice if no gifts exist.
func (m *Manager) ListGifts(ctx context.Context, billingID string) ([]GiftEntry, error) {
	rows, err := exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).ListGiftCredits, billingID)
	if err != nil {
		return nil, fmt.Errorf("list gifts: %w", err)
	}
	gifts := make([]GiftEntry, len(rows))
	for i, r := range rows {
		gifts[i] = GiftEntry{
			Amount:    tender.Mint(0, r.Amount),
			CreatedAt: r.CreatedAt,
		}
		if r.Note != nil {
			gifts[i].Note = *r.Note
		}
		if r.GiftID != nil {
			gifts[i].GiftID = *r.GiftID
		}
	}
	return gifts, nil
}

func (m *Manager) SpendCredits(ctx context.Context, billingID string, quantity int, unitPrice tender.Value) error {
	if unitPrice.IsNegative() {
		return fmt.Errorf("unit price must be non-negative, got %d microcents", unitPrice.Microcents())
	}

	creditType := "usage"
	return exedb.WithTx1(m.DB, ctx, (*exedb.Queries).UseCredits, exedb.UseCreditsParams{
		AccountID:  billingID,
		Amount:     unitPrice.Times(-quantity).Microcents(),
		CreditType: &creditType,
	})
}

func (m *Manager) CreditBalance(ctx context.Context, billingID string) (tender.Value, error) {
	bal, err := exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).GetCreditBalance, billingID)
	if err != nil {
		return tender.Zero(), err
	}
	return tender.Mint(0, bal), nil
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

	// Auto-recharge enablement is checked by callers before charging.
	// A concurrent disable can still allow one in-flight recharge attempt.
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

	for intent, err := range c.V1PaymentIntents.List(ctx, params).All(ctx) {
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
		amount := tender.Mint(intent.Amount, 0)
		stripeEventID := intent.ID
		err := exedb.WithTx1(m.DB, ctx, (*exedb.Queries).InsertPaidCredits, exedb.InsertPaidCreditsParams{
			AccountID:     intent.Customer.ID,
			Amount:        amount.Microcents(),
			StripeEventID: &stripeEventID,
		})
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

// ReceiptURLs returns a map of payment intent ID to Stripe receipt URL
// for all succeeded credit-purchase charges belonging to the given customer.
func (m *Manager) ReceiptURLs(ctx context.Context, customerID string) (map[string]string, error) {
	c := m.client()

	params := &stripe.ChargeListParams{
		Customer: stripe.String(customerID),
	}

	result := make(map[string]string)
	for charge, err := range c.V1Charges.List(ctx, params).All(ctx) {
		if err != nil {
			return nil, fmt.Errorf("list charges: %w", err)
		}
		if charge.PaymentIntent == nil || charge.ReceiptURL == "" {
			continue
		}
		if charge.Metadata["type"] != "credit_purchase" {
			continue
		}
		result[charge.PaymentIntent.ID] = charge.ReceiptURL
	}
	return result, nil
}

// ReceiptInfo holds a receipt URL and the charge creation time.
type ReceiptInfo struct {
	URL     string
	Created time.Time
}

// ReceiptURLsAfter returns receipt URLs for credit-purchase charges created at or after since.
func (m *Manager) ReceiptURLsAfter(ctx context.Context, customerID string, since time.Time) ([]ReceiptInfo, error) {
	c := m.client()
	params := &stripe.ChargeListParams{
		Customer:     stripe.String(customerID),
		CreatedRange: &stripe.RangeQueryParams{GreaterThanOrEqual: since.Unix()},
	}
	var result []ReceiptInfo
	for charge, err := range c.V1Charges.List(ctx, params).All(ctx) {
		if err != nil {
			return nil, fmt.Errorf("list charges: %w", err)
		}
		if charge.ReceiptURL == "" || charge.Metadata["type"] != "credit_purchase" {
			continue
		}
		result = append(result, ReceiptInfo{URL: charge.ReceiptURL, Created: time.Unix(charge.Created, 0).UTC()})
	}
	return result, nil
}

// InvoiceInfo holds the fields needed to display an invoice in the UI.
type InvoiceInfo struct {
	Description      string
	PlanName         string    // e.g. "Individual", "Team" — from first line item
	PeriodStart      time.Time // billing period start
	PeriodEnd        time.Time // billing period end
	Date             time.Time
	AmountPaid       int64  // cents
	Currency         string // e.g. "usd"
	Status           string // "paid", "open", "draft", "void", "uncollectible"
	HostedInvoiceURL string
	InvoicePDF       string
}

// ListInvoices returns the customer's invoices from the last 6 months.
func (m *Manager) ListInvoices(ctx context.Context, customerID string) ([]InvoiceInfo, error) {
	c := m.client()

	since := time.Now().AddDate(0, -6, 0)
	params := &stripe.InvoiceListParams{
		Customer:     stripe.String(customerID),
		CreatedRange: &stripe.RangeQueryParams{GreaterThanOrEqual: since.Unix()},
	}
	params.ListParams.Limit = stripe.Int64(12) // at most 12 invoices in 6 months

	var result []InvoiceInfo
	for inv, err := range c.V1Invoices.List(ctx, params).All(ctx) {
		if err != nil {
			return nil, fmt.Errorf("list invoices: %w", err)
		}
		// Only show paid and open invoices
		if inv.Status != stripe.InvoiceStatusPaid && inv.Status != stripe.InvoiceStatusOpen {
			continue
		}
		desc := inv.Description
		if desc == "" {
			// Build description from billing reason and period
			t := time.Unix(inv.PeriodEnd, 0).UTC()
			desc = "Subscription — " + t.Format("Jan 2006")
		}

		// Extract plan name and service period from first line item.
		// Line item description is like "1 × Individual Plan (at $20.00 / month)".
		// Line item Period has the actual service dates (invoice-level period is just the anchor).
		var planName string
		periodStart := time.Unix(inv.PeriodStart, 0).UTC()
		periodEnd := time.Unix(inv.PeriodEnd, 0).UTC()
		if inv.Lines != nil {
			for _, li := range inv.Lines.Data {
				if li.Description != "" {
					planName = parseInvoiceLinePlanName(li.Description)
				}
				if li.Period != nil {
					periodStart = time.Unix(li.Period.Start, 0).UTC()
					periodEnd = time.Unix(li.Period.End, 0).UTC()
				}
				break
			}
		}

		result = append(result, InvoiceInfo{
			Description:      desc,
			PlanName:         planName,
			PeriodStart:      periodStart,
			PeriodEnd:        periodEnd,
			Date:             time.Unix(inv.Created, 0).UTC(),
			AmountPaid:       inv.AmountPaid,
			Currency:         string(inv.Currency),
			Status:           string(inv.Status),
			HostedInvoiceURL: inv.HostedInvoiceURL,
			InvoicePDF:       inv.InvoicePDF,
		})
	}
	return result, nil
}

// UpcomingInvoice returns a preview of the customer's next invoice, or nil if there isn't one.
func (m *Manager) UpcomingInvoice(ctx context.Context, customerID string) (*InvoiceInfo, error) {
	c := m.client()
	inv, err := c.V1Invoices.CreatePreview(ctx, &stripe.InvoiceCreatePreviewParams{
		Customer: stripe.String(customerID),
	})
	if err != nil {
		// No upcoming invoice (e.g. no active subscription) is not an error.
		return nil, nil //nolint:nilerr
	}

	var planName string
	periodStart := time.Unix(inv.PeriodStart, 0).UTC()
	periodEnd := time.Unix(inv.PeriodEnd, 0).UTC()
	if inv.Lines != nil {
		for _, li := range inv.Lines.Data {
			if li.Description != "" {
				planName = parseInvoiceLinePlanName(li.Description)
			}
			if li.Period != nil {
				periodStart = time.Unix(li.Period.Start, 0).UTC()
				periodEnd = time.Unix(li.Period.End, 0).UTC()
			}
			break
		}
	}

	return &InvoiceInfo{
		Description: "Upcoming",
		PlanName:    planName,
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
		Date:        time.Unix(inv.Created, 0).UTC(),
		AmountPaid:  inv.AmountDue,
		Currency:    string(inv.Currency),
		Status:      "upcoming",
	}, nil
}

// parseInvoiceLinePlanName extracts a clean plan name from a Stripe line item description.
// Input like "1 × Individual Plan (at $20.00 / month)" returns "Individual".
func parseInvoiceLinePlanName(desc string) string {
	// Strip leading quantity: "1 × " or "1 x "
	if i := strings.Index(desc, "×"); i >= 0 {
		desc = strings.TrimSpace(desc[i+len("×"):])
	} else if i := strings.Index(desc, " x "); i >= 0 {
		desc = strings.TrimSpace(desc[i+3:])
	}
	// Strip trailing pricing: " (at $20.00 / month)"
	if i := strings.Index(desc, " (at "); i >= 0 {
		desc = strings.TrimSpace(desc[:i])
	}
	// Strip trailing " Plan" suffix for cleanliness
	desc = strings.TrimSuffix(desc, " Plan")
	return desc
}

// PlanCategoryGroup represents a group of subscribers on the same plan_id version.
type PlanCategoryGroup struct {
	PlanID   string
	BasePlan string
	Interval string
	Version  string
	Count    int
}

// ListPlanCategorys returns all active plan versions with subscriber counts,
// grouped by the full plan_id value in account_plans.
func (m *Manager) ListPlanCategorys(ctx context.Context) ([]PlanCategoryGroup, error) {
	rows, err := exedb.WithRxRes0(m.DB, ctx, (*exedb.Queries).ListPlanVersionCounts)
	if err != nil {
		return nil, fmt.Errorf("list plan versions: %w", err)
	}
	var groups []PlanCategoryGroup
	for _, row := range rows {
		g := PlanCategoryGroup{
			PlanID: row.PlanID,
			Count:  int(row.Cnt),
		}
		p, i, v := plan.ParseID(g.PlanID)
		g.BasePlan, g.Interval, g.Version = string(p), i, v
		groups = append(groups, g)
	}
	return groups, nil
}

// ListSubscribersByPlanCategory returns all account IDs with the given plan_id.
func (m *Manager) ListSubscribersByPlanCategory(ctx context.Context, planID string) ([]string, error) {
	return exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).ListActiveSubscribersByPlanID, planID)
}

// MigratePlanCategory batch-migrates all active subscribers from one plan_id
// to another, closing the old plan and inserting the new one within a single
// transaction. Returns the number of accounts migrated.
func (m *Manager) MigratePlanCategory(ctx context.Context, fromPlanID, toPlanID string) (int, error) {
	now := time.Now()
	changedBy := fmt.Sprintf("admin:migrate:%s->%s", fromPlanID, toPlanID)

	// Collect account IDs first.
	accountIDs, err := exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).ListActiveSubscribersByPlanID, fromPlanID)
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

// PaymentMethodInfo holds display-safe payment method details fetched from Stripe.
type PaymentMethodInfo struct {
	Type         string // "card", "link", "paypal", etc.
	Brand        string // For cards: "visa", "mastercard", etc.
	Last4        string // Last 4 digits for cards, empty otherwise
	ExpMonth     int    // 1-12 for cards, 0 otherwise
	ExpYear      int    // 4-digit year for cards, 0 otherwise
	Email        string // For Link/PayPal: the associated email
	DisplayLabel string // e.g. "Visa •••• 4242" or "Link (user@example.com)"
}

// GetPaymentMethod fetches the payment method used for the customer's subscription from Stripe.
// Returns (nil, nil) if no payment method is set or the customer doesn't exist.
//
// Stripe Checkout sets the payment method on the subscription (not on the
// customer-level invoice_settings.default_payment_method), so we check the
// subscription first. This ensures that when a user pays with a card that
// isn't their customer-level default, we show the correct one.
func (m *Manager) GetPaymentMethod(ctx context.Context, billingID string) (*PaymentMethodInfo, error) {
	c := m.client()

	// Prefer the subscription's payment method — this is the one actually
	// being charged for the subscription. Check active, trialing, and
	// past_due statuses (all represent a live subscription).
	for _, status := range []string{"active", "trialing", "past_due"} {
		subParams := &stripe.SubscriptionListParams{
			Customer: &billingID,
			Status:   new(status),
		}
		subParams.AddExpand("data.default_payment_method")
		for sub, err := range c.V1Subscriptions.List(ctx, subParams).All(ctx) {
			if err != nil {
				return nil, fmt.Errorf("list %s subscriptions for payment method: %w", status, err)
			}
			if sub.DefaultPaymentMethod != nil {
				return extractPaymentMethodInfo(sub.DefaultPaymentMethod), nil
			}
		}
	}

	// Fall back to the customer-level default payment method.
	params := &stripe.CustomerRetrieveParams{}
	params.AddExpand("invoice_settings.default_payment_method")

	customer, err := c.V1Customers.Retrieve(ctx, billingID, params)
	if err != nil {
		if isNotExists(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("retrieve customer for payment method: %w", err)
	}

	if customer.InvoiceSettings != nil && customer.InvoiceSettings.DefaultPaymentMethod != nil {
		return extractPaymentMethodInfo(customer.InvoiceSettings.DefaultPaymentMethod), nil
	}

	return nil, nil
}

func extractPaymentMethodInfo(pm *stripe.PaymentMethod) *PaymentMethodInfo {
	if pm == nil {
		return nil
	}
	info := &PaymentMethodInfo{Type: string(pm.Type)}
	switch pm.Type {
	case stripe.PaymentMethodTypeCard:
		if pm.Card != nil {
			info.Brand = string(pm.Card.Brand)
			info.Last4 = pm.Card.Last4
			info.ExpMonth = int(pm.Card.ExpMonth)
			info.ExpYear = int(pm.Card.ExpYear)
			info.DisplayLabel = formatCardLabel(info.Brand, info.Last4)
		}
	case stripe.PaymentMethodTypeLink:
		if pm.Link != nil && pm.Link.Email != "" {
			info.Email = pm.Link.Email
			info.DisplayLabel = fmt.Sprintf("Link (%s)", pm.Link.Email)
		} else {
			info.DisplayLabel = "Link"
		}
	case stripe.PaymentMethodTypePaypal:
		if pm.Paypal != nil && pm.Paypal.PayerEmail != "" {
			info.Email = pm.Paypal.PayerEmail
			info.DisplayLabel = fmt.Sprintf("PayPal (%s)", pm.Paypal.PayerEmail)
		} else {
			info.DisplayLabel = "PayPal"
		}
	default:
		info.DisplayLabel = formatPaymentTypeLabel(string(pm.Type))
	}
	return info
}

func formatCardLabel(brand, last4 string) string {
	tc := cases.Title(language.English)
	brandDisplay := tc.String(brand)
	switch brand {
	case "amex":
		brandDisplay = "American Express"
	case "diners":
		brandDisplay = "Diners Club"
	case "jcb":
		brandDisplay = "JCB"
	}
	if last4 != "" {
		return fmt.Sprintf("%s •••• %s", brandDisplay, last4)
	}
	return brandDisplay
}

func formatPaymentTypeLabel(pmType string) string {
	tc := cases.Title(language.English)
	return tc.String(strings.ReplaceAll(pmType, "_", " "))
}
