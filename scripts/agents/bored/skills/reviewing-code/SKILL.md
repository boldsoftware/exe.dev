---
name: reviewing-code
description: Use when the user asks for a code review.
---

# Reviewing Code

`codereview.py` subcommands block, some for a long time. Invoke and wait; do not poll, stream, or read partial output. Unix conventions: stdout is data, stderr is diagnostics, nonzero exit means failure.

## Default scope

When the user asks for a code review without specifying a target (e.g. "please codereview"), use `uv run .claude/skills/reviewing-code/codereview.py context` to determine the set of commits in scope.

## How reviews run

The goal: everything the user needs to act, nothing they don't. The user's time is the scarcest resource — all easy questions, confusions, and noise must be resolved before the final review reaches them.

Every review runs through `codereview.py run` — the primary agent never reviews code directly. The user may steer agent count and focus: "codereview 3", "codereview 4 with focus on security". Without explicit guidance, judge by size, complexity, commit messages, and number of commits. Default to two agents; scale up or adjust focus as the diff warrants.

The primary agent's context window must survive the review intact for useful follow-up conversation. Do not read intermediate review files — only `final-review.md`.

### Multi-commit branches

Before choosing agents, assess the commit structure (from `context.txt`). If commits are intentionally atomic and well-crafted (e.g., bug fix → refactor → feature), scope some agents to individual commits and others to the full diff via `extra_prompt` (e.g., "Review only commit abc123; use the broader branch for context."). If commits are sloppy incremental saves, review as a unit and raise the poor commit hygiene as a numbered review item with options (squash, reorganize, stet, etc.).

### Review directory

Each review round uses its own directory with standardized filenames — the directory path is the only value passed between steps.

| File | Description |
|---|---|
| `context.txt` | Git context. Written by the primary agent via `codereview.py context`. |
| `notes.txt` | Prior codereview notes. Written by the primary agent via `codereview.py read-notes`. May be empty. |
| `config.json` | Agent configuration. Written by the primary agent (see config schema below). |
| `{name}-{backend}.md` | Individual agent reviews. Written by `codereview.py run`. |
| `final-review.raw` | Merger output in faux-XML format. Written by `codereview.py run`. |
| `final-review.md` | Merged review (generated from raw). Written by `codereview.py run`. |
| `final-review.json` | Structured JSON for browser UI (generated from raw). Written by `codereview.py run`. |

If doing multiple review rounds in one session, use a fresh directory for each round.

### Execution

1. Create the review directory and populate inputs:

  ```
  REVIEW_DIR=$(mktemp -d)
  uv run .claude/skills/reviewing-code/codereview.py context > "$REVIEW_DIR/context.txt" && uv run .claude/skills/reviewing-code/codereview.py read-notes > "$REVIEW_DIR/notes.txt"
  ```

  Read `context.txt` to assess commit structure and decide agent configuration.

2. Write `$REVIEW_DIR/config.json` and run:

  ```
  uv run .claude/skills/reviewing-code/codereview.py run "$REVIEW_DIR"
  ```

  **Config schema:**
  - `agents` (required) — list of objects with keys `name` (required), `role` (optional), `extra_prompt` (optional).

  **Agents** already receive git context and a base review prompt. Leave `extra_prompt` empty unless you have a specific reason to steer focus — a general-purpose agent reviews well without extra instructions, and roles already shape behavior. When used, keep it to a few words (e.g., "focus on security").

  **Roles:**
  - `architecture` — the 40,000-foot question: right approach, right layer, structural decisions that will age well. Strategic, not tactical.

  **Merger:** Multiple agents provide independent viewpoints to increase quality. The merger combines them into one review — deduplicating, verifying claims against source, and surfacing genuine disagreements. It also handles notes filtering and final formatting (numbered items with lettered options).

  **Agent scaling** (the default two are 1 and 2; add more as complexity warrants):
  1. A general reviewer
  2. An `architecture` reviewer
  3. Special-focus reviewers tailored to the specific needs of the work (security, confirming changes are no-ops, etc.)

3. Read `$REVIEW_DIR/final-review.md`. The review is fully formatted with numbered items and lettered options. Using the AskUserQuestion tool (if available), ask the user to choose between:

  - **Delegate:** Claude handles every item it can, tells the user what it decided, then asks about items that need external context — product decisions, user intent, or anything Claude can't resolve on its own. Amend and rebase default to yes.
  - **Browser:** Run `uv run .claude/skills/reviewing-code/review_ui.py "$REVIEW_DIR"`. The process blocks until the user submits; their feedback is printed to stdout.
  - **Autopilot:** Full delegation — Claude uses its own judgment for every item, no exceptions. The user is treated as unavailable; do not ask for input via AskUserQuestion or any other mechanism. The most common mistakes at this stage are scope creep and overengineering. Amend and rebase are both yes. After handling all items, summarize.
  - **Terminal:** Present the results verbatim. Do NOT re-number/re-letter choices.

## Standard options reference

When processing the user's feedback, here are some standard option semantics:

- **delegate** — Claude uses its own judgment; no further user input needed for this item
- **stet** — user considers it intentional; persist in codereview notes so it won't be re-raised
- **temporarily ignore** — skip for now; do NOT persist (may be re-raised in future reviews)
- **add comments** — add code comments to improve clarity

## Codereview notes (git notes)

Codereview decisions are persisted via `.claude/skills/reviewing-code/codereview.py` to avoid re-raising items the user has already dealt with.

### After the user responds

`$REVIEW_DIR/notes.txt` contains the prior notes. Compose a note incorporating past note content, and write it:

```
uv run .claude/skills/reviewing-code/codereview.py write-notes -m "<content>"
```

This will replace all previous notes.

**Note content guidance:** The note is by and for Claude — the user will never read it. Be robust to code movement: reference semantic descriptions, not line numbers. Do NOT include general codebase knowledge — this is a per-task review memory only.

**Stet format:** Each stetted item gets a `STET:key` line — a short canonical key naming the *concern* (not the code location), followed by enough context for the merger to match semantically against differently-worded reviewer items. Preserve existing `STET:` entries unless the user explicitly un-stets.
