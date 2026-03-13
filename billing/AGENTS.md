# Package Purpose

Stripe integration for exe.dev. Manages subscriptions (access gating), billing credits (prepaid dollar balance), and checkout flows. Uses hand-written SQL with named parameters, NOT sqlc.

# Agents Available

- `exe-billing-credit` — Owns credit code paths (billing credits, LLM gateway credits, credit spending). Agent definition in `.ai/agents/exe-billing-credit.md`.
- `exe-billing-sub` — Owns Stripe subscriptions, products, and prices. Agent definition in `.ai/agents/exe-billing-sub.md`.
- `exe-billing-ux` — Owns the billing credit display UI on the user profile page. Agent definition in `.ai/agents/exe-billing-ux.md`.
- `stripe-expert` skill — Stripe API and webhook expertise. Skill definition in `.ai/skills/stripe-expert/SKILL.md`.
- `exe-billing` doc — Billing architecture overview. Doc in `.ai/docs/exe-billing.md`.

Install all with: `bin/agent-link install --pkg billing`

# General Rules

- `billing/` MUST use hand-written SQL in `const q = ...` blocks colocated with the method that uses them.
- SQL execution in `billing.Manager` MUST go through `m.exec(ctx, q, args...)` or `m.query(ctx, q, args...)`; do not introduce parallel DB helper stacks.
- SQL arguments MUST use named parameters (`@accountID`, `@amount`, etc.) and `sql.Named(...)` at callsites for readability and auditability.
- DB writes MUST be idempotent when syncing external Stripe state (`INSERT OR IGNORE`, unique keys, deterministic identifiers).
- Errors from DB and Stripe calls MUST be wrapped with operation context (`fmt.Errorf("insert credit ledger entry: %w", err)` style).
- Stripe request IDs MUST be logged when available for failed or significant external calls.
- Keep business logic in Go and SQL focused on persistence; do not move orchestration logic into SQL.
- Tests for billing DB behavior MUST exercise real query paths (no stubs for core query behavior) and validate idempotency/replay behavior.
- Any proposed `sqlc` usage in `billing/` is out of scope by default and requires explicit human approval.
