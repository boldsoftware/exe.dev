# exe-ops Implementation Plan

## Context

Greenfield infrastructure monitoring system with two binaries: a lightweight agent that collects system metrics and a server that stores/displays them. Goal is a single pane of visibility into the exe infrastructure (CPU, memory, disk, network, ZFS, exelet/exeprox status, system updates, tags). Data stored in SQLite with 7-day retention. UI is Vue3+PrimeVue embedded in the Go binary.

## Project Structure

```
exe-ops/
  go.mod
  Makefile
  cmd/
    exe-ops-agent/main.go
    exe-ops-server/main.go
  apitype/
    apitype.go              # shared types + hostname parser
  agent/
    agent.go                # scheduler loop, orchestration
    collector/
      collector.go          # Collector interface
      cpu.go, memory.go, disk.go, network.go
      zfs.go, exe.go, host.go
    client/
      client.go             # HTTP client to send reports
  server/
    server.go               # HTTP handler setup, SPA fallback
    handlers.go             # API handler implementations
    auth.go                 # token auth middleware
    db.go                   # SQLite open + migrations
    store.go                # data access layer
    migrations/
      001-initial.sql
  ui/
    embedfs.go              # go:embed dist/*
    package.json
    vite.config.ts
    src/
      main.ts, App.vue, router.ts
      api/client.ts
      views/Dashboard.vue, ServerDetails.vue
      components/ServerCard.vue, MetricBar.vue, TagList.vue
```

## Key Design Decisions

- **Module path**: `exe.dev/exe-ops`
- **No external metric libraries** -- read `/proc/stat`, `/proc/meminfo`, `/proc/net/dev` directly, use `syscall.Statfs` for disk. Keeps binary small.
- **System updates**: parse `apt list --upgradeable` output.
- **Exe component versions**: run `exelet --version` / `exeprox --version`, parse output. Status via `systemctl is-active`.
- **JSON text columns** for components, updates, tags in SQLite -- small data, rarely filtered by value.
- **Auth on agent POST only** -- UI served same-origin, no user-facing auth initially.
- **modernc.org/sqlite** with WAL mode, busy timeout, proper indexes.
- **Single `reports` table** -- 7-day retention with modest server counts is fine for SQLite.

## SQLite Schema

```sql
CREATE TABLE servers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    hostname   TEXT NOT NULL,
    role       TEXT NOT NULL DEFAULT '',
    region     TEXT NOT NULL DEFAULT '',
    env        TEXT NOT NULL DEFAULT '',
    instance   TEXT NOT NULL DEFAULT '',
    tags       TEXT NOT NULL DEFAULT '[]',
    first_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE reports (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id    INTEGER NOT NULL REFERENCES servers(id),
    timestamp    DATETIME NOT NULL,
    cpu_percent  REAL NOT NULL,
    mem_total    INTEGER NOT NULL,
    mem_used     INTEGER NOT NULL,
    mem_free     INTEGER NOT NULL,
    mem_swap     INTEGER NOT NULL,
    disk_total   INTEGER NOT NULL,
    disk_used    INTEGER NOT NULL,
    disk_free    INTEGER NOT NULL,
    net_send     INTEGER NOT NULL,
    net_recv     INTEGER NOT NULL,
    zfs_used     INTEGER,
    zfs_free     INTEGER,
    uptime_secs  INTEGER NOT NULL DEFAULT 0,
    components   TEXT NOT NULL DEFAULT '[]',
    updates      TEXT NOT NULL DEFAULT '[]'
);

-- Indexes: (server_id, timestamp) composite, timestamp for retention purge
```

## API Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/api/v1/report` | Token | Agent submits metrics |
| GET | `/api/v1/servers` | No | List all servers + latest snapshot |
| GET | `/api/v1/servers/{name}` | No | Server detail + recent history |
| GET | `/health` | No | Health check |
| GET | `/*` | No | SPA fallback (embedded Vue UI) |

## Agent CLI Flags

`--server` (required), `--token` (required), `--name` (default: hostname), `--interval` (default: 30s), `--tags` (comma-separated)

## Server CLI Flags

`--addr` (default: `:8080`), `--db` (default: `exe-ops.db`), `--token` (required), `--retention` (default: `168h`)

## Implementation Phases

### Phase 1: Foundation
1. `go mod init`, Makefile with basic targets
2. `apitype/apitype.go` -- shared types, `ParseHostname`, tests
3. `server/db.go` + `server/migrations/001-initial.sql` -- DB setup
4. `server/store.go` + tests (real in-memory SQLite)
5. `server/auth.go` + tests

### Phase 2: Server Core
6. `server/server.go` + `server/handlers.go` -- HTTP handlers
7. `cmd/exe-ops-server/main.go` -- CLI entry point
8. Integration tests: POST report, GET it back

### Phase 3: Agent Core
9. Collectors: `cpu.go`, `memory.go`, `disk.go`, `network.go`, `host.go` + tests
10. `agent/client/client.go` -- HTTP client
11. `agent/agent.go` -- ticker-based scheduler loop
12. `cmd/exe-ops-agent/main.go` -- CLI entry point

### Phase 4: Extended Collectors
13. `zfs.go` -- ZFS metrics (gracefully absent)
14. `exe.go` -- exelet/exeprox version + status via systemctl
15. System updates detection in `host.go`

### Phase 5: Frontend
16. Vue 3 scaffolding: Vite, PrimeVue, PrimeIcons, dark mode
17. `Dashboard.vue` -- server card grid with live metrics (polls every 30s)
18. `ServerDetails.vue` -- full detail view with components, updates, ZFS, tags
19. `ui/embedfs.go` + SPA fallback handler
20. Makefile `build-ui` target, `build-server` depends on it

### Phase 6: Polish
21. Retention purge goroutine (hourly ticker)
22. Graceful shutdown (signal handling, context cancellation)
23. Request logging middleware (slog)

## Verification

- **Unit tests**: `go test ./...` -- table-driven tests for hostname parser, store operations (in-memory SQLite), collectors (testdata for /proc files), auth middleware
- **Integration**: Start server, run agent pointed at it, verify data appears via GET `/api/v1/servers`
- **UI**: `make build && ./bin/exe-ops-server --token test --db :memory:` then browse to `http://localhost:8080`
