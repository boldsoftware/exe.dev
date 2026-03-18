# Bored

An always-on agent that keeps a queue of super high quality, high priority, high relevance commits always ready and waiting for a human to review — instead of swordfighting in the halls.

Runs on `bored.exe.xyz`. The exe repo is cloned at `~/exe` on the VM.

## The Website

A single-page website that shows one commit at a time for the current user. Must work well on mobile.

When a user visits, the system automatically reserves the next available item for them. No other user sees that item while it's reserved (15-minute timeout, refreshed as long as the user's page is open). This prevents multiple people from reviewing the same commit simultaneously.

The commit is a git commit message (subject, body) and a diff (behind a toggle). The diff uses the same diff display engine as Shelley (`@pierre/diffs`). Labels from the underlying issue are shown so the reviewer has context about the source (e.g. `ci-flaky-test`, `continuous-codereview`).

Each commit is pushed to GitHub under a hidden ref (`refs/bored/<id>`) so the user can view it on GitHub. The UI shows a link to view the commit on GitHub, and copy-pasteable `git fetch` / `git cherry-pick FETCH_HEAD` instructions.

The user is identified via the exe VM gateway auth headers (`X-ExeDev-UserID`, `X-ExeDev-Email`), which are injected by the proxy and cannot be spoofed.

### Approve Button

Works basically the same as the `bin/q` script: pushing to a special named branch (`queue-main-bored-<commit_id>-<slug>`). The HTTP handler returns immediately; approval runs in a background thread with status updates via polling.

**But first, a qualification step.** Before showing a commit to users, we need to verify it passes CI. The flow:

1. Amend "CI ONLY" into the commit message subject.
2. Push to a queue branch and wait for CI to complete (same as `bin/q`).
3. The "CI ONLY" marker causes CI to run tests but skip pushing to main, pushing to ralph, and Slack notifications (see `queue-main.yml` lines 233–249).
4. If CI passes, remove "CI ONLY" from the commit message. The commit is now qualified and ready to show to the user.
5. If CI fails, the commit is not shown. Discard it, post the failure to the issue, and try again on the next cycle.

This way, when a user clicks approve, CI failures should be rare (only flakes). On approval:

- Push to a `queue-main-bored-<slug>` branch and poll for CI completion (same mechanism as `bin/q`).
- **On success:** remove the commit from the queue.
- **On failure:** show an error message with a link to the CI run. Re-amend the `Approved-By` and `Approved-At` trailers with a fresh timestamp so the new SHA registers as a distinct push. The commit stays displayed with an approve/retry button so the user can easily retry. (This handles CI flakes.) If the user judges it a legit failure, they can comment and reject instead.

On every approve attempt, the commit message is amended with two git trailers: `Approved-By: <email>` and `Approved-At: <ISO 8601 timestamp>`. These provide useful provenance in the commit history and double as cache-busting (the changing timestamp produces a new SHA on each retry).

### Reject Button

Includes a free-form text field where the user explains why they've rejected it. That rejection text, along with the commit SHA and ref path (for provenance), gets posted back to the associated issue as a comment, clearly stating which user (from `X-ExeDev-Email`) made the rejection. The hidden ref is then deleted to prevent future agents from fetching attacker-crafted commit content (prompt injection defense); the SHA and ref path recorded in the comment provide sufficient context.

The commit is removed from the queue. We don't want an agent to contort itself trying to rescue a commit that has the fundamentally wrong shape — easier to discard, save the feedback, and start over. The issue remains open in `boldsoftware/bots` for future gardening/fix cycles to pick up again, now with the human's feedback for future agents to take into consideration.

### Leave for Someone Else

Releases the user's reservation on the commit without rendering any judgment. The item goes back to the pool for other users. A 48-hour cooldown prevents the same user from seeing it again (so they aren't repeatedly shown items they've already decided to skip). After skipping, the user is immediately shown the next available item.

## The Queue

A single queue of items drawn from all open issues in `github.com/boldsoftware/bots/issues` regardless of label. The queue holds up to 10 fully qualified (CI-passed) items ready for human review.

### Reservations

When a user visits the page, the next available item is automatically reserved for them. While reserved:

- No other user sees that item.
- The reservation lasts 15 minutes, refreshed on each page poll (every 5 seconds).
- If the user closes the tab, the reservation expires after 15 minutes and the item returns to the pool.
- Approve (on success), reject, or skip all release the reservation. On approve failure, the reservation is kept so the original reviewer can retry.

### Storage

State is stored in a SQLite database at `~/bored-state/bored.db` with WAL mode for concurrent reads. Three tables:

- **items** — the queue of qualified commits
- **reservations** — active user-to-item locks
- **cooldowns** — per-user per-item 48h "don't show again" entries

## Background Process: Generating Commits

The goal of the background process is to keep the queue full (up to 10 items). When there's room, the following happens:

All Claude Code invocations in this process must use `--dangerously-skip-permissions` to run without user oversight.

### Step 1: Gardening Pass

```
git fetch && git reset --hard origin/main
```

Invoke Claude Code and ask it to do a gardening pass. The prompt conveys roughly this:

```
Please take a gardening pass through https://github.com/boldsoftware/bots/issues.

- Look for items that have already been fixed (or otherwise obviated) and comment and close them.
- Look for dups and pick a winner and cross-reference and close the other.
- Look for codereview follow-up issues that are about speculative code that has
  not landed and comment and close them.
- Look for user feedback (made clear in the preamble of a rejection comment)
  indicating that an issue is fundamentally misguided or should be closed, and
  decide whether to close it with a comment, or whether there's enough direction
  that the issue should be re-attempted later with a different approach, in which
  case, add a comment to that effect (for use by future gardening agents).
- Look for high impact/importance issues that are amenable
  to reasonably straightforward single-commit resolution.

Use subagents liberally...this is extremely parallelizable.

Return ONLY the issue number of the single highest priority outstanding issue,
or 0 if no such issues exist.
```

Then check whether the issue(s) corresponding to any queued items were closed by this gardening pass. If so, remove them — we now have more room to fill.

This keeps the issues repo clean and, importantly, identifies the issue to work on next.

### Step 2: Fix the Issue

Create a new worktree from `~/exe`. Start Claude Code in that worktree and ask it to fix issue `github.com/boldsoftware/bots/issues/{N}` and commit the result.

### Step 3: Autorefinement

Port the `reviewing-code` and `autorefine` skills into the worktree (copy `~/.claude/skills/{reviewing-code,autorefine}` into the worktree's repo root so Claude can find them, along with all necessary supporting scripts). Then run:

```
claude -p autorefine
```

This typically works for an hour or two, incrementally reviewing and refining code.

### Step 4: Final Commit Message Update

Run Claude Code one more time and ask it to update the commit message, incorporating the commentary about the refinement process from the autorefinement Claude process. The purpose is to ensure the commit message contains the implicit information that surfaced during the review/refine process: what were the interesting decisions that got made along the way? What might be controversial? This context helps human reviewers.

### Step 5: CI Qualification

Run the "CI ONLY" qualification step (described above under Approve Button). If it passes, the commit is added to the queue. If it fails, discard, post the failure to the issue, and try again.

---

## Operational Details

### Primary Resources

| Resource | Path | What It Is |
|----------|------|------------|
| Existing agents | `scripts/agents/watch-ci-flake/`, `scripts/agents/continuous-codereview/` | Reference implementations for agent structure, systemd units, state management |
| `bin/q` | `bin/q` | CI queue push-and-wait script; the bored approval flow reimplements its core logic |
| CI workflow | `.github/workflows/queue-main.yml` | Defines queue branch triggers, "CI ONLY" marker detection (lines 233–249), push-to-main, gate files |
| Queue gate files | `.github/queue-gate-ancestor`, `.github/queue-gate-bad-subjects` | Live-updated commit validation gates |
| Shelley diff engine | `shelley/ui/src/components/PatchTool.tsx` | `@pierre/diffs` integration: split/unified views, syntax highlighting, mobile-responsive |
| Diff worker | `shelley/ui/src/diffs-worker.ts` | Web Worker for background syntax tokenization |
| Proxy auth headers | `exeweb/proxy.go` lines 927–953 | Injects `X-ExeDev-UserID`, `X-ExeDev-Email`; strips spoofed headers and auth tokens |
| VM inventory | `devdocs/vms.md` | Add `bored` entry here |
| Reviewing-code skill | `~/.claude/skills/reviewing-code/` | `SKILL.md`, `codereview.py` (578 lines), `review_ui.py` (205 lines) |
| Autorefine skill | `~/.claude/skills/autorefine/SKILL.md` | Iterative review loop, stores snapshots as git refs |

### Glossary

| Term | Meaning |
|------|---------|
| **queue branch** | `queue-main-{user}-{slug}` — pushing here triggers CI via `queue-main.yml`; on success, CI auto-pushes to `origin/main` and deletes the branch |
| **CI ONLY** | Case-insensitive marker in commit subject; causes CI to run tests but skip push-to-main, ralph, and Slack notifications |
| **hidden ref** | `refs/bored/<id>` — a git ref not visible as a branch; viewable on GitHub at `github.com/{repo}/commit/{sha}` after pushing |
| **ralph** | AI auto-fix agent (`queue-ralph.yml`) triggered on CI failure; creates a fix commit and pushes it back to the queue branch |
| **gardening pass** | Triage sweep of the issues repo: close fixed/dup/moot issues, identify the single highest-priority issue to work on |
| **reservation** | Temporary lock (15 min, refreshed by polling) that assigns a queue item to a specific user; prevents duplicate review effort |
| **cooldown** | 48-hour per-user per-item suppression created by "leave for someone else"; prevents the same item from being re-shown |
| **`boldsoftware/bots`** | Issues-only repo where `watch-ci-flake` and `continuous-codereview` agents file issues |
| **`boldsoftware/exe`** | The main codebase; commits target this repo; cloned at `~/exe` on the VM |
| **`@pierre/diffs`** | Diff rendering library used by Shelley; supports split/unified views, 30+ language syntax highlighting, dark/light themes |
| **gateway auth** | exe.dev proxy injects `X-ExeDev-UserID` and `X-ExeDev-Email` headers; cannot be spoofed (stripped on ingress, set by proxy) |
| **`--dangerously-skip-permissions`** | Required flag for all Claude Code invocations in this agent; allows autonomous operation without user approval prompts |
| **`cco`** | Alias for `claude --model=opus --effort high --dangerously-skip-permissions` |
| **worktree** | `git worktree add` — creates a separate checkout of the repo for isolated work; one worktree per in-flight commit |
| **queue gate** | `.github/queue-gate-ancestor` requires all queue commits to descend from a specific SHA; `.github/queue-gate-bad-subjects` blocks specific commit subjects |
| **autorefine** | Iterative loop (up to 25 rounds) that runs code review + auto-fix cycles, storing snapshots as `refs/autorefine/<branch>/<run-id>/<iteration>` |
| **systemd oneshot** | How existing agents run: `Type=oneshot` service triggered by a `.timer` unit; bored will likely use a long-running service instead |

### Agent Conventions (from existing agents)

- State directory: `~/bored-state/`
- Systemd unit files: `bored.service` (long-running, not timer-based — it maintains a web server and background loop)
- Dependencies: `claude`, `gh`, `git`, `python3`/`uv`, `node`/`npm` (for diff rendering)
- Issues repo: `boldsoftware/bots`
- Code repo: `boldsoftware/exe` (at `~/exe`)
- Whence note: include provenance footer in issue comments (agent name + hostname, per existing agent convention)
