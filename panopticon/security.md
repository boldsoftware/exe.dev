Security model for the Panopticon proxy/sandbox system.

## Threat model

Untrusted data flows into the sandbox via proxy objects. The agent code
runs LLM-generated Python influenced by that data. The sandbox can only
interact with the outside world through the UDS mux socket. The worst
case is trashing the Panopticon issue set, which is recoverable via
git rollback (every mutation is a commit).

## Layers (outside in)

1. **Deno/Pyodide/WASM sandbox.** No subprocess, no filesystem, no
   network unless explicitly granted. The only holes: stdin/stdout
   (JSON-RPC to host) and the UDS socket (proxy resolution).

2. **MuxServer (HTTP-over-UDS).** Rate-limited (TokenBucket), enforces
   a request body size cap. Endpoints: `POST /proxy/getattr` and
   `POST /proxy/call`. Methods follow the same safety-by-construction
   model as attributes — the allowlist (`_proxy_methods`) controls which
   methods are callable, and all arguments must be JSON primitives.

3. **ProxyRegistry allowlist.** `_proxy_dir` is an allowlist—attributes
   not listed are inaccessible. `__doc__`, `__dir__`, `__iter__` are
   special-cased before the allowlist check. `_classify_value` rejects
   anything not JSON-serializable (methods, classes, etc.).

4. **JSON-RPC protocol hardening.** The host kills the Deno subprocess
   on any protocol violation (unexpected method, ID mismatch, unrecognized
   message format). Crash-and-restart over silent tolerance.

5. **Copy-on-write storage.** Issues live in git. Every mutation is a
   commit. Rollback is `git checkout <sha>`.

## Write path API design (not yet implemented)

The write surface must be limited to Panopticon issues and agent notes.
Mux handlers for writes must be strongly typed ("update issue X field Y")
not generic ("POST arbitrary JSON"). No handler should accept free-text
URLs, search queries on external sources, or anything that could
exfiltrate data. The transport is JSON, never pickle.
