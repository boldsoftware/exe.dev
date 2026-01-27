# Billing

exe.dev uses Stripe for subscriptions. Access is granted based on subscription status from polled events.

## Access Control

Authorization happens in `execore/exe-web-auth.go:userNeedsBilling()`.

**Access granted when:**
- `billing_status = 'active'` (has active subscription)
- Created before 2026-01-06 23:10 UTC (grandfathered)
- `billing_exemption = 'free'`
- `billing_exemption = 'trial'` with future `billing_trial_ends_at`

**Access denied when:**
- `billing_status = 'canceled'` (always, overrides exemptions)
- None of the above conditions met

Checked at VM creation (`execore/exe-web-auth.go:193`), not at signup.

## Event Polling

`execore/subscription_poller.go` polls Stripe every 3 seconds for:
- `customer.subscription.created`
- `customer.subscription.updated`
- `customer.subscription.deleted`

Maps Stripe events to billing_events.event_type:
- `deleted` → `canceled`
- `created` → `active` (only if status is active/trialing)
- `updated` → `active` or `canceled` based on subscription status

**Lookback window:**
- At process startup: always 60 days (never consults billing_events table)
- During runtime: tracks max event timestamp from last poll, fetches events `created > max_timestamp`

**Duplicate handling:**
- Unique index on `(account_id, event_type, event_at)` prevents duplicate storage
- `INSERT OR IGNORE` silently skips duplicates from overlapping polls or delayed events
- No explicit overlap window; deduplication handles events arriving out of order

## Pricing

Stripe prices are referenced by lookup keys, not hardcoded price IDs.

**Default plan:** `individual` (defined in `billing/billing.go:30`)

When creating checkout sessions, `billing.Subscribe()` calls `lookupPriceID(lookupKey)` which queries Stripe's API for active prices matching the key. The price ID is cached per API key.

**Why lookup keys:** Allows changing price amounts or creating new prices in Stripe dashboard without code changes. Just reassign the lookup key `individual` to the new price ID in Stripe.

## Account IDs

Account IDs are generated with `exe_` prefix (e.g., `exe_a1b2c3d4e5f6g7h8`).

**Why custom IDs:** Stripe uses "customers" but we bill "accounts". The `exe_` prefix maintains separation between Stripe's concept and ours, and makes our accounts instantly identifiable in Stripe dashboard.

Generated in `exe-web-auth.go` when creating accounts (`exe_` + 16 random chars).

## Database Schema

```sql
CREATE TABLE billing_events (
    account_id TEXT NOT NULL,
    event_type TEXT NOT NULL CHECK (event_type IN ('active', 'canceled')),
    event_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (account_id) REFERENCES accounts(id)
);

CREATE UNIQUE INDEX idx_billing_events_unique
    ON billing_events(account_id, event_type, event_at);
```

Latest event's `event_type` per `account_id` determines billing status. Unique index prevents duplicate events.

## User Flows

**Creating subscription (no active subscription):**
1. User attempts VM creation → `/billing/update`
2. Redirect to Stripe Checkout (subscription mode)
3. User completes payment → Stripe redirects to `/billing/success?session_id={CHECKOUT_SESSION_ID}`
4. Server calls `billing.VerifyCheckout(session_id)` to confirm payment
5. If verified: insert `billing_events` row with `event_type='active'`
6. User can now create VMs

**Managing subscription (active subscription):**
1. User visits `/billing/update` or profile page
2. Server checks `hasActiveSubscription()` via `billing_events`
3. If active: redirect to Stripe Billing Portal (not Checkout)
4. User cancels/updates subscription in portal
5. Poller detects event within 3 seconds → updates `billing_events`
6. Access revoked immediately (checked at next VM creation attempt)

**Signup:**
- No billing required at signup
- Billing checked only when creating VMs (`exe-web-auth.go:193`)

## Implementation Files

- `billing/billing.go` - Stripe API integration, subscription events iterator
- `execore/subscription_poller.go` - Polls Stripe, writes to billing_events
- `execore/billing_status.go` - Access control logic
- `execore/exe-web-auth.go` - Billing enforcement (lines 190-202)
- `exedb/schema/066-billing-events.sql` - Table schema
