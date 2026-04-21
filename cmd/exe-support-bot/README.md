# exe-support-bot

Agentic helper for the Missive support inbox.

## Pieces

1. **Importer** (`import` subcommand) — pulls every conversation that has
   ever touched the support inbox (open + closed, all teams) from the Missive
   API into a local SQLite database with an FTS5 index on message bodies and
   comments.
2. **Agent loop** (`run` subcommand / `/api/run` endpoint) — given a
   conversation id or an ad-hoc prompt, it runs Claude Sonnet via the
   exe.dev VM LLM gateway with these tools:
   - `sqlite_query` — read-only SELECT against the imported messages
     (`SELECT … FROM messages` / `comments` / `conversations`; FTS via
     `messages_fts MATCH '…'`).
   - `clickhouse_query` — read-only query against prod logs via the
     exe.dev `clickhouse` integration. Default URL
     `https://clickhouse.int.exe.xyz/`, override with `EXE_CLICKHOUSE_URL`.
   - `exe_docs` — fetch a page of `https://exe.dev/docs.md` (or any
     `/docs/...md` listed in the index), cached on disk for up to 24h.
   - `publish_result` — final output. Stored in the `results` table and
     shown on the web page (not yet sent to Missive as a comment — that
     plug-in is deliberately left for a follow-up once we trust the
     behaviour).
3. **Web server** (`serve` subcommand) — renders recent results, scrape
   metadata, and a live “run the agent” form that streams steps & token
   cost back over Server-Sent Events.

## Untrusted data

Everything in the Missive DB is untrusted user input. The agent is given
tool *outputs* (which never include raw Missive bodies unless the agent
asks for them via `sqlite_query`) and is instructed that every message is
user-controlled. The agent cannot take any side-effecting action: no write
tools are exposed. `publish_result` only stores a string locally.

## Limits

Every tool clips its output to 1000 lines / 50 KiB and always returns a
string (errors included) so the agent can recover.

## Environment

| var | purpose |
| --- | --- |
| `EXE_MISSIVE_API_KEY` | Direct Missive PAT. If set, we hit Missive's public API directly. |
| `EXE_MISSIVE_BASE` | Override Missive endpoint. Defaults to `https://public.missiveapp.com/v1` when a PAT is set, otherwise `https://missive.int.exe.xyz/v1` (the exe.dev `missive` integration proxy, which injects auth). |
| `EXE_CLICKHOUSE_URL` | Clickhouse endpoint. Defaults to `https://clickhouse.int.exe.xyz/`. |
| `EXE_LLM_GATEWAY` | LLM gateway base URL. Defaults to the VM-local `http://169.254.169.254/gateway/llm`. |

With no env vars set, the bot assumes it's running inside an exe.dev VM with
the `missive` and `clickhouse` integrations attached; both tools work without
any tokens in that configuration.

## Running locally

```
go run ./cmd/exe-support-bot -db /tmp/support.db import
go run ./cmd/exe-support-bot -db /tmp/support.db serve -http :8000
```
