#!/usr/bin/env python3
"""Continuous code review (oneshot).

Randomly samples a line from recent commits and runs a deep, focused
code review looking for significant p0 bugs. Files GitHub issues
for confirmed findings. Designed to be invoked by a systemd timer;
use continuous-codereview.sh as the wrapper (handles flock dedup).
"""

import json
import os
import random
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time

CODEREVIEW_PY = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "codereview.py"
)
GITHUB_REPO = "boldsoftware/bots"
ISSUE_LABEL = "continuous-codereview"
COMMIT_LOOKBACK = "24.hours"
PROVENANCE = (
    "\n\n---\n"
    "*posted by [continuous-codereview]"
    "(https://github.com/boldsoftware/exe/blob/main/scripts/agents/"
    "continuous-codereview/continuous_codereview.py)"
    f" from `{socket.gethostname()}`*"
)
# Files to skip during line selection.
SKIP_SUFFIXES = (
    ".pb.go", ".pb.gw.go",
    ".png", ".jpg", ".gif", ".ico", ".svg", ".pdf",
    ".woff", ".woff2", ".ttf", ".eot",
    ".zip", ".tar", ".gz", ".bin",
    ".sum", ".lock",
)
SKIP_PREFIXES = ("vendor/", "webstatic/", "node_modules/")


# ---------------------------------------------------------------------------
# Utilities
# ---------------------------------------------------------------------------

def log(msg):
    print(f"[{time.strftime('%H:%M:%S')}] {msg}", flush=True)


def git(*args):
    """Run a git command. Returns stdout on success, None on failure."""
    r = subprocess.run(
        ["git", *args], capture_output=True, text=True,
    )
    return r.stdout.strip() if r.returncode == 0 else None


def should_skip_file(path):
    return (
        any(path.endswith(s) for s in SKIP_SUFFIXES)
        or any(path.startswith(p) for p in SKIP_PREFIXES)
    )


# ---------------------------------------------------------------------------
# Step 1: Rebase
# ---------------------------------------------------------------------------

def rebase_onto_main():
    r = subprocess.run(
        ["git", "fetch", "origin", "main"],
        capture_output=True, text=True,
    )
    if r.returncode != 0:
        log(f"git fetch failed: {r.stderr.strip()}")
        return False
    r = subprocess.run(
        ["git", "rebase", "origin/main"],
        capture_output=True, text=True,
    )
    if r.returncode != 0:
        log(f"git rebase failed: {r.stderr.strip()}")
        subprocess.run(
            ["git", "rebase", "--abort"], capture_output=True,
        )
        return False
    return True


# ---------------------------------------------------------------------------
# Step 2: Pick a random line
# ---------------------------------------------------------------------------

def pick_random_line():
    """Pick a random changed line from recent commits on origin/main.

    Weighting: commits that touch more lines are more likely to contribute
    a selection, and lines changed in multiple commits are more likely to
    be chosen.
    """
    result = git("log", "origin/main", "--format=%H",
                 "--since", COMMIT_LOOKBACK)
    if not result:
        log("no commits found on origin/main")
        return None

    commits = result.splitlines()

    # Pool all candidates across all commits so that larger commits
    # contribute proportionally more lines.
    all_candidates = []
    for commit in commits:
        diff = git("show", "--format=", "--unified=0", "--no-ext-diff", commit)
        if not diff:
            continue

        current_file = None
        new_line = None

        for dline in diff.splitlines():
            if dline.startswith("diff --git"):
                current_file = None
                new_line = None
            elif dline.startswith("+++ b/"):
                path = dline[6:]
                current_file = None if should_skip_file(path) else path
            elif dline.startswith("@@ ") and current_file:
                m = re.search(r"\+(\d+)", dline)
                if m:
                    new_line = int(m.group(1))
            elif dline.startswith("+") and not dline.startswith("+++"):
                if current_file and new_line is not None:
                    content = dline[1:].strip()
                    if content:
                        all_candidates.append((commit, current_file, new_line, content))
                    new_line += 1

    if not all_candidates:
        return None

    random.shuffle(all_candidates)
    for commit, chosen_file, orig_line, chosen_content in all_candidates:
        if not os.path.isfile(chosen_file):
            continue

        try:
            with open(chosen_file) as f:
                current_lines = f.readlines()
        except (OSError, UnicodeDecodeError):
            continue

        # Find the matching line closest to the original line number.
        matches = [
            i for i, fline in enumerate(current_lines, 1)
            if fline.strip() == chosen_content
        ]
        if matches:
            best = min(matches, key=lambda i: abs(i - orig_line))
            log(f"selected from commit {commit[:8]}: {chosen_file}:{best}")
            return chosen_file, best, chosen_content

        # Fallback: random non-empty line from the same file.
        non_empty = [
            (i, l.strip())
            for i, l in enumerate(current_lines, 1)
            if l.strip()
        ]
        if non_empty:
            idx, content = random.choice(non_empty)
            log(f"fallback from {chosen_file}:{idx} (content shifted)")
            return chosen_file, idx, content

    return None


# ---------------------------------------------------------------------------
# Step 3: Run focused review
# ---------------------------------------------------------------------------

def run_focused_review(filename, line_number, line_content):
    """Run a codereview focused on the given line.

    Returns (review_file_path, review_dir) on success, or (None, None).
    Caller is responsible for cleaning up review_dir.
    """
    review_dir = tempfile.mkdtemp(prefix="ccr-")

    context = (
        f"CONTINUOUS CODE REVIEW — seed point: {filename}:{line_number}\n"
        f"\n"
        f"This is a randomly selected recently-changed line, meant as a\n"
        f"starting/focal/inspiration point for a narrow but deep codereview."
    )
    with open(os.path.join(review_dir, "context.txt"), "w") as f:
        f.write(context)

    with open(os.path.join(review_dir, "notes.txt"), "w") as f:
        pass  # empty

    extra = (
        f"Using {filename}:{line_number} as a starting/focal/inspiration "
        f"point, do a narrow but deep codereview, looking only for "
        f"significant p0 bugs. Do not report possible bugs or subpar "
        f"design decisions. Only report issues that are unarguably wrong "
        f"and genuinely high priority. No bug report should ever say "
        f"'may' or 'might'. RESEARCH to answer uncertainty. If you "
        f"cannot achieve confidence, don't report it."
    )
    config = {"agents": [{"name": "reviewer", "extra_prompt": extra}]}
    with open(os.path.join(review_dir, "config.json"), "w") as f:
        json.dump(config, f, indent=2)

    log("running review agents...")
    try:
        r = subprocess.run(
            [sys.executable, CODEREVIEW_PY, "run", review_dir],
            capture_output=True, text=True,
            timeout=600,
        )
    except subprocess.TimeoutExpired:
        log("review timed out after 600s")
        shutil.rmtree(review_dir, ignore_errors=True)
        return None, None

    # Find the best available output file.
    for candidate in (
        os.path.join(review_dir, "final-review.raw"),
        os.path.join(review_dir, "reviewer-claude.md"),
        os.path.join(review_dir, "reviewer-codex.md"),
    ):
        if os.path.exists(candidate) and os.path.getsize(candidate) > 0:
            return candidate, review_dir

    if r.returncode != 0:
        log(f"review failed (rc={r.returncode}): {r.stderr[:300]}")
    else:
        log("review produced no output")
    shutil.rmtree(review_dir, ignore_errors=True)
    return None, None


# ---------------------------------------------------------------------------
# Step 4: Sanity check and file issues
# ---------------------------------------------------------------------------

def sanity_check_and_file(review_path, focal_file, focal_line):
    prompt = (
        f"You are the sanity check for a continuous codereview system. A review "
        f"was run seeded from {focal_file}:{focal_line}. Read the review output "
        f"at {review_path} and decide what (if anything) is worth filing as a "
        f"GitHub issue on {GITHUB_REPO}.\n"
        f"\n"
        f"Pick the single highest-priority finding (if any) that is unarguably "
        f"wrong and genuinely important. Check {GITHUB_REPO} issues with label "
        f"'{ISSUE_LABEL}' for duplicates. If there's a duplicate that's missing "
        f"detail or insight from this review, add a comment and note the new "
        f"insight. Don't reopen closed issues. Don't file anything speculative. "
        f"When in doubt, don't file. False positives erode trust in this system.\n"
        f"\n"
        f"Output a JSON array with zero or one element:\n"
        f'  {{"action": "create", "title": "...", "body": "..."}}\n'
        f'  {{"action": "comment", "issue_number": N, "comment": "..."}}\n'
        f"\n"
        f"New issue bodies should end with a blank line then: Count: 1\n"
        f"If nothing warrants filing, output: []\n"
        f"Output ONLY the JSON array, no other text."
    )

    log("running sanity check...")
    try:
        r = subprocess.run(
            ["claude", "--dangerously-skip-permissions",
             "--model", "opus", "-p", prompt],
            capture_output=True, text=True,
            timeout=300,
        )
    except subprocess.TimeoutExpired:
        log("sanity check timed out")
        return

    if r.returncode != 0:
        log(f"sanity check failed: {r.stderr[:200]}")
        return

    output = r.stdout.strip()
    try:
        start = output.index("[")
        end = output.rindex("]") + 1
        actions = json.loads(output[start:end])
    except (ValueError, json.JSONDecodeError) as e:
        log(f"could not parse sanity check JSON: {e}")
        if output:
            log(f"output preview: {output[:300]}")
        return

    if not actions:
        log("sanity check: no issues to file")
        return

    for action in actions:
        act = action.get("action")
        if act == "create":
            create_issue(action.get("title", ""), action.get("body", ""))
        elif act == "comment":
            comment_on_issue(
                action.get("issue_number"),
                action.get("comment", ""),
            )


def create_issue(title, body):
    if not title or not body:
        log("skipping issue with empty title or body")
        return
    body += PROVENANCE
    log(f"creating issue: {title}")
    r = subprocess.run(
        ["gh", "issue", "create",
         "-R", GITHUB_REPO,
         "--title", title,
         "--body", body,
         "--label", ISSUE_LABEL],
        capture_output=True, text=True,
    )
    if r.returncode == 0:
        log(f"created: {r.stdout.strip()}")
    else:
        log(f"failed to create issue: {r.stderr.strip()}")


def comment_on_issue(issue_number, comment):
    if not issue_number or not comment:
        return
    comment += PROVENANCE
    log(f"commenting on #{issue_number}")

    r = subprocess.run(
        ["gh", "issue", "comment",
         "-R", GITHUB_REPO,
         str(issue_number),
         "--body", comment],
        capture_output=True, text=True,
    )
    if r.returncode != 0:
        log(f"failed to comment: {r.stderr.strip()}")
        return

    # Increment "Count: N" in the issue body.
    r = subprocess.run(
        ["gh", "issue", "view",
         "-R", GITHUB_REPO,
         str(issue_number),
         "--json", "body"],
        capture_output=True, text=True,
    )
    if r.returncode != 0:
        return

    try:
        body = json.loads(r.stdout)["body"]
    except (json.JSONDecodeError, KeyError):
        return

    m = re.search(r"Count:\s*(\d+)", body)
    if m:
        old = int(m.group(1))
        new_body = body[:m.start()] + f"Count: {old + 1}" + body[m.end():]
        subprocess.run(
            ["gh", "issue", "edit",
             "-R", GITHUB_REPO,
             str(issue_number),
             "--body", new_body],
            capture_output=True, text=True,
        )
        log(f"count: {old} -> {old + 1}")


# ---------------------------------------------------------------------------
# Setup / main
# ---------------------------------------------------------------------------

def ensure_label():
    """Create the issue label if it doesn't already exist."""
    subprocess.run(
        ["gh", "label", "create", ISSUE_LABEL,
         "-R", GITHUB_REPO,
         "--description", "Issues found by continuous code review",
         "--color", "d93f0b"],
        capture_output=True, text=True,
    )


def check_prerequisites():
    ok = True
    for cmd in ("claude", "codex", "gh", "git"):
        if shutil.which(cmd) is None:
            print(f"error: {cmd} not found on PATH", file=sys.stderr)
            ok = False
    if not os.path.isfile(CODEREVIEW_PY):
        print(f"error: {CODEREVIEW_PY} not found", file=sys.stderr)
        ok = False
    if not ok:
        sys.exit(1)

    r = subprocess.run(
        ["gh", "auth", "status"], capture_output=True, text=True,
    )
    if r.returncode != 0:
        print("error: gh not authenticated — run: gh auth login",
              file=sys.stderr)
        sys.exit(1)

    # Ensure CWD is the repo root so relative paths from diffs work.
    root = git("rev-parse", "--show-toplevel")
    if not root:
        print("error: not inside a git repository", file=sys.stderr)
        sys.exit(1)
    os.chdir(root)


def main():
    check_prerequisites()
    ensure_label()

    log(f"continuous code review: starting")
    log(f"  repo:  {GITHUB_REPO}")
    log(f"  label: {ISSUE_LABEL}")

    if not rebase_onto_main():
        sys.exit(1)

    result = pick_random_line()
    if not result:
        log("could not pick a line, done")
        return

    filename, line_number, content = result
    log(f"focal: {filename}:{line_number}  {content[:80]}")

    review_path, review_dir = run_focused_review(
        filename, line_number, content,
    )
    if not review_path:
        return

    try:
        log(f"review: {review_path}")
        sanity_check_and_file(review_path, filename, line_number)
    finally:
        shutil.rmtree(review_dir, ignore_errors=True)


if __name__ == "__main__":
    main()
