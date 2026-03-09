# Known Issues & Future Work

## ProxyRegistry global RLock vs. multi-agent

`ProxyRegistry` serializes all proxy resolution behind a single `RLock`,
including lazy property evaluation. If one agent's GitHub API call takes
5 seconds, every other proxy access (Discord, etc.) is blocked. Fine for
a single agent, but the spec envisions multiple concurrent agents sharing
the same platform. When we get there, options include per-object locks,
read-write locks, or releasing the lock around I/O.

## Hardcoded TTL

Every lazy property has `> 3600` hardcoded inline. The caching.md says
sources shouldn't bake in assumptions about agent cadence, but the TTL
is baked at every call site. For a CRM monitor that runs continuously
vs. a newsletter that runs daily, the optimal TTL is very different.
Consider making TTL configurable per-source or per-registry.

## ProxyRegistry never evicts stale proxy objects

When a TTL-cached property re-evaluates (e.g. `GitHubRepo.issues`), it
creates fresh child objects with deterministic proxy IDs — these overwrite
in the registry. But grandchild objects (e.g. `GitHubComment` from a
previous issue's `.comments`) remain as orphans. For the long-lived daemon
envisioned in the spec, this is a slow memory leak. Fine while processes
are short-lived (newsletter). When we go long-lived, consider weak refs,
LRU eviction, or explicit `unregister()`.

## PR review comments not available

`GitHubPullRequest.comments` uses the issues comments endpoint, which
returns regular discussion comments only. PR review comments (inline
diff comments, review bodies) come from `/pulls/{n}/reviews` and
`/pulls/{n}/comments` — a different API surface. On repos with active
code review, this is where most substantive PR discussion happens.
Not needed today (we don't use PRs), but important if open-sourced or
used on repos with review workflows.
