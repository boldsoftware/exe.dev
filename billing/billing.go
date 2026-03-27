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

	"exe.dev/billing/entitlement"
	"exe.dev/billing/tender"
	"exe.dev/errorz"
	"exe.dev/exedb"
	"exe.dev/logging"
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

	Logger    *slog.Logger
	SlackFeed *logging.SlackFeed

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
		err := exedb.WithTx1(m.DB, ctx, (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
			AccountID: sub.Customer.ID,
			EventType: eventType,
			EventAt:   sqlite.NormalizeTime(eventAt),
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
	basePlan := entitlement.CategoryBasic
	if eventType == "active" {
		basePlan = entitlement.CategoryIndividual
	}
	newPlanID := entitlement.PlanID(basePlan)

	// Skip if the active plan's base matches — avoids duplicate rows from poller replays.
	activePlan, err := exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check current plan: %w", err)
	}
	// Compare base plans so that both bare ("individual") and versioned
	// ("individual:monthly:20260325") are treated as equivalent.
	if err == nil && entitlement.BasePlan(activePlan.PlanID) == basePlan {
		return nil
	}

	normalizedAt := sqlite.NormalizeTime(eventAt)
	changedBy := "stripe:event"

	// Normalize trialEnd for SQLite storage if present.
	var normalizedTrialEnd *time.Time
	if trialEnd != nil {
		t := sqlite.NormalizeTime(*trialEnd)
		normalizedTrialEnd = &t
	}

	if err := exedb.WithTx(m.DB, ctx, func(ctx context.Context, q *exedb.Queries) error {
		if err := q.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{
			AccountID: accountID,
			EndedAt:   &normalizedAt,
		}); err != nil {
			return fmt.Errorf("close existing plan: %w", err)
		}
		return q.UpsertAccountPlan(ctx, exedb.UpsertAccountPlanParams{
			AccountID:      accountID,
			PlanID:         newPlanID,
			StartedAt:      normalizedAt,
			TrialExpiresAt: normalizedTrialEnd,
			ChangedBy:      &changedBy,
		})
	}); err != nil {
		return fmt.Errorf("sync account plan: %w", err)
	}

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
	SpendCredits(ctx context.Context, billingID string, quantity int, unitPrice tender.Value) (remaining tender.Value, _ error)
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

func (m *Manager) SpendCredits(ctx context.Context, billingID string, quantity int, unitPrice tender.Value) (remaining tender.Value, _ error) {
	if unitPrice.IsNegative() {
		return tender.Zero(), fmt.Errorf("unit price must be non-negative, got %d microcents", unitPrice.Microcents())
	}

	creditType := "usage"
	rem, err := exedb.WithTxRes1(m.DB, ctx, (*exedb.Queries).UseCredits, exedb.UseCreditsParams{
		AccountID:  billingID,
		Amount:     unitPrice.Times(-quantity).Microcents(),
		CreditType: &creditType,
	})
	if err != nil {
		return tender.Zero(), err
	}
	return tender.Mint(0, rem), nil
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
	for charge, err := range c.V1Charges.List(ctx, params) {
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
		p, i, v := entitlement.ParsePlanID(g.PlanID)
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
	now := time.Now().UTC()
	normalizedAt := sqlite.NormalizeTime(now)
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
			EndedAt: &normalizedAt,
		}); err != nil {
			return fmt.Errorf("close old plans: %w", err)
		}
		for _, accountID := range accountIDs {
			if err := q.InsertAccountPlanMigration(ctx, exedb.InsertAccountPlanMigrationParams{
				AccountID: accountID,
				PlanID:    toPlanID,
				StartedAt: normalizedAt,
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
