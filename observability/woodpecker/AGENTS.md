# Woodpecker 🪶

Woodpecker is a daily observability report agent. It pecks at logs.

## How it works

1. A systemd timer fires `woodpecker.sh` daily at 07:00 UTC
2. The script pulls the latest `origin/main` into the worktree, then runs `woodpecker.py`
3. `woodpecker.py` invokes `shelley client chat` with the prompt from `prompt.md`
4. Shelley examines ClickHouse logs and Prometheus metrics, comparing today vs yesterday
5. Shelley emails a daily digest to the VM owner
6. Shelley updates persistent state files for continuity across runs

## Files

- `prompt.md` — The master prompt sent to Shelley each run (uses `{{STATE_DIR}}` placeholder)
- `woodpecker.py` — Python runner, requires `--state-dir` argument
- `woodpecker.sh` — Wrapper that pulls latest main then runs the python script
- `woodpecker.timer` — Systemd timer unit
- `woodpecker.service` — Systemd service unit

## State

State lives **outside the repo** at the path given by `--state-dir` (default: `/home/exedev/woodpecker-state`).
This keeps agent-generated files (learnings, reports, refinements) out of version control.

## Deployment

Woodpecker runs from a dedicated git worktree at `/home/exedev/exe-woodpecker`.
The worktree tracks `origin/main`; the shell wrapper pulls latest before each run.

```bash
# Install systemd units
sudo cp woodpecker.service woodpecker.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now woodpecker.timer
```

## Manual run

```bash
/home/exedev/exe-woodpecker/observability/woodpecker/woodpecker.sh
```

## Logs

```bash
journalctl -u woodpecker -f
```
