# Billing Architecture

For user-facing billing docs, see [devdocs/billing.md](../devdocs/billing.md).

## Plans

All plans are defined in `billing/plan/plan.go`. Each plan has a `Category`, a versioned `ID`, entitlements, and a `DefaultTier`.

| Plan | ID | Paid | Entitlements |
|------|-----|------|--------------|
| Enterprise | `enterprise:monthly:20260106` | Yes | LLM, credits, invites, VM create/run, disk resize |
| Team | `team:monthly:20260106` | Yes | LLM, credits, invites, VM create/run, disk resize |
| Individual | `individual:monthly:20260106` | Yes | LLM, credits, invites, **teams**, VM create/run, disk resize |
| Friend | `friend` | No | LLM, VM create/run, disk resize |
| Grandfathered | `grandfathered` | No | Same as Friend |
| Trial | `trial:monthly:20260106` | No | Same as Friend |
| Basic | `basic:monthly:20260106` | No | LLM only |
| Restricted | `restricted` | No | None |

Only Individual gets `TeamCreate`.

### Versioned Plan IDs

Plan IDs use a colon-separated format:
- **Bare category:** `individual`, `friend`
- **3-part legacy:** `individual:monthly:20260106`
- **4-part tier:** `individual:medium:monthly:20260106`

`ParseID()` extracts `(category, interval, version)`. `Base()` extracts just the category.

### Plan Resolution

`plan.ForUser()` is the canonical way to determine a user's plan. Priority:

1. `canceled` billing status → Basic
2. `plan_id` is `"friend"` or `"free"` → Friend
4. Team billing active → Team
5. Active billing → Individual
6. Trial not expired → Trial
7. Created before 2026-01-06 23:10 UTC → Grandfathered
8. Default → Basic

## Tiers

Tiers live in `billing/plan/tier.go`. Each plan has a `DefaultTier`; Individual has four compute tiers.

Tier IDs use 4-part format: `{category}:{tier}:{interval}:{version}`

### Individual Tiers

| Tier | Compute | Disk | Max Disk | Bandwidth | Max VMs |
|------|---------|------|----------|-----------|----------|
| Small | 2 CPU / 8 GB | 25 GB | 75 GB | 100 GB | 50 |
| Medium | 4 CPU / 16 GB | 25 GB | 75 GB | 100 GB | 50 |
| Large | 8 CPU / 32 GB | 25 GB | 75 GB | 100 GB | 50 |
| XLarge | 16 CPU / 64 GB | 25 GB | 75 GB | 100 GB | 50 |

All other plans have a single `default` tier. Tier entitlements are `nil` (inherit from parent plan) unless explicitly overridden.

### Tier Resolution

`getTierByID()` handles all ID formats:
1. Direct lookup in the `tiers` map (4-part ID)
2. Fallback: extract category via `Base()`, look up the plan's `DefaultTier`

`Grants(planID, entitlement)` resolves the tier, then checks tier-level overrides before falling back to plan entitlements.

## Entitlements

Entitlements are boolean feature gates defined in `billing/plan/entitlement.go`. Each is a struct with `ID` and `DisplayName`:

| Entitlement | ID | Description |
|-------------|-----|-------------|
| LLMUse | `llm:use` | Use LLM Gateway |
| CreditPurchase | `credit:purchase` | Purchase Credits |
| InviteRequest | `invite:request` | Request Invites |
| TeamCreate | `team:create` | Create Teams |
| VMCreate | `vm:create` | Create VMs |
| VMRun | `vm:run` | Run VMs |
| DiskResize | `disk:resize` | Resize VM Disks |
| All | `*` | Wildcard (reserved) |

Checked via `execore/billing_status.go:UserHasEntitlement()`, which resolves user → account → active plan → tier → entitlements. For team members, the parent account's plan is used.

## Credits

Credits are prepaid balance for LLM usage, stored as integer microcents via `tender.Value` (1 USD = 1,000,000 microcents, 1 cent = 10,000 microcents).

The `Credits` interface (`billing/credits.go`) provides: `GiftCredits`, `SpendCredits`, `CreditBalance`, `GetCreditState`, `ListGifts`.

### Crediting

Credits enter via purchases (Stripe) or gifts.

- **Purchases:** `BuyCredits` creates a Stripe checkout session. `SyncCredits` polls for completed payment intents and inserts paid credit rows.
- **Gifts:** `GiftCredits(billingID, params)` takes `AmountUSD` and a `GiftPrefix`. The billing package handles tender conversion and gift ID construction (`prefix:billingID:nanos`). Idempotent via `INSERT OR IGNORE` on `gift_id`.

Gift prefixes: `GiftPrefixDebug`, `GiftPrefixSignup`, `GiftPrefixSSH`.

### Debiting

LLM requests debit through a waterfall:
1. **Gateway credits** (per-user quota in `user_llm_credit`, managed by `llmgateway/credit.go`)
2. **Billing credits** (per-account prepaid balance in `billing_credits`, via `SpendCredits`)
3. **Debt tolerance** up to $2.00
4. **402 rejection** if everything is exhausted

Usage rows are bucketed by hour (`hour_bucket`). Each hour gets one row per account that accumulates debits.

### Gateway Credit Refresh

Gateway credits refresh lazily on every LLM request via `CheckAndRefreshCredit` in `llmgateway/credit.go`:
- **Paid users:** Monthly reset to plan's `MonthlyLLMCreditUSD` (e.g. $20 for Individual, $500 for Enterprise/Team/Friend)
- **Free users:** No refresh (flat lifetime grant)

## Subscription Sync

`execore/subscription_poller.go` polls Stripe every 3 seconds via `SyncSubscriptions`. Events are recorded in `billing_events` (idempotent via unique index). `syncAccountPlan` updates `account_plans`:
- `active` event → insert plan row matching the subscription
- `canceled` event → insert `basic` plan row

Skips duplicate updates when the active plan's base category already matches.

`HandleWebhook` (`billing/webhook.go`) stores raw Stripe payloads but does not process them inline — processing happens via the poller.

## Key Files

| File | Role |
|------|------|
| `billing/billing.go` | `Manager`: Subscribe, VerifyCheckout, SyncSubscriptions, syncAccountPlan, pricing, plan migration |
| `billing/invoices.go` | Invoice listing, upcoming invoice preview, credit balance, discounts |
| `billing/payment_method.go` | Payment method retrieval, card/link/PayPal formatting |
| `billing/credits.go` | `Credits` interface: GiftCredits, SpendCredits, BuyCredits, SyncCredits |
| `billing/webhook.go` | Stripe webhook signature verification and storage |
| `billing/plan/plan.go` | Plan catalog, `ForUser()` resolution, versioned plan IDs |
| `billing/plan/entitlement.go` | Entitlement types, `Grants()` |
| `billing/plan/tier.go` | Tier catalog, compute classes, quotas, disk/bandwidth helpers |
| `billing/tender/tender.go` | `Value` type (microcents), `Mint`, arithmetic |
| `execore/billing_status.go` | `UserHasEntitlement`, `checkCanCreateVM` |
| `execore/subscription_poller.go` | 3-second poll loop for Stripe subscription events |
| `execore/credit_bar.go` | Credit bar UI computation |
| `llmgateway/credit.go` | Gateway credit manager: refresh, debit, plan-based quotas |
| `llmgateway/accounting_transport.go` | HTTP transport for LLM proxy credit enforcement |
