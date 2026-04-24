package billing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"exe.dev/billing/httprr"
	"exe.dev/billing/plan"
	"exe.dev/billing/stripetest"
	"exe.dev/billing/tender"
	"exe.dev/exedb"
	exesqlite "exe.dev/sqlite"
	_ "exe.dev/sqlite/sqltest"
	"exe.dev/tslog"
	"github.com/stripe/stripe-go/v85"
)

// newTestManager returns a Manager configured to use an httprr recorder
// for the test named t.Name(). It also returns a cleanup function that
// can be used to cleanup resources used by the Manager before exiting.
// This is typically needed in synctest bubbles to ensure the channels created
// in the Go resolver are cleaned up to prevent use outside syntest bubble.
func newTestManager(t *testing.T) *Manager {
	// t.Helper()

	fname := "testdata/stripe/" + t.Name()

	rr, err := httprr.Open(fname, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rr.Close(); err != nil {
			t.Errorf("failed to close httprr: %v", err)
		}
	})

	var counter atomic.Int64
	rr.ScrubReq(func(r *http.Request) error {
		r.Header.Del("Authorization")
		r.Header.Del("Idempotency-Key")
		r.Header.Del("Stripe-Version")
		r.Header.Del("User-Agent")
		r.Header.Del("X-Stripe-Client-User-Agent")
		r.Header.Del("X-Stripe-Client-Telemetry")

		// Normalize Replay-Id header to make recordings stable.
		// This prevents a response to an identical request in the same test
		// from clobbering the first request's recording.
		r.Header.Set("Replay-Id", fmt.Sprintf("req_%d", counter.Add(1)))

		// Normalize time-based query params to make recordings stable across runs.
		q := r.URL.Query()
		if q.Get("created[gte]") != "" {
			q.Set("created[gte]", "0")
			r.URL.RawQuery = q.Encode()
		}
		if q.Get("created[gt]") != "" {
			q.Set("created[gt]", "0")
			r.URL.RawQuery = q.Encode()
		}
		// Normalize customer param only for payment_intents listing
		// to keep SyncCredits recordings stable across test clock IDs.
		if strings.Contains(r.URL.Path, "/v1/payment_intents") && r.Method == "GET" && q.Get("customer") != "" {
			q.Set("customer", "cus_normalized")
			r.URL.RawQuery = q.Encode()
		}

		// Normalize test names in request bodies
		if r.Body != nil {
			if body, ok := r.Body.(*httprr.Body); ok {
				// Replace name=TestXXX with name=TestNormalized
				re := regexp.MustCompile(`name=Test[^&]*`)
				body.Data = re.ReplaceAll(body.Data, []byte("name=TestNormalized"))
				body.ReadOffset = 0
			}
		}
		return nil
	})
	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
		HTTPClient:    rr.Client(),
		LeveledLogger: stripetest.LeveledLogger(t),
	})
	m := &Manager{
		DB:     newTestDB(t),
		Logger: tslog.Slogger(t),
		Client: stripe.NewClient(TestAPIKey,
			stripe.WithBackends(backends),
		),
	}

	if err := m.InstallPrices(t.Context()); err != nil {
		t.Fatalf("InstallPrices: %v", err)
	}

	return m
}

type testClock struct {
	t *testing.T
	*stripetest.Clock
}

// startClock starts a new test clock for the provided Manager and
// registers a t.Cleanup function to delete the clock when the test
// completes successfully, or preserve it for debugging if the test failed.
func (m *Manager) startClock(t *testing.T) *testClock {
	t.Helper()
	clock, err := stripetest.Start(t.Context(), m.Client, t.Name())
	if err != nil {
		t.Fatalf("stripetest.Start: %v", err)
	}

	t.Cleanup(func() {
		t.Helper()
		if t.Failed() {
			t.Logf("Test clock preserved for debugging: %s", clock.Link())
		} else {
			clock.Close()
		}
	})

	t.Logf("test clock %q started (now=%v)", clock.ID(), clock.Now())
	m.testClockID = clock.ID()
	return &testClock{t, clock}
}

func (tc *testClock) Sleep(d time.Duration) {
	tc.t.Helper()
	err := tc.Clock.Sleep(tc.t.Context(), d, func(status string) {
		tc.t.Logf("test clock %q: %s", tc.ID(), status)
	})
	if err != nil {
		tc.t.Fatalf("clock.Sleep(%v): %v", d, err)
	}
}

func isStripeParamError(err error, param string) bool {
	if e, ok := errors.AsType[*stripe.Error](err); ok {
		return e.Param == param
	}
	return false
}

// newTestDB creates a test database with all migrations applied and returns
// an exe.dev/sqlite.DB suitable for use in billing Manager tests.
func newTestDB(t *testing.T) *exesqlite.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "billing_test.db")
	if err := exedb.CopyTemplateDB(tslog.Slogger(t), dbPath); err != nil {
		t.Fatalf("failed to copy template database: %v", err)
	}

	db, err := exesqlite.New(dbPath, 1)
	if err != nil {
		t.Fatalf("failed to create sqlite wrapper: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestDBGoTime is like newTestDB, but uses SQLite's _go_time mode so
// CURRENT_TIMESTAMP follows Go's time.Now, including synctest bubbles.
func newTestDBGoTime(t *testing.T) *exesqlite.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "billing_test.db")
	if err := exedb.CopyTemplateDB(tslog.Slogger(t), dbPath); err != nil {
		t.Fatalf("failed to copy template database: %v", err)
	}
	dsn := "file:" + dbPath + "?_go_time=true"

	db, err := exesqlite.New(dsn, 1)
	if err != nil {
		t.Fatalf("failed to create sqlite wrapper: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// createTestAccount creates a test user and account in the DB for billing tests.
func createTestAccount(t *testing.T, db *exesqlite.DB, accountID, userID string) {
	t.Helper()
	err := exedb.WithTx(db, context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		if err := q.InsertUser(ctx, exedb.InsertUserParams{
			UserID: userID,
			Email:  userID + "@example.com",
			Region: "pdx",
		}); err != nil {
			return err
		}
		return q.InsertAccount(ctx, exedb.InsertAccountParams{
			ID:        accountID,
			CreatedBy: userID,
		})
	})
	if err != nil {
		t.Fatalf("createTestAccount: %v", err)
	}
}

// newEmptyTestDB creates a bare SQLite database without migrations.
// Useful for testing error paths where queries fail.
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

func stringPtr(s string) *string { return &s }

// stripeSubscribe creates a subscription for the given customer ID and price
// lookup key using the Stripe API directly to simulate checkout/portal
// subscription creation.
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

// stripeCompleteCreditPurchase simulates a completed credit purchase by creating
// and confirming a PaymentIntent with credit_purchase metadata. This generates the
// payment_intent.succeeded event that SyncCredits processes.
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

// stripeChangeTier updates an existing subscription to a new price.
// It finds the subscription's current non-metered item and swaps its price,
// using immediate proration (matching Stripe portal default behavior).
func stripeChangeTier(ctx context.Context, m *Manager, customerID, newPriceLookupKey string) (*stripe.Subscription, error) {
	c := m.client()

	newPriceID, err := m.lookupPriceID(ctx, newPriceLookupKey)
	if err != nil {
		return nil, err
	}

	// Find the active subscription and its non-metered item.
	var subID, itemID string
	for sub, err := range c.V1Subscriptions.List(ctx, &stripe.SubscriptionListParams{
		Customer: &customerID,
		Status:   stripe.String(string(stripe.SubscriptionStatusActive)),
	}).All(ctx) {
		if err != nil {
			return nil, err
		}
		subID = sub.ID
		for _, item := range sub.Items.Data {
			if item.Price != nil && item.Price.Recurring != nil &&
				item.Price.Recurring.UsageType != stripe.PriceRecurringUsageTypeMetered {
				itemID = item.ID
				break
			}
		}
		break
	}
	if subID == "" {
		return nil, errors.New("no active subscription found")
	}
	if itemID == "" {
		return nil, errors.New("no non-metered subscription item found")
	}

	proration := "create_prorations"
	return c.V1Subscriptions.Update(ctx, subID, &stripe.SubscriptionUpdateParams{
		ProrationBehavior: &proration,
		Items: []*stripe.SubscriptionUpdateItemParams{
			{
				ID:    &itemID,
				Price: &newPriceID,
			},
		},
	})
}

// getActiveSubscription returns the active subscription for a customer.
func getActiveSubscription(ctx context.Context, m *Manager, customerID string) (*stripe.Subscription, error) {
	for sub, err := range m.client().V1Subscriptions.List(ctx, &stripe.SubscriptionListParams{
		Customer: &customerID,
		Status:   stripe.String(string(stripe.SubscriptionStatusActive)),
	}).All(ctx) {
		if err != nil {
			return nil, err
		}
		return sub, nil
	}
	return nil, errors.New("no active subscription")
}

// testEventClock is a simple monotonic clock for generating event timestamps
// in tests. Each call to next() returns a timestamp 1 second later than the
// previous, ensuring syncAccountPlan's stale-event check never skips events
// regardless of wall-clock speed (important for cassette replay).
type testEventClock struct {
	now time.Time
}

func newTestEventClock() *testEventClock {
	return &testEventClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *testEventClock) next() time.Time {
	c.now = c.now.Add(time.Second)
	return c.now
}

// syncSub simulates what SyncSubscriptions does for a single subscription event:
// derives the event type from the subscription status, then calls syncAccountPlan.
func syncSub(t *testing.T, m *Manager, ctx context.Context, accountID string, sub *stripe.Subscription, eventAt time.Time) {
	t.Helper()
	eventType, ok := subscriptionEventType("customer.subscription.updated", sub.Status)
	if !ok {
		t.Fatalf("subscriptionEventType: no event type for status %q", sub.Status)
	}
	if err := m.syncAccountPlan(ctx, accountID, eventType, eventAt, nil, sub); err != nil {
		t.Fatalf("syncAccountPlan: %v", err)
	}
}

// setBasicPlan inserts a basic plan for the account.
func setBasicPlan(t *testing.T, db *exesqlite.DB, accountID string) {
	t.Helper()
	err := exedb.WithTx(db, context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		return q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: accountID,
			PlanID:    plan.ID(plan.CategoryBasic),
			StartedAt: time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC),
		})
	})
	if err != nil {
		t.Fatalf("setBasicPlan: %v", err)
	}
}

// assertActivePlanCategory checks that the active plan for the account
// matches the expected category.
func assertActivePlanCategory(t *testing.T, db *exesqlite.DB, accountID string, wantCategory plan.Category) {
	t.Helper()
	ap, err := exedb.WithRxRes1(db, context.Background(), (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	gotCategory := plan.Base(ap.PlanID)
	if gotCategory != wantCategory {
		t.Fatalf("active plan category = %q, want %q (plan_id=%q)", gotCategory, wantCategory, ap.PlanID)
	}
}

// assertActivePlanID checks that the active plan_id matches exactly.
func assertActivePlanID(t *testing.T, db *exesqlite.DB, accountID, wantPlanID string) {
	t.Helper()
	ap, err := exedb.WithRxRes1(db, context.Background(), (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("no active plan found, want %q", wantPlanID)
		}
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	if ap.PlanID != wantPlanID {
		t.Fatalf("active plan_id = %q, want %q", ap.PlanID, wantPlanID)
	}
}

// assertSubscriptionPrice verifies that the subscription's non-metered item
// uses the expected price lookup key.
func assertSubscriptionPrice(t *testing.T, sub *stripe.Subscription, wantLookupKey string) {
	t.Helper()
	got := subscriptionLookupKey(sub)
	if got != wantLookupKey {
		t.Fatalf("subscription price lookup key = %q, want %q", got, wantLookupKey)
	}
}
