# Running exed locally

How to build and run the full exed+exelet+sshpiper stack locally for development.

## On an exe.dev VM (i.e. /exe.dev exists)

Build exelet first to avoid OOM from concurrent Go compilations on small VMs:

```bash
cd /home/exedev/exe  # or your worktree
make exelet
go build -o /tmp/exed-local ./cmd/exed/
/tmp/exed-local -stage=local -start-exelet -db tmp
```

This single command starts everything:
- HTTP on `:8080`
- SSH server on `:2223` (internal, used by sshpiper)
- Piper gRPC plugin on `:2224`
- sshpiper on `:2222` (auto-started, reads host key from the DB)
- exelet (auto-started via `-start-exelet`)

The `-db tmp` flag creates a fresh temporary database. To persist state across restarts,
use a path like `-db /tmp/my-exed.db`.

sshpiper is auto-started in local mode. It reads the SSH host key directly from the
active database (not hardcoded to `exe.db`), so it works with any `-db` path.

### Connect via SSH

```bash
ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 localhost
```

### Access the web UI

- Locally: `http://localhost:8080`
- Externally (via exe.dev proxy): `https://<your-vm>.exe.xyz:8080/`

## On macOS (with Lima VMs)

Same commands, but exelet runs on `lima-exe-ctr` instead of locally:

```bash
make exelet
go build -o /tmp/exed-local ./cmd/exed/
/tmp/exed-local -stage=local -start-exelet -db tmp -open
```

The `-open` flag opens the web UI in your browser.

## Useful flags

| Flag | Description |
|------|-------------|
| `-stage=local` | Local development mode (required) |
| `-start-exelet` | Auto-start exelet |
| `-db tmp` | Use a fresh temp database |
| `-db /path/to/file.db` | Use a persistent database |
| `-open` | Open browser to web UI (macOS only) |
| `-http :9090` | Change HTTP listen address |
| `-piperd-port 2222` | Change sshpiper listen port |
| `-multi-exelet` | Also start exelet on lima-exe-ctr-tests (macOS only) |

## Environment variables for GitHub integration

To test GitHub integration, source credentials before starting:

```bash
source ~/.envrc-github  # sets EXE_GITHUB_APP_* vars
/tmp/exed-local -stage=local -start-exelet -db /tmp/ghtest-db
```

## Troubleshooting

- **OOM during build**: Always `make exelet` before `go build ./cmd/exed/`.
- **sshpiper won't start**: Check that `deps/sshpiper/sshpiperd` binary exists. If not, it will be built automatically by `sshpiper.sh`. On an exe.dev VM, ensure `timeout` is available (it should be).
- **Port conflicts**: If 2222/2223/2224/8080 are in use, kill existing processes or use different ports via flags.
