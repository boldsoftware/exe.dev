# Billing Architecture

For the current billing architecture, see [devdocs/BILLING.md](../devdocs/BILLING.md).
What follows is a proposed evolution of the billing system.

## Plans and Entitlements (WIP)

> The entitlement identifiers (e.g. `access:exempt`, `llm:friend`) are proposed names
> and do not exist in the code yet. The behavior they describe is real.

| Plan | Identifier | Price | Stripe Price ID | Lookup Key | Stripe Product | Platform Access | LLM Max Credit | LLM Refresh | LLM Monthly Top-up | VM Compute | Team Billing | Upgrade Bonus |
|------|-----------|-------|----------------|------------|---------------|----------------|---------------|-------------|-------------------|------------|-------------|--------------|
| Free | `"free"` | $0 | — | — | — | `access:exempt` | `llm:friend` ($100) | `llm:refresh:5/hr` | — | `compute:credits` | — | — |
| Trial | `"trial"` | $0 (1 month) | — | — | — | `access:trial` | `llm:no_billing` ($50) | `llm:refresh:1/hr` | — | `compute:credits` | — | — |
| Individual | `"individual"` | $20/mo | `price_1SwI3WGWIXq1kJnorPaqvufu` | `individual` | `prod_Tu6G7ryKeSRsqx` | `access:subscription` | `llm:has_billing` ($100) | `llm:refresh:5/hr` | `llm:topup:20/mo` | `compute:credits` | — | `llm:upgrade_bonus:100` |
| Team | `"team"` | ??? | — | — | — | `access:subscription` | `llm:has_billing` ($100) | `llm:refresh:5/hr` | `llm:topup:20/mo` | `compute:credits` | `team:billing_cover` | `llm:upgrade_bonus:100` |
| VIP | `"vip"` | $0 | — | — | — | `access:exempt` | per-user override | per-user override | — | `compute:credits` | — | — |
| Grandfathered | `"grandfathered"` | $0 | — | — | — | `access:legacy` | `llm:no_billing` ($50) | `llm:refresh:1/hr` | — | `compute:credits` | — | — |

## Entitlement Migration Status

Tracks which entitlements use `UserHasEntitlement` (new) vs `userNeedsBilling`/`GetUserBillingStatus` (old).

| Entitlement | `UserHasEntitlement` | Old Logic |
|-------------|:---:|:---:|
| `vm:create` | ✅ | |
| `vm:connect` | | ❌ |
| `llm:use` | | ❌ |
| `credit:renew` | | ❌ |
| `credit:purchase` | | ❌ |
| `credit:refresh` | | ❌ |
| `compute:spend` | | ❌ |
| `compute:purchase` | | ❌ |
| `compute:debt` | | ❌ |
| `compute:on_demand` | | ❌ |
| `admin:override` | | ❌ |

## Plan Migration Status

Tracks whether each plan is a real, code-defined plan or just a label we use to describe existing ad-hoc logic.

| Plan | In `GetPlanVersion` | In Stripe | Shown in UI | Notes |
|------|:---:|:---:|:---:|-------|
| Individual | ✅ | ✅ | ✅ | Only self-serve plan with a Stripe price |
| Team | | | ✅ | Display-only override in profile when user is on a team. No `VersionTeam` resolution yet — team members resolve to `Individual` via `has_billing` category |
| VIP | ✅ | | | Friend + explicit overrides. No UI surface |
| Friend | ✅ | | | `billing_exemption='free'` without overrides |
| Grandfathered | ✅ | | ✅ | Created before 2026-01-06, no billing |
| Invite | ✅ | | | Trial exemption with valid expiry |
| Basic | ✅ | | ✅ | Default fallback. Shows "Subscribe" in UI |

## Credits Architecture

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

```
User request (SSH/HTTP)
         │
         ▼
  Resolve plan version
  ┌────────────────────────────────────────┐
  │  GetUserBilling()                      │  ◄── SQL: billing_exemption, billing_status,
  │  GetPlanVersion(UserPlanInputs)        │       created_at, team_billing_active
  │                                        │
  │  Canceled?      ──► Basic              │
  │  Friend + overrides? ──► VIP           │
  │  Friend?        ──► Friend             │
  │  Has billing?   ──► Individual         │
  │  Team billing?  ──► Team               │
  │  Trial + valid? ──► Invite             │
  │  Old user?      ──► Grandfathered      │
  │  Default        ──► Basic              │
  └────────────────────────────────────────┘
         │
         ▼
  Check entitlement
  ┌────────────────────────────────────────┐
  │  PlanGrants(version, entitlement)      │
  │                                        │
  │  Plan ──► Entitlements map             │
  │  VIP  ──► All: true (wildcard)         │
  │  Basic ──► LLMUse, CreditRefresh,      │
  │            VMConnect only              │
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
| `execore/billing_status.go` | `UserHasEntitlement` — main entitlement check used by request handlers |

## Three Billing Systems

### 1. Subscriptions (Access Gating)
Controls whether a user can access exe.dev. Managed via Stripe subscriptions.
The subscription poller (`execore/subscription_poller.go`) syncs status from Stripe every ~5 minutes.
Subscriptions can be created via Stripe Checkout or directly in the Stripe dashboard.

### 2. Billing Credits (Prepaid Balance)
Prepaid balance for VM compute usage, denominated in microcents (1 USD = 100,000,000 microcents).
Purchased via Stripe Checkout, consumed per-minute by VM usage.
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
A user can access the platform if ANY of these are true:
- Active subscription (`status = active/trialing`)
- `billing_exemption = 'free'` (Free/VIP)
- `billing_exemption = 'trial'` (Trial, within expiration)
- Created before 2026-01-06 (Grandfathered)
- Team's `billing_owner` has active billing

## VIP
VIP users get `billing_exemption='free'` for access and per-user overrides
in the `user_llm_credit` table for custom `max_credit` and `refresh_per_hour`.
Can be granted via:
- Invite codes with `plan_type='free'`
- Debug admin endpoints (Tailscale/localhost only)
- Creating a subscription directly in the Stripe dashboard
