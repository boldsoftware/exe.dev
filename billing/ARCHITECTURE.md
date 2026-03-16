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
