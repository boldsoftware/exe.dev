# Package Purpose

Stripe integration for exe.dev. Manages subscriptions (access gating), billing credits (prepaid dollar balance), and checkout flows. Uses hand-written SQL with named parameters, NOT sqlc.

# Agent Team

## Lead

- `exe-billing-lead` — Billing architecture lead. Owns overall design, makes architecture decisions, sets direction for the team. Agent definition in `.ai/agents/exe-billing-lead.md`.

## Engineers

- `exe-entitlements` — Owns the entitlement system: plan catalog, plan resolution, entitlement constants. Agent definition in `.ai/agents/exe-entitlements.md`.
- `exe-credits` — Owns the credit system: credit ledger, tender types, credit bar display, credit purchase flows. Agent definition in `.ai/agents/exe-credits.md`.
- `exe-billing` — Owns the Stripe integration: subscriptions, checkout, webhook sync, price management. Agent definition in `.ai/agents/exe-billing.md`.
- `exe-internal` — Owns internal/debug billing tooling: admin UI, credit adjustments, billing debug endpoints. Agent definition in `.ai/agents/exe-internal.md`.

## QA

- `exe-qa` — Code review and testing. Owns all billing test files, reviews PRs, writes tests. Agent definition in `.ai/agents/exe-qa.md`.

## How the Team Works

- Engineers ask **exe-billing-lead** for direction on architecture and design decisions.
- Engineers request code review from **exe-qa** before merging.
- **exe-qa** reports coverage gaps and quality issues to **exe-billing-lead**.
- **exe-billing-lead** escalates product-level questions to the human (Bryan).

# General Rules

## Critical: Entitlement Checks, Not Plan Names

**NEVER write code that checks plan names or categories when an entitlement check would suffice.**

❌ **WRONG:**
```go
if planCategory == entitlement.CategoryIndividual {
    // allow invites
}
```

✅ **CORRECT:**
```go
if entitlement.PlanGrants(planCategory, entitlement.InviteRequest) {
    // allow invites
}
```

**Why:** Plan structure may change. New plans may be added. An "individual" user might be on a team tomorrow. Checking entitlements keeps the code future-proof and centralizes capability definitions in the plan catalog.

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
