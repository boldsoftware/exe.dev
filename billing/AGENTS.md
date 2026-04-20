# Package Purpose

Stripe integration for exe.dev. Manages subscriptions (access gating), billing credits (prepaid dollar balance), plan tiers with compute/disk/bandwidth quotas, and checkout flows.

# General Rules

## Critical: Entitlement Checks, Not Plan Names

**NEVER write code that checks plan names or categories when an entitlement check would suffice.**

❌ **WRONG:**
```go
if planCategory == plan.CategoryIndividual {
    // allow invites
}
```

✅ **CORRECT:**
```go
if plan.Grants(planID, plan.InviteRequest) {
    // allow invites
}
```

**Why:** `Grants` resolves the tier from any plan ID format (4-part tier, 3-part legacy, or bare category) and applies tier-override → plan-fallback logic. Checking entitlements keeps the code future-proof.

**Exceptions:** Plan category checks are acceptable for:
- **Display:** Debug/admin UI showing plan name/status
- **Analytics:** Metrics tracking plan distribution
- **Migrations:** Backfill scripts dealing with historical data
- **Webhooks/sync:** Determining which plan_id to assign when syncing external events

If you're checking a plan to **allow or deny an action**, use entitlements.

## SQL and Database

- All DB access goes through **sqlc-generated queries** via the `exedb` package. SQL lives in `exedb/query/*.sql`.
- Use `exedb.WithTx` / `exedb.WithTx1` for writes, `exedb.WithRxRes0` / `exedb.WithRxRes1` for reads.
- DB writes MUST be idempotent when syncing external Stripe state (`INSERT OR IGNORE`, unique keys, deterministic identifiers).
- Errors from DB and Stripe calls MUST be wrapped with operation context.
- Stripe request IDs MUST be logged when available for failed or significant external calls.
