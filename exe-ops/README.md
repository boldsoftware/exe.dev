# exe-ops

Infrastructure monitoring system that provides single-pane visibility into exe infrastructure. A lightweight agent collects system metrics from servers and reports them to a central server with a web dashboard.

## Deployment

To deploy exe-ops use the following:

### Server

To deploy the server, make sure you create a tag (e.g. `git tag -a -m 'v0.1.0' v0.1.0`) and then run:

```
./ops/deploy-exe-ops-server.sh exe-ops
```

This will build and deploy the latest release to the exe-ops server.

### Agent

For agents, if it's a new instance run the following:

```
/ops/deploy-exe-ops-agent.sh <server-name> https://exe-ops.crocodile-vector.ts.net "<AGENT-TOKEN>"
```

This will build and deploy the latest agent to the specified server (using the Tailscale hostname).

For existing agents, you can upgrade in the exe-ops application. This will fetch the latest agent binary from the exe-ops server and self-upgrade.

## Architecture

- **exe-ops-agent** — Runs on each server, collects metrics from `/proc` and system commands, reports to the server over HTTP every 30s
- **exe-ops-server** — Aggregates metrics in SQLite, serves a Vue 3 web UI, tracks agent presence via SSE

```
┌──────────────┐     HTTP/SSE      ┌──────────────┐
│  exe-ops-agent│ ───────────────► │ exe-ops-server│
│  (per server) │                  │  (central)    │
└──────────────┘                  └──────┬───────┘
                                         │
                                    ┌────┴────┐
                                    │ SQLite  │
                                    │  (WAL)  │
                                    └─────────┘
```

## Metrics Collected

| Metric | Source |
|--------|--------|
| CPU | `/proc/stat` |
| Memory & Swap | `/proc/meminfo` |
| Disk | `syscall.Statfs` (root fs) |
| Network | `/proc/net/dev` |
| Uptime | `/proc/uptime` |
| System Updates | `apt list --upgradeable` |
| ZFS | `zfs list` (graceful fallback) |
| EXE Components | exelet/exeprox version & systemd status |

## Quick Start

```bash
# Build everything (UI + agent + server)
make build

# Run the server
make run-server  # TOKEN=dev-token ADDR=:8080 DB=exe-ops.db

# Run an agent
make run-agent   # SERVER=http://localhost:8080 TOKEN=dev-token
```

### Manual usage

```bash
# Server
./bin/exe-ops-server --token <shared-token> --addr :8080 --db exe-ops.db --retention 168h

# Agent
./bin/exe-ops-agent --server http://<server>:8080 --token <shared-token> --interval 30s --tags replica
```

## API Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/api/v1/report` | Token | Receive agent metrics |
| GET | `/api/v1/servers` | — | List all servers |
| GET | `/api/v1/servers/{name}` | — | Server detail + history |
| GET | `/api/v1/stream` | Token | SSE stream (agent presence) |
| GET | `/api/v1/agent/binary` | Token | Download agent binary |
| GET | `/api/v1/version` | — | Server version |
| GET | `/health` | — | Health check |

## Development

```bash
make build-ui      # Build Vue frontend with Vite
make build-agent   # Build agent binary
make build-server  # Build server (embeds UI + agent binaries)
make test          # Run all Go tests
make fmt           # Format with goimports + gofmt
make clean         # Remove build artifacts
```

## Dependencies

Minimal by design:

- [urfave/cli/v2](https://github.com/urfave/cli) — CLI framework
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) — Pure Go SQLite (no cgo)

## Key Design Decisions

- **No cgo** — Pure Go SQLite enables easy cross-compilation
- **Embedded UI** — Vue SPA compiled into the server binary for single-artifact deployment
- **Graceful degradation** — Collectors fail independently; one failure doesn't block others
- **Auto-upgrade** — Agents can download and atomically replace their binary from the server
- **7-day retention** — Hourly purge of old reports keeps the database compact
