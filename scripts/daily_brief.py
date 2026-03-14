#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.11"
# dependencies = ["dspy>=2.6", "mlflow>=2.18"]
# ///
"""Daily commit brief for exe.dev.

Fetches origin/main periodically and, around midnight UTC, generates
a daily brief of the day's commits and posts it to Slack #news.

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
import os
import socket
import subprocess
import sys
import time

import dspy

GITHUB_REPO = "boldsoftware/exe"
SLACK_CHANNEL = "news"
FETCH_INTERVAL = 1800  # 30 minutes

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "slack"))


# ---------------------------------------------------------------------------
# dSPy signature
# ---------------------------------------------------------------------------

class DailyBrief(dspy.Signature):
    """Write the exe.dev daily brief.

    Your audience is the exe.dev engineering team. They are busy, deeply
    technical, and work on this codebase daily. The goal is to help them
    keep their mental model of the system up to date.

    This is NOT a changelog. NOT a summary of all changes. Think of it as
    internal release notes: an *assessment* of what changes are important
    and worth everyone knowing about, conveyed with high technical density
    and precision, plus direct links to primary sources on GitHub.

    Prioritize ruthlessly. Many commits aren't worth mentioning. Collapse
    related commits into one bullet. Some important changes might get two
    bullets. Do NOT have a catchall "other fixes" or "bug fixes" bullet —
    if it's not worth its own bullet, omit it entirely. Avoid a wall of
    text. A few tight bullets is ideal. Quiet days can be a single line.

    Do NOT start with a title or header — the date is already clear from
    the posting context. No preamble, no sign-off, no explanation.

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
    brief: str = dspy.OutputField(
        desc="The brief, ready to post to Slack. Slack mrkdwn format."
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
    mlflow.set_experiment("daily-brief")


# ---------------------------------------------------------------------------
# Brief generation
# ---------------------------------------------------------------------------

def generate_brief(date, commits, history, verbose=False):
    """Use a dSPy RLM to produce the brief. Returns the text or empty string."""
    # Prevent Deno from discovering a parent package.json and using manual
    # node_modules resolution, which breaks pyodide loading in the sandbox.
    os.environ["DENO_NO_PACKAGE_JSON"] = "1"

    lm = dspy.LM("anthropic/claude-opus-4-6", max_tokens=16384)
    dspy.configure(lm=lm)

    rlm = dspy.RLM(
        DailyBrief,
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

    brief_path = os.path.join(os.getcwd(), f"brief_{date.strftime('%Y_%m_%d')}.md")
    with open(brief_path, "w") as f:
        f.write(result.brief)
    log(f"wrote {brief_path}")
    return result.brief


# ---------------------------------------------------------------------------
# Slack posting
# ---------------------------------------------------------------------------

def post_to_slack(brief):
    from client import SlackClient, ensure_token

    hostname = socket.gethostname()
    footer = f"\n\n_posted from `{hostname}` · <https://github.com/{GITHUB_REPO}/blob/main/scripts/daily_brief.py|scripts/daily_brief.py>_"

    token = ensure_token()
    slack = SlackClient(token)
    channel_id = slack.find_channel_id(SLACK_CHANNEL)
    slack.post_message(channel_id, brief + footer, mrkdwn=True)
    log(f"posted brief to #{SLACK_CHANNEL}")


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
    brief = generate_brief(date, commits, history, verbose=verbose)
    if not brief:
        log("failed to generate brief")
        return

    print()
    print(brief)
    print()

    if post:
        post_to_slack(brief)


def main_loop(verbose=False):
    """Production loop: fetch periodically, brief around midnight UTC."""
    log("daily brief daemon starting")
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

        # Fetch periodically.
        if time.time() - last_fetch >= FETCH_INTERVAL:
            fetch()
            last_fetch = time.time()

        # Generate brief shortly after midnight UTC.
        if now.hour == 0 and now.minute >= 5:
            yesterday = (now - datetime.timedelta(days=1)).date()
            brief_path = os.path.join(os.getcwd(), f"brief_{yesterday.strftime('%Y_%m_%d')}.md")
            if last_brief_date != yesterday and not os.path.exists(brief_path):
                log(f"generating brief for {yesterday}")
                commits = get_commits_for_date(yesterday)
                if commits:
                    history = get_history(yesterday)
                    brief = generate_brief(yesterday, commits, history, verbose=verbose)
                    if brief:
                        try:
                            post_to_slack(brief)
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
    parser = argparse.ArgumentParser(description="exe.dev daily commit brief")
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
