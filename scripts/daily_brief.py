#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.11"
# dependencies = ["dspy>=2.6", "mlflow>=2.18"]
# ///
"""Daily codebase brief for exe.dev.

Fetches origin/main periodically and, around midnight UTC, generates
a codebase brief of the day's commits and posts it to Slack #news.

Uses a dSPy RLM (Recursive Language Model) to explore commit diffs via
a sandboxed REPL — no truncation, no context limits.

System dependencies: deno (required by dSPy's REPL sandbox).
Environment: ANTHROPIC_API_KEY must be set.

Debug usage (single run, stdout only):
    uv run scripts/daily_brief.py --date 2025-03-01

With RLM traces:
    uv run scripts/daily_brief.py --date 2025-03-01 --verbose

Post to Slack:
    uv run scripts/daily_brief.py --date 2025-03-01 --post

View traces:
    mlflow ui --backend-store-uri sqlite:///mlruns.db

Production loop:
    uv run scripts/daily_brief.py
"""

import argparse
import datetime
import json
import os
import socket
import subprocess
import sys
import time
import urllib.request

import dspy

GITHUB_REPO = "boldsoftware/exe"
SLACK_CHANNEL = "news"
FETCH_INTERVAL = 1800  # 30 minutes

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "slack"))


# ---------------------------------------------------------------------------
# dSPy signature
# ---------------------------------------------------------------------------

class CodebaseBrief(dspy.Signature):
    """Write the exe.dev codebase brief.

    Your audience is the exe.dev engineering team. They are busy, deeply
    technical, and work on this codebase daily. The goal is to help them
    keep their mental model of the system up to date.

    This is NOT a changelog. NOT a summary of all changes. Think of it as
    internal release notes: an *assessment* of what changes are important
    and worth everyone knowing about, conveyed with high technical density
    and precision, plus direct links to primary sources on GitHub.

    Prioritize ruthlessly. Many commits aren't worth mentioning. Collapse
    related commits into one item. Some important changes might get two
    items. Do NOT have a catchall "other fixes" or "bug fixes" item —
    if it's not worth its own item, omit it entirely. A few tight items
    is ideal. Quiet days can be a single item.

    Do NOT start an item with a title or header — the date is already
    clear from the posting context. No preamble, no sign-off, no
    explanation.

    Every sentence should update the reader's mental model of the system.
    Use the history to understand what readers already know — their prior
    context from recent briefs. Frame today's changes as a delta: what's
    new, what shifted, what landed that they were waiting on. Place work
    on its broader trajectory ("X, started last week, now does Y") when
    it matters, but don't recap. Skip implementation mechanics and
    expected follow-through (tests, config plumbing, deploys) — assume
    competent execution and focus on what's *different about the system
    now*. Trust readers to click through to commits for details. This is
    an index and discovery mechanism, not documentation.

    Use Slack mrkdwn: *bold*, `code`, <URL|text> for links. NOT GitHub
    markdown. Link to commits as
    <https://github.com/boldsoftware/exe/commit/FULL_SHA|`short_sha`>."""

    date: str = dspy.InputField(desc="UTC date (YYYY-MM-DD)")
    n_commits: int = dspy.InputField(desc="Number of commits on main today")
    commits: list[dict] = dspy.InputField(
        desc="Today's commits. Each has: short, sha, subject, author, "
             "date, diff (full patch), url. This is the primary material."
    )
    history: list[str] = dspy.InputField(
        desc="Full commit messages from the prior ~30 days (excluding today), "
             "newest first. This is what readers already know — use it to "
             "understand their mental model and frame today's changes as a "
             "delta against it. Rarely, link a prior commit if a reader "
             "would almost certainly want to look it up — but default to not."
    )
    items: list[str] = dspy.OutputField(
        desc="Self-contained brief items, each posted as its own Slack message. "
             "Slack mrkdwn format."
    )


# ---------------------------------------------------------------------------
# Utilities
# ---------------------------------------------------------------------------

def log(msg):
    print(f"[{time.strftime('%H:%M:%S')}] {msg}", flush=True)


def git(*args):
    """Run a git command. Returns stdout on success, None on failure."""
    r = subprocess.run(["git", *args], capture_output=True, text=True)
    return r.stdout.strip() if r.returncode == 0 else None


def fetch():
    log("fetching origin/main")
    r = subprocess.run(
        ["git", "fetch", "origin", "main"],
        capture_output=True, text=True,
    )
    if r.returncode != 0:
        log(f"git fetch failed: {r.stderr.strip()}")
        return False
    return True


# ---------------------------------------------------------------------------
# Commit gathering
# ---------------------------------------------------------------------------

def get_commits_for_date(date):
    """Return a list of commit dicts on origin/main for the given UTC date."""
    since = f"{date}T00:00:00Z"
    until = f"{date + datetime.timedelta(days=1)}T00:00:00Z"

    result = git(
        "log", "origin/main", "--format=%H",
        f"--since={since}", f"--until={until}",
    )
    if not result:
        return []

    commits = []
    for sha in result.splitlines():
        info = git("log", "-1", "--format=%H%n%h%n%s%n%an%n%aI", sha)
        if not info:
            continue
        lines = info.splitlines()
        if len(lines) < 5:
            continue

        diff = git("show", "--format=", "--stat", "--patch", sha)
        commits.append({
            "sha": lines[0],
            "short": lines[1],
            "subject": lines[2],
            "author": lines[3],
            "date": lines[4],
            "diff": diff or "",
            "url": f"https://github.com/{GITHUB_REPO}/commit/{lines[0]}",
        })

    return commits


def get_history(date, days=30):
    """Return full commit messages from the prior `days` days on origin/main."""
    since = f"{date - datetime.timedelta(days=days)}T00:00:00Z"
    until = f"{date}T00:00:00Z"
    result = git(
        "log", "origin/main", "--format=%h %an %aI%n%B",
        f"--since={since}", f"--until={until}",
    )
    if not result:
        return []
    # Split on double-newlines to separate individual commit messages.
    return [m.strip() for m in result.split("\n\n") if m.strip()]


# ---------------------------------------------------------------------------
# Tracing
# ---------------------------------------------------------------------------

def setup_tracing():
    import mlflow
    mlflow.dspy.autolog()
    mlflow.set_tracking_uri("sqlite:///mlruns.db")
    mlflow.set_experiment("codebase-brief")


# ---------------------------------------------------------------------------
# Brief generation
# ---------------------------------------------------------------------------

def generate_brief(date, commits, history, verbose=False):
    """Use a dSPy RLM to produce the brief. Returns (header, items).

    The header is built in Python from the run context (date and commit
    count); items are produced by the agent, one self-contained brief
    entry per element.
    """
    # Prevent Deno from discovering a parent package.json and using manual
    # node_modules resolution, which breaks pyodide loading in the sandbox.
    os.environ["DENO_NO_PACKAGE_JSON"] = "1"

    lm = dspy.LM("anthropic/claude-opus-4-6", max_tokens=16384)
    dspy.configure(lm=lm)

    rlm = dspy.RLM(
        CodebaseBrief,
        max_iterations=15,
        max_llm_calls=50,
    )

    result = rlm(
        date=date.isoformat(),
        n_commits=len(commits),
        commits=commits,
        history=history,
        verbose=verbose,
    )

    items = list(result.items)
    header = "exe.dev codebase brief"

    brief_path = os.path.join(os.getcwd(), f"brief_{date.strftime('%Y_%m_%d')}.md")
    with open(brief_path, "w") as f:
        f.write(header)
        for item in items:
            f.write("\n\n")
            f.write(item)
        f.write("\n")
    log(f"wrote {brief_path}")
    return header, items


# ---------------------------------------------------------------------------
# Slack posting
# ---------------------------------------------------------------------------

def post_to_slack(header, items):
    from client import SlackClient, ensure_token

    hostname = socket.gethostname()
    provenance = f"_posted from `{hostname}` · <https://github.com/{GITHUB_REPO}/blob/main/scripts/daily_brief.py|scripts/daily_brief.py>_"

    token = ensure_token()
    slack = SlackClient(token)
    channel_id = slack.find_channel_id(SLACK_CHANNEL)
    slack.post_message(channel_id, f"{header}\n{provenance}", mrkdwn=True)
    for item in items:
        slack.post_message(channel_id, item, mrkdwn=True)
    log(f"posted header + {len(items)} items to #{SLACK_CHANNEL}")


# ---------------------------------------------------------------------------
# Alerts — email via the exe.dev gateway, matching scripts/agents/bored.
# ---------------------------------------------------------------------------

ALERT_EMAIL = "josharian@gmail.com"
GATEWAY_EMAIL_URL = "http://169.254.169.254/gateway/email/send"


def send_alert(subject, body):
    """Send an alert email via the exe.dev gateway. Best-effort, never raises."""
    try:
        data = json.dumps({"to": ALERT_EMAIL, "subject": subject, "body": body}).encode()
        req = urllib.request.Request(
            GATEWAY_EMAIL_URL, data=data,
            headers={"Content-Type": "application/json"},
        )
        urllib.request.urlopen(req, timeout=10)
        log(f"alert email sent: {subject}")
    except Exception as e:
        log(f"failed to send alert email: {e}")


# ---------------------------------------------------------------------------
# Self-deploy: fast-forward to origin/main between cycles so a merge is
# enough to ship. On pull failure, email an alert once per process lifetime
# and keep running on the old code rather than crash-looping.
# ---------------------------------------------------------------------------

_autodeploy_alert_sent = False


def _git_rev_parse(rev, *, short=False):
    args = ["git", "rev-parse"]
    if short:
        args.append("--short")
    args.append(rev)
    r = subprocess.run(args, capture_output=True, text=True, timeout=10)
    return r.stdout.strip() or None if r.returncode == 0 else None


def _autodeploy_check():
    """Return True iff origin/main advanced and we just fast-forwarded.

    The caller should exit so systemd restarts us with fresh code. On
    pull failure, email a one-shot alert and return False.
    """
    global _autodeploy_alert_sent

    r = subprocess.run(
        ["git", "fetch", "--quiet", "origin", "main"],
        capture_output=True, text=True, timeout=30,
    )
    if r.returncode != 0:
        log(f"git fetch failed: {r.stderr.strip() or r.stdout.strip()}")
        return False

    local = _git_rev_parse("HEAD")
    remote = _git_rev_parse("origin/main")
    if not local or not remote or local == remote:
        return False

    pull = subprocess.run(
        ["git", "pull", "--ff-only", "--quiet", "origin", "main"],
        capture_output=True, text=True, timeout=60,
    )
    if pull.returncode == 0:
        log(f"self-deployed {local[:12]} → {(_git_rev_parse('HEAD') or remote)[:12]}")
        return True

    if not _autodeploy_alert_sent:
        _autodeploy_alert_sent = True
        err = (pull.stderr.strip() or pull.stdout.strip()) or "no output"
        send_alert(
            "daily_brief autodeploy stuck",
            f"scripts/daily_brief.py cannot fast-forward to {remote[:12]}.\n"
            f"Running on stale code at {local[:12]} on {socket.gethostname()}.\n"
            f"Fix the worktree on the host.\n\n{err}",
        )
    return False


# ---------------------------------------------------------------------------
# Modes
# ---------------------------------------------------------------------------

def run_once(date, post=False, verbose=False):
    """Generate a brief for the given date. Print to stdout, optionally post."""
    log(f"generating brief for {date.isoformat()}")

    fetch()

    commits = get_commits_for_date(date)
    if not commits:
        log(f"no commits found for {date.isoformat()}")
        return

    log(f"found {len(commits)} commits")
    history = get_history(date)
    log(f"loaded {len(history)} history entries")
    header, items = generate_brief(date, commits, history, verbose=verbose)
    if not items:
        log("failed to generate brief")
        return

    print()
    print(header)
    for item in items:
        print()
        print(item)
    print()

    if post:
        post_to_slack(header, items)


def main_loop(verbose=False):
    """Production loop: fetch periodically, brief around midnight UTC."""
    log("codebase brief daemon starting")
    log(f"  running from {_git_rev_parse('HEAD', short=True) or 'unknown'}")
    log(f"  channel: #{SLACK_CHANNEL}")
    log(f"  fetch interval: {FETCH_INTERVAL}s")
    log(f"  stop: touch stop")

    last_brief_date = None
    last_fetch = 0

    while True:
        if os.path.exists("stop"):
            os.remove("stop")
            log("stop file found, exiting")
            break

        now = datetime.datetime.now(datetime.timezone.utc)
        in_window = now.hour == 0

        # Self-deploy outside the posting window only: a mid-window restart
        # would lose last_brief_date and risk a duplicate post.
        if not in_window and _autodeploy_check():
            log("new code on main, exiting for restart")
            break

        # Fetch periodically.
        if time.time() - last_fetch >= FETCH_INTERVAL:
            fetch()
            last_fetch = time.time()

        # Generate brief shortly after midnight UTC.
        if in_window and now.minute >= 5:
            yesterday = (now - datetime.timedelta(days=1)).date()
            if last_brief_date != yesterday:
                log(f"generating brief for {yesterday}")
                commits = get_commits_for_date(yesterday)
                if commits:
                    history = get_history(yesterday)
                    try:
                        header, items = generate_brief(yesterday, commits, history, verbose=verbose)
                    except Exception as e:
                        log(f"failed to generate brief: {e}")
                        header, items = "", []
                    if items:
                        try:
                            post_to_slack(header, items)
                            last_brief_date = yesterday
                        except Exception as e:
                            log(f"failed to post: {e}")
                    else:
                        log("failed to generate brief, will retry")
                else:
                    log(f"no commits for {yesterday}, skipping")
                    last_brief_date = yesterday

        time.sleep(60)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="exe.dev codebase brief")
    parser.add_argument(
        "--date",
        type=lambda s: datetime.date.fromisoformat(s),
        help="Generate brief for this UTC date (debug mode, no loop). "
             "Format: YYYY-MM-DD",
    )
    parser.add_argument(
        "--post",
        action="store_true",
        help="Actually post to Slack (default: print to stdout only)",
    )
    parser.add_argument(
        "--verbose",
        action="store_true",
        help="Print inline RLM REPL traces to stdout",
    )
    args = parser.parse_args()

    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("error: ANTHROPIC_API_KEY environment variable is required", file=sys.stderr)
        sys.exit(1)

    root = git("rev-parse", "--show-toplevel")
    if not root:
        print("error: not inside a git repository", file=sys.stderr)
        sys.exit(1)
    os.chdir(root)

    setup_tracing()

    if args.date:
        run_once(args.date, post=args.post, verbose=args.verbose)
    else:
        if not args.post:
            log("warning: running in loop mode without --post; briefs will only print")
        try:
            main_loop(verbose=args.verbose)
        except KeyboardInterrupt:
            log("interrupted")


if __name__ == "__main__":
    main()
