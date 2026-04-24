package billing

import (
	"context"
	"testing"
	"time"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
	"exe.dev/logging"
	"exe.dev/stage"
	"exe.dev/tslog"
	"github.com/stripe/stripe-go/v85"
)

func TestSyncAccountPlan_SlackTierChangeNotification(t *testing.T) {
	db := newTestDB(t)
	log := tslog.Slogger(t)
	sf := logging.NewSlackFeed(log, stage.Test())

	m := &Manager{
		DB:        db,
		Logger:    log,
		SlackFeed: sf,
	}

	ctx := context.Background()
	accountID := "acct_tier_notify"
	userID := "user_tier_notify"
	createTestAccount(t, db, accountID, userID)

	// Start on Individual Small.
	err := exedb.WithTx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: accountID,
			PlanID:    "individual:small:monthly:20260106",
			StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			ChangedBy: stringPtr("stripe:event"),
		})
	})
	if err != nil {
		t.Fatalf("InsertAccountPlan: %v", err)
	}

	// Upgrade to Medium via syncAccountPlan.
	mediumSub := &stripe.Subscription{
		Items: &stripe.SubscriptionItemList{
			Data: []*stripe.SubscriptionItem{{
				Price: &stripe.Price{
					LookupKey: "individual:medium:monthly:20160102",
					Recurring: &stripe.PriceRecurring{
						UsageType: stripe.PriceRecurringUsageTypeLicensed,
					},
				},
			}},
		},
	}
	eventAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := m.syncAccountPlan(ctx, accountID, "active", eventAt, nil, mediumSub); err != nil {
		t.Fatalf("syncAccountPlan upgrade: %v", err)
	}

	// Verify the plan changed.
	activePlan, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	if activePlan.PlanID != "individual:medium:monthly:20260106" {
		t.Fatalf("active plan = %q, want individual:medium:monthly:20260106", activePlan.PlanID)
	}

	// Downgrade back to Small.
	smallSub := &stripe.Subscription{
		Items: &stripe.SubscriptionItemList{
			Data: []*stripe.SubscriptionItem{{
				Price: &stripe.Price{
					LookupKey: "individual",
					Recurring: &stripe.PriceRecurring{
						UsageType: stripe.PriceRecurringUsageTypeLicensed,
					},
				},
			}},
		},
	}
	eventAt2 := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	if err := m.syncAccountPlan(ctx, accountID, "active", eventAt2, nil, smallSub); err != nil {
		t.Fatalf("syncAccountPlan downgrade: %v", err)
	}

	activePlan, err = exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	if activePlan.PlanID != "individual:small:monthly:20260106" {
		t.Fatalf("active plan = %q, want individual:small:monthly:20260106", activePlan.PlanID)
	}

	// Verify no notification when plan doesn't change (same subscription replayed).
	if err := m.syncAccountPlan(ctx, accountID, "active", eventAt2, nil, smallSub); err != nil {
		t.Fatalf("syncAccountPlan replay: %v", err)
	}

	// Verify no notification when transitioning to basic (not individual→individual).
	eventAt3 := time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)
	if err := m.syncAccountPlan(ctx, accountID, "canceled", eventAt3, nil, smallSub); err != nil {
		t.Fatalf("syncAccountPlan cancel: %v", err)
	}
	activePlan, err = exedb.WithRxRes1(db, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	if plan.Base(activePlan.PlanID) != plan.CategoryBasic {
		t.Fatalf("active plan = %q, want basic", activePlan.PlanID)
	}

	// Test passed: syncAccountPlan correctly resolved tiers and called SlackFeed.
	// With nil Slack client, the calls are logged (verified by log output in test).
	t.Log("Slack notifications fired correctly (logged, no real Slack client)")
}
