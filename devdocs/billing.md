# Billing

exe.dev uses Stripe for subscriptions and credit purchases. Access is controlled by plans and entitlements.

## Plans and Entitlements

Access control uses a plan-based system defined in `billing/plan/`. Each account has an active plan in `account_plans` (the row where `ended_at IS NULL`). The plan determines which entitlements are granted.

**Plan catalog** (`billing/plan/plan.go`):

| Plan | Grants | LLM Category |
|------|--------|--------------|
| `vip` | All entitlements (wildcard) | `friend` |
| `enterprise` | LLM, credits, invites, VM create/run, disk resize | `has_billing` |
| `team` | LLM, credits, invites, VM create/run, disk resize | `has_billing` |
| `individual` | LLM, credits, invites, teams, VM create/run, disk resize | `has_billing` |
| `friend` | LLM, VM create/run, disk resize | `friend` |
| `grandfathered` | LLM, VM create/run, disk resize | `no_billing` |
| `trial` | LLM, VM create/run, disk resize | `no_billing` |
| `basic` | LLM only | `no_billing` |
| `restricted` | Nothing | `no_billing` |

**Plan resolution** (`billing/plan/plan.go:GetPlanCategory`), in priority order:

1. `canceled` billing status -> `basic` (overrides everything)
2. `HasExplicitOverrides` (plan_id like `vip:%`) -> `vip`
3. `friend` category -> `friend`
4. Team billing active -> `team`
5. `has_billing` category -> `individual`
6. Trial exemption not yet expired -> `trial`
7. Created before 2026-01-06 23:10 UTC -> `grandfathered`
8. Otherwise -> `basic`

**Entitlement check**: `execore/billing_status.go:UserHasEntitlement()` resolves user -> account -> active plan -> entitlements. Checks `SkipBilling` flag first. If the account has a `parent_id`, the parent's active plan is used.

Checked at VM creation (`execore/billing_status.go:checkCanCreateVM`), not at signup.

## Subscription Sync

`execore/subscription_poller.go` runs a 3-second poll loop calling `billing.Manager.SyncSubscriptions()`.

**Stripe events polled:**
- `customer.subscription.created`
- `customer.subscription.updated`
- `customer.subscription.deleted`

**Event mapping** (`billing/billing.go:subscriptionEventType`):

| Stripe Event | Subscription Status | billing_events.event_type |
|---|---|---|
| `deleted` | any | `canceled` |
| `created` | active, trialing | `active` |
| `updated` | active, trialing, past_due | `active` |
| `updated` | canceled, incomplete, incomplete_expired, unpaid | `canceled` |

After recording a billing event, `syncAccountPlan` updates `account_plans`: `active` -> insert `individual` plan, `canceled` -> insert `basic` plan.

**Lookback window:**
- Startup: 60 days back
- Runtime: tracks max event timestamp, fetches events created after it

**Duplicate handling:** Unique index on `(account_id, event_type, event_at)` with `INSERT OR IGNORE`.

## Credits

The billing system supports prepaid credits stored in microcents (1 cent = 10,000 microcents).

**Operations** (`billing/billing.go`):
- `GiftCredits` -- gift credits to an account (idempotent via `gift_id`)
- `SpendCredits` -- deduct credits for usage
- `BuyCredits` -- create a Stripe checkout session for one-time credit purchase
- `SyncCredits` -- poll Stripe for completed `credit_purchase` payment intents and record them
- `GetCreditState` -- returns paid/gift/used/total breakdown

**Rounding:** `tender.Value.Cents()` rounds positive fractional microcents up to the next cent so charges are never truncated below cost.

**Auto-recharge race:** Enablement checks are best-effort. A concurrent disable can allow one in-flight recharge attempt. Handle via normal Stripe refund if needed.

## Pricing

Stripe prices are referenced by lookup keys, not hardcoded price IDs.

**Default plan:** `individual` at $20/month (`billing/billing.go`).

`billing.Subscribe()` calls `lookupPriceIDCached()` which queries Stripe for active prices matching the key. Cached per API key (via `sync.OnceValue`). Lookup keys allow changing prices in the Stripe dashboard without code changes.

## Account IDs

Generated as `exe_` + 16 random chars (e.g., `exe_a1b2c3d4e5f6g7h8`). Created in `execore/exe-web-auth.go`. The `exe_` prefix distinguishes our accounts from Stripe's "customer" concept.

## User Flows

**Creating subscription (no active subscription):**
1. User attempts VM creation -> `/billing/update`
2. Redirect to Stripe Checkout (subscription mode)
3. User completes payment -> `/billing/success?session_id={CHECKOUT_SESSION_ID}`
4. Server calls `billing.VerifyCheckout(session_id)` to confirm
5. If verified: insert `billing_events` row with `event_type='active'`
6. User can now create VMs

**Managing subscription (active subscription):**
1. User visits `/billing/update`
2. `hasActiveSubscription()` queries Stripe for active/trialing/past_due subscriptions
3. If active: redirect to Stripe Billing Portal
4. User modifies subscription in portal
5. Poller detects event within ~3 seconds -> updates `billing_events` and `account_plans`

## Database Schema

```sql
CREATE TABLE billing_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id TEXT NOT NULL,
    event_type TEXT NOT NULL CHECK (event_type IN ('active', 'canceled')),
    event_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stripe_event_id TEXT,
    FOREIGN KEY (account_id) REFERENCES accounts(id)
);
CREATE UNIQUE INDEX idx_billing_events_unique
    ON billing_events(account_id, event_type, event_at);
CREATE UNIQUE INDEX idx_billing_events_stripe_event_id
    ON billing_events(stripe_event_id) WHERE stripe_event_id IS NOT NULL;

CREATE TABLE account_plans (
    account_id   TEXT     NOT NULL REFERENCES accounts(id),
    plan_id      TEXT     NOT NULL,
    started_at   DATETIME NOT NULL,
    ended_at     DATETIME,
    trial_expires_at DATETIME,
    changed_by   TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- Partial unique index: one active plan per account.
CREATE UNIQUE INDEX idx_account_plans_active
    ON account_plans(account_id) WHERE ended_at IS NULL;
```

## Implementation Files

| File | Purpose |
|------|---------|
| `billing/billing.go` | Stripe API: subscriptions, credits, checkout, sync |
| `billing/plan/entitlement.go` | Entitlement constants (VMCreate, LLMUse, etc.) |
| `billing/plan/plan.go` | Plan catalog, plan resolution logic |
| `billing/plan/tier.go` | Tier catalog, compute classes, disk/bandwidth quotas |
| `billing/tender/tender.go` | Microcent value type and arithmetic |
| `execore/subscription_poller.go` | 3-second poll loop for subscription events |
| `execore/billing_status.go` | `UserHasEntitlement()`, `checkCanCreateVM()` |
| `execore/exe-web-auth.go` | Account ID generation, billing enforcement on web |
| `exedb/schema/133-base.sql` | `billing_events` and `account_plans` tables |
