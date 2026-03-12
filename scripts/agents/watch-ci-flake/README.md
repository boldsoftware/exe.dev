# watch-ci-flake

Automated CI failure triage for boldsoftware/exe. Polls GitHub Actions
for failed runs, classifies each failure, and for flaky failures spawns
parallel analysis agents and files issues in boldsoftware/bots.

Runs on exe-agents.exe.xyz as a systemd timer.

## How it works

Every 5 minutes, `watch-ci-flake.sh` polls for new failed CI runs.
For each new failure, it launches a Claude agent with the
`watch-ci-flake.md` prompt, which:

1. Investigates the failure (logs, changed files, job metadata)
2. Triages into one of: **regression**, **wai**, **flaky-test**, **flaky-infra**
3. For regressions and wai: logs the classification and stops
4. For flaky failures: spawns 4 parallel analysis agents
   (diagnostic + architectural, each via Claude and Codex),
   merges the results, and files/updates a GitHub issue

Issues go to boldsoftware/bots with labels `ci-flaky-test` or
`ci-flaky-infra`. Duplicate detection avoids noise; repeat occurrences
bump a count.

## Files

| File | Purpose |
|------|---------|
| `watch-ci-flake.sh` | Main script (oneshot, run by systemd timer) |
| `watch-ci-flake.md` | Orchestrator prompt for the triage agent |
| `diagnostic.md` | Sub-agent prompt: root cause diagnosis |
| `architectural.md` | Sub-agent prompt: systemic/design analysis |
| `merge.md` | Sub-agent prompt: synthesize analyses into report |
| `yolo_claude.sh` | Wrapper: run Claude with `--dangerously-skip-permissions` |
| `yolo_codex.sh` | Wrapper: run Codex with `--dangerously-bypass-approvals-and-sandbox` |
| `watch-ci-flake.service` | systemd oneshot unit |
| `watch-ci-flake.timer` | systemd timer (every 5 min) |

## Runtime state

Stored in `~/watch-ci-flake-state/` (override with `WATCH_CI_FLAKE_STATE_DIR`):

- `.state` — IDs of already-processed runs (one per line)
- `ci-notes.md` — operational knowledge, evolved by the agent at runtime.
  Starts empty; the agent populates it as it learns patterns.

## Dependencies

`gh` (authenticated, with write access to boldsoftware/bots),
`claude`, `codex`, `jq`.
