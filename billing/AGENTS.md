# Package Purpose

Stripe integration for exe.dev. Manages subscriptions (access gating), billing credits (prepaid dollar balance), and checkout flows. Uses hand-written SQL with named parameters, NOT sqlc.

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

**Why:** `Grants` is the single entry point for capability checks. It resolves the correct tier from any plan ID (4-part tier ID, 3-part legacy, or bare category) and applies tier-override → plan-fallback logic. Plan structure may change. New plans may be added. Checking entitlements keeps the code future-proof and centralizes capability definitions in the plan catalog.

**Exceptions:** The only acceptable plan category checks are:
- **Display only:** Debug/admin UI showing plan name/status, UI buttons that link to billing portal
- **Analytics:** Metrics tracking plan distribution, conversion rates
- **Migrations:** Backfill scripts dealing with historical data
- **Webhooks/sync:** Determining which plan_id to assign when syncing external events

Examples of **acceptable** plan checks:
```go
// OK: UI display logic
showBillingPortalLink := (cat == CategoryIndividual)

// OK: Debug admin UI
canGrantTrial := (cat == CategoryBasic || cat == CategoryRestricted)

// OK: Sync logic determining new plan
newPlan := CategoryIndividual  // when user subscribes
```

If you're checking a plan to **allow or deny an action**, that's wrong. Use entitlements.

**Red flags that indicate you're doing it wrong:**
- Returning different API responses based on plan
- Allowing/blocking feature access based on plan
- Changing limits or quotas based on plan
- Showing/hiding features based on plan (unless it's just the billing link itself)

If you find yourself about to write `if planCategory == ...` to make a capability decision, **STOP** and define/check an entitlement instead.

## SQL and Database

- `billing/` MUST use hand-written SQL in `const q = ...` blocks colocated with the method that uses them.
- SQL execution in `billing.Manager` MUST go through `m.exec(ctx, q, args...)` or `m.query(ctx, q, args...)`; do not introduce parallel DB helper stacks.
- SQL arguments MUST use named parameters (`@accountID`, `@amount`, etc.) and `sql.Named(...)` at callsites for readability and auditability.
- DB writes MUST be idempotent when syncing external Stripe state (`INSERT OR IGNORE`, unique keys, deterministic identifiers).
- Errors from DB and Stripe calls MUST be wrapped with operation context (`fmt.Errorf("insert credit ledger entry: %w", err)` style).
- Stripe request IDs MUST be logged when available for failed or significant external calls.
- Keep business logic in Go and SQL focused on persistence; do not move orchestration logic into SQL.
- Tests for billing DB behavior MUST exercise real query paths (no stubs for core query behavior) and validate idempotency/replay behavior.
- Any proposed `sqlc` usage in `billing/` is out of scope by default and requires explicit human approval.
