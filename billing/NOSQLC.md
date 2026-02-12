# Why Billing Avoids `sqlc`

Agents perform better when logic and SQL are co-located and the edit graph is
shallow.

Billing is a correctness boundary, not just a query surface. The code has to
enforce idempotency, replay safety, and accounting invariants under failure.
When SQL is next to the decision that uses it, the full invariant is visible in
one read and changed in one edit. That lowers review time, conflict risk, and
the chance of subtle behavioral drift.

The SQLC conversion point for billing was commit `5c3b6e1a`.
The billing-only diff looked small (`billing/billing.go`, `+26/-12`), but the
same commit’s full diff was `18 files` and `+1434/-188`. That is coupling
cost, not simplification. A representative pre-conversion billing edit
(`c00ca430`) was `2 files`, `+8/-8`, entirely local to billing and tests.

In the current no-SQLC billing package (`9e0a6e7d..HEAD`), billing has 38 commits;
only 8 touched `exedb/` or `sqlc/query/`. Most behavior work stayed local, and
that locality is exactly what high-velocity agent and human iteration needs.
Across those 38 commits, `sqlc/query/` was touched exactly zero times. The
coupling that remains is schema-level (`exedb/`) where it belongs, not a
generated access layer leaking into routine billing edits.

Those 8 coupled commits are not persistent generator drag either: three are
net-deletion/revert corrections, including removal of a bad credit-event path
in `10825d98`. That pattern supports the same conclusion: local ownership in
billing makes course-correction cheaper and safer.

This is not anti-`sqlc`; it is tool fit. Where queries are broad, shared, and
stable, generated accessors can help. Billing is the opposite: policy-heavy,
fast-moving, and failure-driven. Here, explicit SQL in the package is the
cleaner system.
