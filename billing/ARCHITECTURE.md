# Billing Architecture

For the current billing architecture, see [devdocs/BILLING.md](../devdocs/BILLING.md).

## Plans and Entitlements

All plans are defined in `billing/entitlement/plan.go`. Each plan grants a set of entitlements checked via `UserHasEntitlement`.

| Plan | Version | Price | Entitlements |
|------|---------|-------|-------------|
| VIP | `"vip"` | $0 | All (wildcard) |
| Individual | `"individual"` | $20/mo | `llm:use`, `credit:purchase`, `invite:request`, `team:create`, `vm:create`, `vm:connect`, `vm:run`. Signup bonus: $100 |
| Team | `"team"` | — | `llm:use`, `credit:purchase`, `invite:request`, `vm:create`, `vm:connect`, `vm:run` |
| Friend | `"friend"` | $0 | `llm:use`, `vm:create`, `vm:connect`, `vm:run` |
| Grandfathered | `"grandfathered"` | $0 | Same as Friend |
| Trial | `"trial"` | $0 | Same as Friend |
| Basic | `"basic"` | $0 | `llm:use`, `vm:connect` |
| Restricted | `"restricted"` | — | None |

## Credits Architecture

Credits are the currency users spend on LLM API requests. They are purchased via Stripe or granted as gifts, and debited automatically when LLM requests flow through the gateway.

The `billing_credits` ledger is the source of truth for all credit operations. All amounts are stored as integer microcents via `tender.Value` ($1 = 1,000,000 microcents).

> **Legacy:** `user_llm_credit` in exedb (float64 `available_credit`) is deprecated and being migrated. It still controls LLM gateway access and automatic refreshes during the transition.

### Crediting

Credits enter the ledger through purchases (Stripe) or gifts.

```
┌─────────────────────────────────────────────────────────┐
│                    Credit Sources                        │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  Stripe checkout ──► handleCreditsSuccess               │
│                          │                              │
│                          ▼                              │
│                      SyncCredits ──► billing_credits    │
│                                      stripe_event_id    │
│                                      credit_type = NULL │
│                                                         │
│  Debug UI ──► handleDebugGiftCredits ──┐                │
│  SSH cmd  ──► sudo-exe add-gift ───────┤                │
│  Upgrade  ──► giftSignupBonus() ───────┘                │
│                          │                              │
│                          ▼                              │
│                  billing.GiftCredits(billingID, params)  │
│                          │                              │
│                          ▼                              │
│                  INSERT OR IGNORE INTO billing_credits   │
│                  credit_type = 'gift', gift_id UNIQUE   │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

`GiftCredits` takes `AmountUSD` and a `GiftPrefix` — the billing package handles tender conversion and gift ID construction (`prefix:billingID:nanos`) internally. Callers never touch `tender.Value`.

Gift prefixes: `GiftPrefixDebug`, `GiftPrefixSignup`, `GiftPrefixSSH`.

### Debiting

LLM requests debit through a waterfall: gateway credits first, then the billing ledger.

```
┌─────────────────────────────────────────────────────────┐
│                  LLM Request Debit Flow                  │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  LLM request arrives at gateway                         │
│          │                                              │
│          ▼                                              │
│  CheckAndRefreshCreditDB(userID)                        │
│          │                                              │
│          ├── available > 0 ──► billingBacked = false     │
│          │                                              │
│          └── available ≤ 0                              │
│              ├── no billing account ──► 402             │
│              └── ledger balance > -$2 ──► billingBacked │
│                                            = true       │
│          │                                              │
│          ▼                                              │
│  Proxy to LLM provider (Anthropic/OpenAI/Fireworks)     │
│          │                                              │
│          ▼                                              │
│  debitResponseCredits(costUSD)                          │
│          │                                              │
│          ├── DebitCreditDB ──► user_llm_credit          │
│          │   available_credit -= costUSD (legacy)       │
│          │                                              │
│          └── if billingBacked && overage > 0            │
│              └── SpendCredits ──► billing_credits       │
│                  credit_type = 'usage'                  │
│                  hour_bucket = current hour             │
│                  ON CONFLICT: amount += negative        │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

Usage rows are bucketed by hour (`hour_bucket`). Each hour gets one row per account that accumulates debits. Accounts can go up to $2.00 negative (debt tolerance) before requests are rejected with 402.

### Automatic Refreshes

Gateway credits refresh lazily — there is no cron or background job. The refresh is evaluated inline on every LLM request during `CheckAndRefreshCreditDB`.

```
┌─────────────────────────────────────────────────────────┐
│              Gateway Credit Refresh Logic                 │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  CheckAndRefreshCreditDB(userID)                        │
│          │                                              │
│          ▼                                              │
│  planForUser(userID) ──► determines plan + Refresh fn   │
│          │                                              │
│          ▼                                              │
│  plan.Refresh(available, lastRefresh, now)              │
│          │                                              │
│          ├── has_billing (paid):                        │
│          │   different UTC month && available < $20?    │
│          │   └── reset to $20                           │
│          │                                              │
│          ├── friend / no_billing (free):                │
│          │   └── no refresh (flat $20 lifetime)         │
│          │                                              │
│          └── VIP (explicit overrides):                  │
│              different UTC month?                       │
│              └── reset to custom max_credit             │
│                                                         │
│  Refresh is per-user, stored in user_llm_credit:        │
│  available_credit, last_refresh_at, max_credit,         │
│  refresh_per_hour                                       │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

This entire refresh system lives in `llmgateway/credit.go` and is a candidate for extraction into the billing package (see WORK-10).

### Key files

| File | Role |
|------|------|
| `billing/billing.go` | `GiftCredits`, `SpendCredits`, `GetCreditState`, `ListGifts`, `Credits` interface |
| `billing/tender/tender.go` | `Value` type (microcents), `Mint`, arithmetic |
| `llmgateway/credit.go` | `CheckAndRefreshCreditDB`, `DebitCreditDB`, `planForUser`, refresh logic |
| `llmgateway/gateway.go` | Pre-flight credit check, billing-backed fallback |
| `llmgateway/accounting_transport.go` | Post-response debit, overage spill to billing ledger |
| `execore/credit_bar.go` | `computeCreditBar`, `giftsFromLedger` |
| `execore/exe-web-auth.go` | `giftSignupBonus` (on Stripe checkout) |
| `execore/debugsrv.go` | Debug gift endpoint |
| `execore/ssh_add_gift_command.go` | `sudo-exe add-gift` |

## Entitlements Architecture

Entitlements are boolean feature gates that control what features an account has access to (e.g. VM creation, credit purchases, LLM gateway). This is not an authorization system — it determines feature availability per plan, not user permissions. Each plan grants a fixed set of entitlements, checked at request time via `UserHasEntitlement`.

### Plan Resolution

Plan resolution uses the `account_plans` table. Every user has an account (created at signup), and every account has exactly one active plan row (`ended_at IS NULL`). For team members, the parent account's plan is used instead.

```
User request (SSH/HTTP)
         │
         ▼
  UserHasEntitlement(source, entitlement, userID)
         │
         ▼
  GetActivePlanForUser(userID)
  ┌──────────────────────────────────────────────────────┐
  │                                                      │
  │  users ──► accounts (via created_by)                 │
  │              │                                       │
  │              ├── parent_id IS NULL                    │
  │              │   └── own account_plans row            │
  │              │       (WHERE ended_at IS NULL)         │
  │              │                                        │
  │              └── parent_id IS NOT NULL (team member)  │
  │                  └── parent's account_plans row       │
  │                      (WHERE ended_at IS NULL)         │
  │                                                      │
  │  Returns: plan_id, account_id                        │
  └──────────────────────────────────────────────────────┘
         │
         ▼
  PlanGrants(plan_id, entitlement)
  ┌──────────────────────────────────────────────────────┐
  │                                                      │
  │  plan_id ──► Plan.Entitlements map                   │
  │                                                      │
  │  "vip"        ──► All: true (wildcard)               │
  │  "individual" ──► all 7 entitlements                 │
  │  "team"       ──► all except team:create             │
  │  "friend"     ──► llm:use, vm:create/connect/run    │
  │  "trial"      ──► same as friend                    │
  │  "basic"      ──► llm:use, vm:connect only          │
  │  "restricted" ──► nothing                            │
  │                                                      │
  │  Granted? ──► allow request                          │
  │  Denied?  ──► log + reject                           │
  └──────────────────────────────────────────────────────┘
```

### Account Hierarchy

```
  Individual account          Team billing owner
  ┌────────────────┐          ┌────────────────┐
  │ id: exe_abc    │          │ id: exe_team1  │
  │ parent_id: NULL│          │ parent_id: NULL│
  │ plan: individual          │ plan: individual
  └────────────────┘          └───────┬────────┘
                                      │ parent_id
                              ┌───────┴────────┐
                              │ id: exe_member1│
                              │ parent_id:     │
                              │   exe_team1    │
                              │ plan: basic    │  ◄── own plan ignored,
                              └────────────────┘      parent's plan used
```

Team members' entitlements are resolved through the parent account's plan. The member's own plan row (`basic`) is not used for entitlement checks — `GetActivePlanForUser` follows `parent_id` and returns the parent's active plan.

### Plan Lifecycle

Plans change via `account_plans` rows (append-only history):

```
  Signup (SSH/OAuth/email)      Stripe checkout success
  ┌─────────────────────┐      ┌─────────────────────┐
  │ createAccountWith   │      │ syncAccountPlan      │
  │ BasicPlan           │      │ (subscription poller)│
  │                     │      │                      │
  │ INSERT account      │      │ Close current plan   │
  │ INSERT account_plan │      │ (set ended_at)       │
  │ plan_id = "basic"   │      │                      │
  │ changed_by =        │      │ INSERT account_plan  │
  │   "system:signup"   │      │ plan_id = "individual│
  └─────────────────────┘      │ changed_by =         │
                               │   "stripe:event"     │
  Invite code applied          └─────────────────────┘
  ┌─────────────────────┐
  │ applyInviteCode     │      Cancellation
  │                     │      ┌─────────────────────┐
  │ Close basic plan    │      │ syncAccountPlan      │
  │ INSERT account_plan │      │                      │
  │ plan_id = "trial"   │      │ Close current plan   │
  │   or "friend"       │      │ INSERT account_plan  │
  │ changed_by =        │      │ plan_id = "basic"    │
  │   "system:invite"   │      │ changed_by =         │
  └─────────────────────┘      │   "stripe:event"     │
                               └─────────────────────┘
```

### Key files

| File | Role |
|------|------|
| `billing/entitlement/plan.go` | Plan catalog, `PlanGrants`, `PlanQuotas`, `GetPlanByID` |
| `billing/entitlement/entitlement.go` | Entitlement type definitions (`VMCreate`, `LLMUse`, etc.) |
| `execore/billing_status.go` | `UserHasEntitlement` — main entitlement check used by request handlers |
| `exedb/query/accounts.sql` | `GetActivePlanForUser` — SQL that walks account hierarchy |
| `execore/subscription_poller.go` | `syncAccountPlan` — keeps account_plans in sync with Stripe |

## Three Billing Systems

### 1. Subscriptions (Access Gating)
Controls whether a user can access exe.dev. Managed via Stripe subscriptions.
The subscription poller (`execore/subscription_poller.go`) syncs status from Stripe every ~5 minutes and calls `syncAccountPlan` to keep `account_plans` in sync.
Subscriptions can be created via Stripe Checkout or directly in the Stripe dashboard.

### 2. Billing Credits (Prepaid Balance)
Prepaid balance for LLM usage, denominated in microcents (1 USD = 100,000,000 microcents).
Purchased via Stripe Checkout, consumed by LLM requests via the spending waterfall.
Accounts can go up to $2.00 negative (debt tolerance).

### 3. LLM Gateway Credits (Per-User Quota)
Rate-limiting mechanism for LLM API proxy usage. Not purchasable — allocated by plan tier.
Refreshes monthly for paid users, flat lifetime grant for free users.

## Spending Waterfall (LLM Requests)
1. Gateway credits first (per-user quota)
2. Billing credits second (per-account prepaid balance)
3. Debt up to $2.00 tolerated
4. 402 rejection if everything is exhausted

## VIP
VIP users have a `vip` plan in `account_plans` and per-user overrides
in the `user_llm_credit` table for custom `max_credit` and `refresh_per_hour`.
Can be granted via:
- Invite codes with `plan_type='free'`
- Debug admin endpoints (Tailscale/localhost only)
