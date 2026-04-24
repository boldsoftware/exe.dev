package billing

import (
	"testing"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
	"github.com/stripe/stripe-go/v85"
)

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


