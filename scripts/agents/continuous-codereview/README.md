# continuous-codereview

Automated code review for boldsoftware/exe. Randomly samples lines from
recent commits, runs deep focused reviews looking for P0 bugs, and files
issues in boldsoftware/bots.

Runs on exe-agents.exe.xyz as a systemd timer.

## How it works

Every 30 minutes, the systemd timer fires `continuous-codereview.sh`,
which acquires a flock (skipping if the previous run is still active)
and runs a single iteration of `continuous_codereview.py`:

1. Fetches origin/main and rebases
2. Picks a random recently-changed line (weighted by commit size)
3. Runs a focused code review via `codereview.py` (Claude + Codex)
4. Sanity-checks findings with Claude Opus — only P0, high-confidence bugs
5. Files/updates GitHub issues in boldsoftware/bots with label `continuous-codereview`

Each run can take 15+ minutes (review timeout 600s + sanity check 300s).
The wrapper has a 20-minute hard timeout as a backstop against hangs in
git/gh calls. The flock ensures overlapping timer invocations are skipped.
If a run crashes or is killed, the kernel releases the flock automatically,
so the next timer tick self-heals.

## Files

| File | Purpose |
|------|---------|
| `continuous_codereview.py` | Main script (oneshot, does one review iteration) |
| `continuous-codereview.sh` | Wrapper: flock dedup, hard timeout, invokes Python script |
| `continuous-codereview.service` | systemd oneshot unit |
| `continuous-codereview.timer` | systemd timer (every 30 min) |

## Runtime state

Stored in `~/continuous-codereview-state/` (override with `CONTINUOUS_CODEREVIEW_STATE_DIR`):

- `.lock` — flock file for dedup (prevents overlapping runs)

## Setup on exe-agents.exe.xyz

```sh
# 1. Enable git maintenance (speeds up git fetch over time)
cd /home/exedev/exe
git maintenance start

# 2. Make the wrapper executable
chmod +x /home/exedev/exe/scripts/agents/continuous-codereview/continuous-codereview.sh

# 3. Install systemd units
sudo cp /home/exedev/exe/scripts/agents/continuous-codereview/continuous-codereview.service /etc/systemd/system/
sudo cp /home/exedev/exe/scripts/agents/continuous-codereview/continuous-codereview.timer /etc/systemd/system/
sudo systemctl daemon-reload

# 4. Enable and start the timer
sudo systemctl enable --now continuous-codereview.timer

# 5. Verify
systemctl list-timers continuous-codereview.timer
sudo journalctl -u continuous-codereview.service -f
```

## Dependencies

`python3`, `claude`, `codex`, `gh` (authenticated, with write access to boldsoftware/bots),
`git`, `flock`, `timeout`.

Also requires `~/.claude/skills/reviewing-code/codereview.py` to be present.
