package billing

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
	exesqlite "exe.dev/sqlite"
	"github.com/stripe/stripe-go/v85"
)

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

// TestTierLifecycle_SubscribeUpgradeDowngrade exercises the full billing
// lifecycle: basic → individual (small) → upgrade to medium → downgrade
// back to small → cancel, verifying that account_plans reflects each
// transition and that the correct Stripe prices are used.
func TestTierLifecycle_SubscribeUpgradeDowngrade(t *testing.T) {
	m := newTestManager(t)
	clock := m.startClock(t)
	ctx := t.Context()

	billingID := "exe_tier_lc_" + clock.ID()
	userID := "user_tier_lifecycle"
	createTestAccount(t, m.DB, billingID, userID)

	// ---- Step 0: account starts on basic ----
	setBasicPlan(t, m.DB, billingID)
	assertActivePlanCategory(t, m.DB, billingID, plan.CategoryBasic)

	// ---- Step 1: subscribe to Individual (small) ----
	if err := m.upsertCustomer(ctx, billingID, "tier-lifecycle@example.com"); err != nil {
		t.Fatalf("upsertCustomer: %v", err)
	}
	if err := stripeSubscribe(ctx, m, billingID, "pm_card_visa", "individual"); err != nil {
		t.Fatalf("stripeSubscribe individual: %v", err)
	}

	ec := newTestEventClock()

	sub, err := getActiveSubscription(ctx, m, billingID)
	if err != nil {
		t.Fatalf("getActiveSubscription after subscribe: %v", err)
	}
	assertSubscriptionPrice(t, sub, "individual")

	// Simulate what SyncSubscriptions does: derive the event type and sync the plan.
	syncSub(t, m, ctx, billingID, sub, ec.next())
	assertActivePlanID(t, m.DB, billingID, "individual:small:monthly:20260106")

	// ---- Step 2: upgrade to medium ----
	updatedSub, err := stripeChangeTier(ctx, m, billingID, "individual:medium:monthly:20160102")
	if err != nil {
		t.Fatalf("stripeChangeTier to medium: %v", err)
	}
	assertSubscriptionPrice(t, updatedSub, "individual:medium:monthly:20160102")

	syncSub(t, m, ctx, billingID, updatedSub, ec.next())
	assertActivePlanID(t, m.DB, billingID, "individual:medium:monthly:20260106")

	// ---- Step 3: downgrade back to small ----
	updatedSub, err = stripeChangeTier(ctx, m, billingID, "individual")
	if err != nil {
		t.Fatalf("stripeChangeTier to small: %v", err)
	}
	assertSubscriptionPrice(t, updatedSub, "individual")

	syncSub(t, m, ctx, billingID, updatedSub, ec.next())
	assertActivePlanID(t, m.DB, billingID, "individual:small:monthly:20260106")

	// ---- Step 4: cancel subscription → should revert to basic ----
	sub, err = getActiveSubscription(ctx, m, billingID)
	if err != nil {
		t.Fatalf("getActiveSubscription before cancel: %v", err)
	}
	if _, err := m.client().V1Subscriptions.Cancel(ctx, sub.ID, nil); err != nil {
		t.Fatalf("cancel subscription: %v", err)
	}

	// Fetch the canceled subscription to get its final state.
	canceledSub, err := m.client().V1Subscriptions.Retrieve(ctx, sub.ID, nil)
	if err != nil {
		t.Fatalf("retrieve canceled subscription: %v", err)
	}
	if canceledSub.Status != stripe.SubscriptionStatusCanceled {
		t.Fatalf("subscription status = %q, want canceled", canceledSub.Status)
	}

	syncSub(t, m, ctx, billingID, canceledSub, ec.next())
	assertActivePlanID(t, m.DB, billingID, plan.ID(plan.CategoryBasic))

	// Verify plan history shows the full progression.
	history, err := exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).ListAccountPlanHistory, billingID)
	if err != nil {
		t.Fatalf("ListAccountPlanHistory: %v", err)
	}
	for i, h := range history {
		t.Logf("  history[%d]: plan_id=%s started_at=%v ended_at=%v changed_by=%v",
			i, h.PlanID, h.StartedAt, h.EndedAt, h.ChangedBy)
	}
	// ListAccountPlanHistory is ordered by started_at DESC (newest first).
	// Expect: basic (active), small (closed), medium (closed), small (closed), basic (closed)
	if len(history) < 5 {
		t.Fatalf("expected at least 5 plan history entries, got %d", len(history))
	}

	newest := history[0]
	if newest.PlanID != plan.ID(plan.CategoryBasic) {
		t.Fatalf("newest history plan_id = %q, want %q", newest.PlanID, plan.ID(plan.CategoryBasic))
	}
	if newest.EndedAt != nil {
		t.Fatalf("newest history ended_at = %v, want nil (active)", newest.EndedAt)
	}
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
