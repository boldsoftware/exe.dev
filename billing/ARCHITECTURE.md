# Billing Architecture

For the current billing architecture, see [devdocs/BILLING.md](../devdocs/BILLING.md).
What follows is a proposed evolution of the billing system.

## Plans and Entitlements

All plans are defined in `billing/entitlement/plan.go`. Each plan grants a set of entitlements checked via `UserHasEntitlement`.

| Plan | Version | Price | Entitlements |
|------|---------|-------|-------------|
| VIP | `"vip"` | $0 | All (wildcard) |
| Restricted | `"restricted"` | — | None |
| Team | `"team"` | — | `llm:use`, `credit:purchase`, `invite:request`, `vm:create`, `vm:connect`, `vm:run` |
| Individual | `"individual"` | $20/mo | Same as Team + `team:create`. Signup bonus: $100 |
| Friend | `"friend"` | $0 | `llm:use`, `vm:create`, `vm:connect`, `vm:run` |
| Grandfathered | `"grandfathered"` | $0 | Same as Friend |
| Invite | `"invite"` | $0 (trial) | Same as Friend |
| Basic | `"basic"` | $0 | `llm:use`, `vm:connect` |

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

```
User request (SSH/HTTP)
         │
         ▼
  Resolve plan version
  ┌────────────────────────────────────────┐
  │  GetUserBilling()                      │  ◄── SQL: billing_exemption, billing_status,
  │  GetPlanVersion(UserPlanInputs)        │       created_at, team_billing_active
  │                                        │
  │  Canceled?           ──► Basic         │
  │  Friend + overrides? ──► VIP           │
  │  Friend?             ──► Friend        │
  │  Team billing?       ──► Team          │
  │  Has billing?        ──► Individual    │
  │  Trial + valid?      ──► Invite        │
  │  Old user?           ──► Grandfathered │
  │  Default             ──► Basic         │
  └────────────────────────────────────────┘
         │
         ▼
  Check entitlement
  ┌────────────────────────────────────────┐
  │  PlanGrants(version, entitlement)      │
  │                                        │
  │  Plan ──► Entitlements map             │
  │  VIP  ──► All: true (wildcard)         │
  │  Restricted ──► nothing                │
  │  Basic ──► LLMUse, VMConnect only      │
  └────────────────────────────────────────┘
```

Plans are defined in `billing/entitlement/plan.go` as a static map. Each plan has:

- `Version` — identifier (e.g. `"individual"`)
- `Name` — display name (e.g. `"Individual"`)
- `Entitlements` — `map[Entitlement]bool` for boolean feature gates
- `Quotas` — `PlanQuotas` struct for numeric values (e.g. `SignupBonusCreditUSD`)

### Key files

| File | Role |
|------|------|
| `billing/entitlement/plan.go` | Plan definitions, `GetPlanVersion`, `PlanGrants`, `PlanQuotas` |
| `billing/entitlement/entitlement.go` | Entitlement type definitions (`VMCreate`, `LLMUse`, etc.) |
| `execore/billing_status.go` | `UserHasEntitlement`, `teamBillingCovers` — main entitlement check used by request handlers |

## Three Billing Systems

### 1. Subscriptions (Access Gating)
Controls whether a user can access exe.dev. Managed via Stripe subscriptions.
The subscription poller (`execore/subscription_poller.go`) syncs status from Stripe every ~5 minutes.
Subscriptions can be created via Stripe Checkout or directly in the Stripe dashboard.

### 2. Billing Credits (Prepaid Balance)
Prepaid balance for LLM usage, denominated in microcents (1 USD = 100,000,000 microcents).
Purchased via Stripe Checkout, consumed by LLM requests via the spending waterfall.
Accounts can go up to $2.00 negative (debt tolerance).

### 3. LLM Gateway Credits (Per-User Quota)
Rate-limiting mechanism for LLM API proxy usage. Not purchasable — allocated by plan tier.
Refreshes per hour based on plan. See table above for rates.

## Spending Waterfall (LLM Requests)
1. Gateway credits first (per-user quota)
2. Billing credits second (per-account prepaid balance)
3. Debt up to $2.00 tolerated
4. 402 rejection if everything is exhausted

## Access Gating
Access is controlled by `UserHasEntitlement`. A user can access the platform if their plan grants the relevant entitlement (e.g. `vm:create` for VM creation). Plan resolution uses `GetPlanVersion` which examines billing status, exemptions, team membership, and account age.

## VIP
VIP users get `billing_exemption='free'` for access and per-user overrides
in the `user_llm_credit` table for custom `max_credit` and `refresh_per_hour`.
Can be granted via:
- Invite codes with `plan_type='free'`
- Debug admin endpoints (Tailscale/localhost only)
- Creating a subscription directly in the Stripe dashboard
