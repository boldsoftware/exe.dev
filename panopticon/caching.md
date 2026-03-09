# Caching in the Proxy System

Two layers: sandbox-side and host-side.

## Sandbox-Side (ProxyRef._cache)

Each `ProxyRef` caches resolved attributes in a `_cache` dict. Ephemeral —
dies when `PythonInterpreter` shuts down at the end of each `forward()`.
Fine, because UDS round-trips are sub-millisecond. Exists only to avoid
redundant round-trips within a single run.

## Host-Side (ProxyObject subclasses)

This is where caching matters. External API calls are expensive.
Host-side objects are plain Python — no run affinity. `ProxyRegistry`,
`MuxServer`, and all `ProxyObject` instances persist across `forward()` calls.

Cache in the `ProxyObject` subclass. `resolve_getattr` uses `getattr()` on
the live object, so standard patterns work:

```python
class GitHubIssue(ProxyObject):
    def __init__(self, issue_id, client):
        super().__init__(proxy_id=f"issue_{issue_id}", ...)
        self._client = client
        self._data = None

    def _fetch(self):
        if self._data is None:
            self._data = self._client.get_issue(self.issue_id)
        return self._data

    @property
    def body(self):
        return self._fetch()["body"]
```

### Invalidation

Per spec: cache external read-only sources aggressively (1 hour).
All source TTLs default to 3600s — sources shouldn't bake in assumptions
about agent cadence.
Strategies live in the ProxyObject subclass: TTL-based (`_fetched_at`),
explicit (`refresh()`), or write-through (clear cache after mutations).

### Concurrency

Proxy resolution is serialized via `threading.RLock` in `ProxyRegistry`.
Both `resolve_getattr` and `resolve_call` hold the lock for the entire
operation, including lazy property evaluation and `_classify_value`
(which may re-enter via `register()`). This means lazy properties are
safe under any server threading model without per-property locking.

## Process Lifetime

The host process should be long-lived. A single Python process holds
the registry, mux server, and cached proxy objects in memory:

```python
registry = ProxyRegistry()
registry.register(github)
registry.register(discord)

with MuxServer(registry) as mux:
    while should_run():
        rlm(github=github, discord=discord)
```

No need for a detached daemon — the long-lived process *is* the daemon.
The MuxServer is already an HTTP server on a Unix socket, so external
triggers (cron, webhooks) can poke it by adding endpoints:
`curl --unix-socket /path/to/mux.sock http://localhost/run`.
