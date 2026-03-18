#!/usr/bin/env python3
"""bored — always-on agent that maintains a queue of high-quality commits.

Runs an HTTP server (port 8080) serving a React UI + JSON API, plus a background
worker thread that continuously generates, refines, and CI-qualifies commits.
"""

import datetime
import fcntl
import hashlib
import http.server
import json
import logging
import mimetypes
import os
import re
import shutil
import socketserver
import sqlite3
import subprocess
import threading
import time
import traceback
import urllib.parse
import urllib.request

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
log = logging.getLogger("bored")

# ---------------------------------------------------------------------------
# Paths and constants
# ---------------------------------------------------------------------------

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
UI_DIST = os.path.join(SCRIPT_DIR, "ui", "dist")
STATE_DIR = os.path.expanduser("~/bored-state")
DB_FILE = os.path.join(STATE_DIR, "bored.db")
EXE_REPO = os.path.expanduser("~/exe")
ISSUES_REPO = "boldsoftware/bots"
CODE_REPO = "boldsoftware/exe"
PORT = 8000
WHENCE = f"— bored on {os.uname().nodename}"
CLAUDE_CMD = ["claude", "--dangerously-skip-permissions", "--model", "opus", "--effort", "high"]
MAX_ITEMS = 10
RESERVATION_TIMEOUT = 900  # 15 minutes
COOLDOWN_DURATION = 172800  # 48 hours
CI_POLL_INTERVAL = 15  # seconds between CI status checks
CI_FIND_TIMEOUT = 180  # seconds to wait for CI run to appear
CI_TIMEOUT = 3600  # max seconds to wait for CI run completion
WORKER_SLEEP = 30  # seconds between worker iterations
GARDENING_BACKOFF = 600  # seconds to wait after gardening finds nothing
CLAUDE_TIMEOUT = 86400  # 24h timeout for claude invocations
NO_ISSUES = object()  # sentinel: gardening found nothing to work on
ISSUE_ALREADY_QUEUED = object()  # sentinel: gardening picked an already-queued issue
APPROVER_AUTHORS = {
    "josharian@gmail.com": ("Josh Bleecher Snyder", "josh"),
    "david@zentus.com": ("David Crawshaw", "david"),
    "philip@bold.dev": ("Philip Zeyliger", "philip"),
    "ian@bold.dev": ("Ian Lance Taylor", "ian"),
    "shaun@bold.dev": ("Shaun Loo", "shaun"),
    "bryan.mikaelian@gmail.com": ("Bryan Mikaelian", "bryan"),
    "evan@h5t.io": ("Evan Hazlett", "evan"),
}
ALERT_EMAIL = "josharian@gmail.com"
GATEWAY_EMAIL_URL = "http://169.254.169.254/gateway/email/send"
CONSECUTIVE_FAILURE_THRESHOLD = 3  # email after this many consecutive failures

# ---------------------------------------------------------------------------
# Database
# ---------------------------------------------------------------------------

_local = threading.local()


def get_db():
    """Get a thread-local database connection."""
    if not hasattr(_local, "db"):
        _local.db = sqlite3.connect(DB_FILE)
        _local.db.row_factory = sqlite3.Row
        _local.db.execute("PRAGMA journal_mode=WAL")
        _local.db.execute("PRAGMA foreign_keys=ON")  # Required: reservation integrity depends on FK enforcement
        _local.db.execute("PRAGMA busy_timeout=5000")
    return _local.db


def init_db():
    """Create database tables if they don't exist."""
    os.makedirs(STATE_DIR, exist_ok=True)
    db = sqlite3.connect(DB_FILE)
    db.executescript("""
        CREATE TABLE IF NOT EXISTS items (
            id TEXT PRIMARY KEY,
            issue_number INTEGER NOT NULL,
            sha TEXT NOT NULL,
            subject TEXT NOT NULL,
            body TEXT NOT NULL DEFAULT '',
            diff TEXT NOT NULL DEFAULT '',
            github_url TEXT NOT NULL,
            status TEXT NOT NULL DEFAULT 'ready',
            ci_run_url TEXT,
            error TEXT,
            labels TEXT NOT NULL DEFAULT '[]',
            created_at REAL NOT NULL
        );

        CREATE TABLE IF NOT EXISTS reservations (
            item_id TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
            user_id TEXT NOT NULL,
            user_email TEXT NOT NULL DEFAULT '',
            reserved_at REAL NOT NULL
        );

        CREATE TABLE IF NOT EXISTS cooldowns (
            user_id TEXT NOT NULL,
            item_id TEXT NOT NULL,
            until REAL NOT NULL,
            PRIMARY KEY (user_id, item_id)
        );
    """)
    db.commit()
    db.close()


# ---------------------------------------------------------------------------
# DB helpers
# ---------------------------------------------------------------------------


def _row_to_item(row):
    d = dict(row)
    d["labels"] = json.loads(d["labels"])
    return d


def db_add_item(item):
    """Insert a new item into the queue."""
    db = get_db()
    db.execute(
        """INSERT INTO items (id, issue_number, sha, subject, body, diff, github_url,
                              status, ci_run_url, error, labels, created_at)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
        (
            item["id"], item["issue_number"], item["sha"], item["subject"],
            item["body"], item["diff"], item["github_url"], item["status"],
            item["ci_run_url"], item["error"], json.dumps(item["labels"]),
            item["created_at"],
        ),
    )
    db.commit()


def db_get_item(item_id):
    """Get an item by ID. Returns dict or None."""
    db = get_db()
    row = db.execute("SELECT * FROM items WHERE id = ?", (item_id,)).fetchone()
    return _row_to_item(row) if row else None


def db_remove_item(item_id):
    """Remove an item (cascades to reservations)."""
    db = get_db()
    db.execute("DELETE FROM items WHERE id = ?", (item_id,))
    db.commit()


_ITEM_COLUMNS = {"status", "sha", "subject", "body", "diff", "github_url", "ci_run_url", "error", "labels"}


def db_update_item(item_id, **kwargs):
    """Update specific fields on an item."""
    for k in kwargs:
        if k not in _ITEM_COLUMNS:
            raise ValueError(f"invalid column: {k}")
    if "labels" in kwargs:
        kwargs["labels"] = json.dumps(kwargs["labels"])
    db = get_db()
    sets = ", ".join(f"{k} = ?" for k in kwargs)
    vals = list(kwargs.values()) + [item_id]
    db.execute(f"UPDATE items SET {sets} WHERE id = ?", vals)
    db.commit()


def db_count_items():
    """Count total items in the queue."""
    db = get_db()
    return db.execute("SELECT COUNT(*) FROM items").fetchone()[0]


def db_all_items():
    """Get all items ordered by creation time."""
    db = get_db()
    rows = db.execute("SELECT * FROM items ORDER BY created_at").fetchall()
    return [_row_to_item(r) for r in rows]


def db_reserve(item_id, user_id, user_email):
    """Create or refresh a reservation."""
    db = get_db()
    db.execute(
        """INSERT OR REPLACE INTO reservations (item_id, user_id, user_email, reserved_at)
           VALUES (?, ?, ?, ?)""",
        (item_id, user_id, user_email, time.time()),
    )
    db.commit()


def db_get_reservation(item_id):
    """Get reservation for an item. Returns dict with user_id/user_email or None."""
    db = get_db()
    row = db.execute("SELECT * FROM reservations WHERE item_id = ?", (item_id,)).fetchone()
    return dict(row) if row else None


def db_release_reservation(item_id):
    """Release a reservation."""
    db = get_db()
    db.execute("DELETE FROM reservations WHERE item_id = ?", (item_id,))
    db.commit()


def db_get_user_reservation(user_id):
    """Get the active (non-expired) reservation for a user. Returns (item_id, reserved_at) or None."""
    db = get_db()
    cutoff = time.time() - RESERVATION_TIMEOUT
    row = db.execute(
        """SELECT r.item_id, r.reserved_at FROM reservations r
           JOIN items i ON r.item_id = i.id
           WHERE r.user_id = ? AND r.reserved_at > ?""",
        (user_id, cutoff),
    ).fetchone()
    return (row["item_id"], row["reserved_at"]) if row else None


def db_reserve_next_for_user(user_id, user_email):
    """Find the next available item for a user and reserve it. Returns item dict or None.

    Skips items that are reserved by others or on cooldown for this user.
    """
    db = get_db()
    now = time.time()
    cutoff = now - RESERVATION_TIMEOUT

    # Clean up expired reservations (but not for in-flight approve/reject) and cooldowns
    db.execute(
        """DELETE FROM reservations WHERE reserved_at <= ?
           AND item_id NOT IN (SELECT id FROM items WHERE status IN ('approving', 'rejecting'))""",
        (cutoff,),
    )
    db.execute("DELETE FROM cooldowns WHERE until <= ?", (now,))
    # Commit closes the implicit transaction so we can start an explicit one.
    db.commit()

    # Atomic select+insert to prevent race conditions with ThreadingMixIn
    db.execute("BEGIN IMMEDIATE")
    try:
        row = db.execute(
            """SELECT i.* FROM items i
               WHERE i.status IN ('ready', 'failed')
                 AND i.id NOT IN (SELECT item_id FROM reservations)
                 AND i.id NOT IN (
                     SELECT item_id FROM cooldowns WHERE user_id = ? AND until > ?
                 )
               ORDER BY i.created_at
               LIMIT 1""",
            (user_id, now),
        ).fetchone()

        if row:
            item = _row_to_item(row)
            # Release any existing reservation for this user (one reservation at a time)
            db.execute("DELETE FROM reservations WHERE user_id = ?", (user_id,))
            db.execute(
                """INSERT INTO reservations (item_id, user_id, user_email, reserved_at)
                   VALUES (?, ?, ?, ?)""",
                (item["id"], user_id, user_email, time.time()),
            )
            db.commit()
            return item
        db.commit()
        return None
    except Exception:
        db.rollback()
        raise


def db_add_cooldown(user_id, item_id):
    """Add a 48h cooldown preventing this user from seeing this item."""
    db = get_db()
    db.execute(
        """INSERT OR REPLACE INTO cooldowns (user_id, item_id, until)
           VALUES (?, ?, ?)""",
        (user_id, item_id, time.time() + COOLDOWN_DURATION),
    )
    db.commit()


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def run_cmd(cmd, *, check=True, capture=True, cwd=None, timeout=None):
    """Run a command and return stdout (stripped)."""
    r = subprocess.run(
        cmd, capture_output=capture, text=True, cwd=cwd, timeout=timeout
    )
    if check and r.returncode != 0:
        stderr = r.stderr if capture else ""
        raise subprocess.CalledProcessError(r.returncode, cmd, r.stdout, stderr)
    return r.stdout.strip() if capture else ""


def git(*args, cwd=None, check=True):
    return run_cmd(["git", *args], cwd=cwd, check=check)


def gh(*args, check=True):
    return run_cmd(["gh", *args], check=check)


def slugify(text):
    s = text.lower()
    s = re.sub(r"[^a-z0-9._-]+", "_", s)
    s = s.strip("_.-")
    return s[:60]


def gen_id():
    return hashlib.sha256(os.urandom(16)).hexdigest()[:8]


def send_alert(subject, body):
    """Send an alert email via the exe.dev gateway. Best-effort, never raises."""
    try:
        data = json.dumps({"to": ALERT_EMAIL, "subject": subject, "body": body}).encode()
        req = urllib.request.Request(
            GATEWAY_EMAIL_URL, data=data,
            headers={"Content-Type": "application/json"},
        )
        urllib.request.urlopen(req, timeout=10)
        log.info("alert email sent: %s", subject)
    except Exception:
        log.warning("failed to send alert email", exc_info=True)


def read_prompt(name, **kwargs):
    """Read a prompt template and substitute placeholders.

    The preamble (prompts/preamble.md) is automatically prepended.
    """
    preamble_path = os.path.join(SCRIPT_DIR, "prompts", "preamble.md")
    path = os.path.join(SCRIPT_DIR, "prompts", name)
    with open(preamble_path) as f:
        preamble = f.read()
    with open(path) as f:
        text = f.read()
    text = preamble + "\n\n" + text
    for k, v in kwargs.items():
        text = text.replace(f"{{{k}}}", str(v))
    return text


def run_claude(prompt, *, cwd=None, timeout=CLAUDE_TIMEOUT):
    """Run claude CLI with the given prompt. Returns stdout."""
    log.info("running claude in %s (prompt: %d chars)", cwd or ".", len(prompt))
    return run_cmd(
        [*CLAUDE_CMD, "-p", prompt],
        cwd=cwd,
        timeout=timeout,
    )


# ---------------------------------------------------------------------------
# CI queue integration (mirrors bin/q logic)
# ---------------------------------------------------------------------------


def find_run(branch, commit, timeout=CI_FIND_TIMEOUT, interval=3):
    """Poll until a workflow run appears for the given branch+commit."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            out = gh(
                "run", "list",
                "--repo", CODE_REPO,
                "--branch", branch,
                "--workflow", "queue-main.yml",
                "--limit", "10",
                "--json", "headSha,databaseId",
                check=False,
            )
            if out:
                for r in json.loads(out):
                    if r.get("headSha") == commit:
                        return r["databaseId"]
        except (json.JSONDecodeError, KeyError):
            pass
        time.sleep(interval)
    return None


def poll_run(run_id, timeout=CI_TIMEOUT, interval=CI_POLL_INTERVAL):
    """Poll a run until completion. Returns (conclusion, url)."""
    t0 = time.monotonic()
    while time.monotonic() - t0 < timeout:
        out = gh(
            "run", "view", str(run_id),
            "--repo", CODE_REPO,
            "--json", "status,conclusion,url",
            check=False,
        )
        try:
            data = json.loads(out)
        except (json.JSONDecodeError, TypeError):
            data = {}
        if data.get("status") == "completed":
            return data.get("conclusion", "failure"), data.get("url", "")
        elapsed = int(time.monotonic() - t0)
        m, s = divmod(elapsed, 60)
        log.info("CI run %s: running [%dm%02ds]", run_id, m, s)
        time.sleep(interval)
    return "timeout", ""


def push_and_wait_ci(sha, branch, *, cwd=None):
    """Push sha to branch, find CI run, poll to completion. Returns (conclusion, url)."""
    git("push", "-f", "origin", f"{sha}:refs/heads/{branch}", cwd=cwd)
    log.info("pushed %s to %s, waiting for CI run...", sha[:7], branch)

    commit = git("rev-parse", sha, cwd=cwd)
    run_id = find_run(branch, commit)
    if not run_id:
        log.error("no CI run found for %s on %s after %ds", sha[:7], branch, CI_FIND_TIMEOUT)
        return "no_run", ""

    url = f"https://github.com/{CODE_REPO}/actions/runs/{run_id}"
    log.info("CI run started: %s", url)

    conclusion, run_url = poll_run(run_id)
    log.info("CI run %s: %s", run_id, conclusion)
    return conclusion, run_url or url


# ---------------------------------------------------------------------------
# Worktree management
# ---------------------------------------------------------------------------


def create_worktree(commit_id):
    """Create a git worktree for isolated work. Returns worktree path."""
    wt_path = f"/tmp/bored-wt-{commit_id}"
    if os.path.exists(wt_path):
        git("worktree", "remove", "--force", wt_path, cwd=EXE_REPO, check=False)
        if os.path.exists(wt_path):
            shutil.rmtree(wt_path)
    git("worktree", "prune", cwd=EXE_REPO)
    git("worktree", "add", "--detach", wt_path, "origin/main", cwd=EXE_REPO)
    return wt_path


def cleanup_worktree(wt_path):
    """Remove a worktree."""
    try:
        git("worktree", "remove", "--force", wt_path, cwd=EXE_REPO)
    except Exception:
        # Worktree may have already been cleaned up
        if os.path.exists(wt_path):
            shutil.rmtree(wt_path)


def copy_skills(wt_path):
    """Copy reviewing-code and autorefine skills into the worktree."""
    skills_src = os.path.expanduser("~/.claude/skills")
    skills_dst = os.path.join(wt_path, ".claude", "skills")
    for skill in ["reviewing-code", "autorefine"]:
        src = os.path.join(skills_src, skill)
        dst = os.path.join(skills_dst, skill)
        if os.path.isdir(src):
            shutil.copytree(src, dst, dirs_exist_ok=True,
                            ignore=shutil.ignore_patterns("__pycache__"))


# ---------------------------------------------------------------------------
# Background worker: commit generation pipeline
# ---------------------------------------------------------------------------


def gardening_pass(queued_issues=None):
    """Run a gardening pass across all open issues. Returns issue number (int) or 0."""
    git("fetch", "origin", cwd=EXE_REPO)
    git("reset", "--hard", "origin/main", cwd=EXE_REPO)

    skip_note = ""
    if queued_issues:
        skip_note = (
            "\n\nThe following issues are already queued for review — "
            "do not select them: "
            + ", ".join(f"#{n}" for n in sorted(queued_issues))
            + "\n"
        )
    prompt = read_prompt("gardening.md", queued_issues_note=skip_note)
    output = run_claude(prompt, cwd=EXE_REPO)

    # Extract issue number from the last non-empty line of output.
    # The prompt instructs Claude to put just "#N" on the last line,
    # but the full output may contain other #N references.
    for line in reversed(output.strip().splitlines()):
        m = re.match(r'^#?(\d+)$', line.strip())
        if m:
            return int(m.group(1))
    return 0


def check_gardening_closures():
    """Check if any queued items' issues were closed by gardening. Remove them."""
    for item in db_all_items():
        if item.get("status") in ("approving", "rejecting"):
            continue
        issue_num = item.get("issue_number")
        if issue_num and _is_issue_closed(issue_num):
            log.info("issue #%d was closed, removing item %s", issue_num, item["id"])
            db_remove_item(item["id"])


def _is_issue_closed(issue_number):
    """Check if an issue is closed."""
    try:
        out = gh(
            "issue", "view", str(issue_number),
            "--repo", ISSUES_REPO,
            "--json", "state",
            check=False,
        )
        data = json.loads(out) if out else {}
        return data.get("state") == "CLOSED"
    except Exception as e:
        log.warning("failed to check issue #%d: %s", issue_number, e)
        return False


def fetch_issue_labels(issue_number):
    """Fetch labels for an issue. Returns list of label names."""
    try:
        out = gh(
            "issue", "view", str(issue_number),
            "--repo", ISSUES_REPO,
            "--json", "labels",
            check=False,
        )
        data = json.loads(out) if out else {}
        return [l["name"] for l in data.get("labels", [])]
    except Exception as e:
        log.warning("failed to fetch labels for issue #%d: %s", issue_number, e)
        return []


def fix_issue(issue_number, wt_path):
    """Run claude to fix an issue in the worktree. Returns True if a commit was created."""
    prompt = read_prompt("fix-issue.md", issue_number=issue_number, repo=ISSUES_REPO)
    run_claude(prompt, cwd=wt_path)

    # Check if a commit was actually created
    try:
        main_sha = git("rev-parse", "origin/main", cwd=wt_path)
        head_sha = git("rev-parse", "HEAD", cwd=wt_path)
        if main_sha == head_sha:
            return False
        # Enforce single-commit contract: squash if Claude created multiple commits
        count = int(git("rev-list", "--count", "origin/main..HEAD", cwd=wt_path))
        if count > 1:
            log.warning("fix_issue produced %d commits, squashing to one", count)
            original_head = git("rev-parse", "HEAD", cwd=wt_path)
            git("reset", "--soft", "origin/main", cwd=wt_path)
            # Uses only the last commit's message; that's fine because
            # update_commit_message rewrites it from the diff anyway.
            git("commit", "-C", original_head, cwd=wt_path)
        return True
    except Exception:
        return False


def autorefine(wt_path):
    """Run the autorefine skill in the worktree. Returns the autorefine summary."""
    copy_skills(wt_path)
    try:
        return run_claude("autorefine", cwd=wt_path)
    finally:
        skills_dir = os.path.join(wt_path, ".claude", "skills")
        if os.path.isdir(skills_dir):
            shutil.rmtree(skills_dir)


def update_commit_message(wt_path, autorefine_summary):
    """Run claude to update the commit message with refinement insights."""
    prompt = read_prompt("refine-message.md", autorefine_summary=autorefine_summary)
    run_claude(prompt, cwd=wt_path)


def ci_qualify(commit_id, wt_path):
    """Run CI qualification (CI ONLY push). Returns (success, sha, ci_url) or (False, None, url)."""
    # Capture full message with %B for exact round-trip (%s/%b split loses whitespace)
    full_message = git("--no-pager", "log", "--format=%B", "-n", "1", "HEAD", cwd=wt_path)
    subject = full_message.split("\n", 1)[0]

    # Amend with CI ONLY prefix
    ci_message = "CI ONLY: " + full_message
    git("commit", "--amend", "-m", ci_message, cwd=wt_path)

    ci_sha = git("rev-parse", "HEAD", cwd=wt_path)
    branch = f"queue-main-bored-cionly-{commit_id}-{slugify(subject)}"

    conclusion, url = push_and_wait_ci(ci_sha, branch, cwd=wt_path)

    # Clean up the CI-only branch regardless of outcome (CI may have already deleted it)
    try:
        git("push", "origin", "--delete", branch, cwd=wt_path)
    except Exception as e:
        log.debug("CI-only branch %s already deleted or failed to delete: %s", branch, e)

    if conclusion == "success":
        # Restore original message exactly
        git("commit", "--amend", "-m", full_message, cwd=wt_path)
        final_sha = git("rev-parse", "HEAD", cwd=wt_path)
        return True, final_sha, url
    else:
        return False, None, url


def push_hidden_ref(commit_id, sha, wt_path):
    """Push a qualified commit to a hidden ref."""
    git("push", "origin", f"{sha}:refs/bored/{commit_id}", cwd=wt_path)
    log.info("pushed %s to refs/bored/%s", sha[:7], commit_id)


def post_ci_failure(issue_number, ci_url):
    """Post CI failure to the issue."""
    body = f"Bored CI qualification failed.\n\nCI run: {ci_url}\n\n{WHENCE}"
    try:
        gh(
            "issue", "comment", str(issue_number),
            "--repo", ISSUES_REPO,
            "--body", body,
        )
    except Exception as e:
        log.warning("failed to post CI failure to issue #%d: %s", issue_number, e)


def generate_commit():
    """Full pipeline: gardening -> fix -> refine -> CI qualify. Returns item dict or None."""
    commit_id = gen_id()
    log.info("starting commit generation (id=%s)", commit_id)

    # Step 1: Gardening pass
    log.info("running gardening pass")
    queued_issues = {item["issue_number"] for item in db_all_items()}
    issue_number = gardening_pass(queued_issues)

    # Check if gardening closed any of our items
    check_gardening_closures()

    if issue_number == 0:
        log.info("no issues to work on")
        return NO_ISSUES

    # Skip if this issue is already queued (gardening was told to skip these,
    # but double-check in case it ignored the instruction).
    # Re-query: check_gardening_closures may have removed items since the snapshot.
    if issue_number in {item["issue_number"] for item in db_all_items()}:
        log.info("issue #%d already queued", issue_number)
        return ISSUE_ALREADY_QUEUED

    log.info("working on issue #%d", issue_number)

    # Fetch labels for display purposes
    labels = fetch_issue_labels(issue_number)

    # Step 2: Fix the issue in a worktree
    wt_path = None
    try:
        git("fetch", "origin", cwd=EXE_REPO)
        wt_path = create_worktree(commit_id)

        log.info("fixing issue #%d in %s", issue_number, wt_path)
        if not fix_issue(issue_number, wt_path):
            log.warning("no commit produced for issue #%d", issue_number)
            return None

        # Step 3: Autorefine
        log.info("running autorefine")
        autorefine_summary = autorefine(wt_path)

        # Step 4: Update commit message
        log.info("updating commit message")
        update_commit_message(wt_path, autorefine_summary)

        # Capture commit info before CI qualification. The amend/restore cycle
        # changes the SHA but preserves the tree and message, so these are stable.
        subject = git("--no-pager", "log", "--format=%s", "-n", "1", "HEAD", cwd=wt_path)
        body = git("--no-pager", "log", "--format=%b", "-n", "1", "HEAD", cwd=wt_path)
        diff = git("--no-pager", "diff", "origin/main..HEAD", cwd=wt_path)

        # Step 5: CI qualification
        log.info("running CI qualification")
        passed, sha, ci_url = ci_qualify(commit_id, wt_path)

        if not passed:
            log.warning("CI qualification failed for issue #%d: %s", issue_number, ci_url)
            post_ci_failure(issue_number, ci_url)
            return None

        # Push to hidden ref
        push_hidden_ref(commit_id, sha, wt_path)

        github_url = f"https://github.com/{CODE_REPO}/commit/{sha}"
        log.info("commit %s qualified and pushed (issue #%d)", commit_id, issue_number)

        return {
            "id": commit_id,
            "issue_number": issue_number,
            "sha": sha,
            "subject": subject,
            "body": body,
            "diff": diff,
            "github_url": github_url,
            "status": "ready",
            "ci_run_url": None,
            "error": None,
            "labels": labels,
            "created_at": time.time(),
        }

    except Exception as e:
        log.exception("failed to generate commit for issue #%d: %s", issue_number, e)
        return None
    finally:
        if wt_path:
            cleanup_worktree(wt_path)


def background_worker():
    """Main background worker loop. Keeps the queue filled to MAX_ITEMS."""
    log.info("background worker started")
    last_gardening_empty = 0  # timestamp when gardening last returned nothing
    consecutive_failures = 0
    alerted = False

    while True:
        try:
            if db_count_items() < MAX_ITEMS:
                if time.time() - last_gardening_empty < GARDENING_BACKOFF:
                    log.debug("gardening backoff, skipping iteration")
                else:
                    # generate_commit returns: dict (success), None (failure),
                    # NO_ISSUES (nothing to work on), or ISSUE_ALREADY_QUEUED.
                    item = generate_commit()
                    if item is NO_ISSUES:
                        last_gardening_empty = time.time()
                    elif item is ISSUE_ALREADY_QUEUED:
                        # Transient — gardening picked an already-queued issue despite
                        # the skip list. Other issues may be available; no backoff.
                        log.info("gardening picked an already-queued issue, retrying")
                    elif item and item.get("id"):
                        db_add_item(item)
                        log.info("added item %s to queue (now %d items)", item["id"], db_count_items())
                    # else: item is None (failure), retry next cycle without backoff
            consecutive_failures = 0
            alerted = False
        except Exception:
            log.exception("worker iteration failed")
            consecutive_failures += 1
            if consecutive_failures >= CONSECUTIVE_FAILURE_THRESHOLD and not alerted:
                tb = traceback.format_exc()
                send_alert(
                    "bored: worker failing repeatedly",
                    f"{consecutive_failures} consecutive failures.\n\nLatest:\n{tb[-1000:]}",
                )
                alerted = True

        time.sleep(WORKER_SLEEP)


# ---------------------------------------------------------------------------
# Approve / Reject / Skip flows
# ---------------------------------------------------------------------------


def _set_approval_trailers(wt_path, user_email):
    """Amend HEAD with Approved-By and Approved-At trailers.

    Uses git interpret-trailers with --if-exists replace so repeated calls
    (retries) update rather than duplicate. The changing timestamp ensures
    a new SHA on each attempt.
    """
    msg = git("--no-pager", "log", "--format=%B", "-n", "1", "HEAD", cwd=wt_path)
    now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    r = subprocess.run(
        ["git", "interpret-trailers",
         "--if-exists", "replace",
         "--trailer", f"Approved-By: {user_email}",
         "--trailer", f"Approved-At: {now}"],
        input=msg, capture_output=True, text=True, cwd=wt_path,
        check=True,
    )
    amend_cmd = ["commit", "--amend", "-m", r.stdout.strip()]
    author = APPROVER_AUTHORS.get(user_email)
    if author:
        name, first = author
        amend_cmd += ["--author", f"{name} (bored) <{first}@bored.exe.xyz>"]
    git(*amend_cmd, cwd=wt_path)


def approve_commit(commit_id, user_email):
    """Push commit through queue-main for real merge."""
    item = db_get_item(commit_id)
    if not item:
        return

    # Status already set to "approving" atomically by the handler.
    subject = item["subject"]
    branch = f"queue-main-bored-{commit_id}-{slugify(subject)}"

    # Check out the commit, add approval trailers, push
    wt_path = None
    try:
        wt_path = create_worktree(f"approve-{commit_id}")
        git("fetch", "origin", f"refs/bored/{commit_id}", cwd=wt_path)
        git("checkout", "FETCH_HEAD", cwd=wt_path)

        _set_approval_trailers(wt_path, user_email)
        sha = git("rev-parse", "HEAD", cwd=wt_path)

        # Update hidden ref with trailered commit
        git("push", "-f", "origin", f"{sha}:refs/bored/{commit_id}", cwd=wt_path)

        conclusion, ci_url = push_and_wait_ci(sha, branch, cwd=wt_path)

        if conclusion == "success":
            log.info("commit %s approved and merged", commit_id)
            try:
                git("push", "origin", "--delete", f"refs/bored/{commit_id}", cwd=wt_path)
            except Exception as e:
                log.warning("failed to delete ref refs/bored/%s: %s", commit_id, e)
            db_remove_item(commit_id)
        else:
            log.warning("approve CI failed for %s: %s", commit_id, ci_url)

            # Clean up the queue branch to prevent Ralph interference
            try:
                git("push", "origin", "--delete", branch, cwd=wt_path)
            except Exception as e:
                log.warning("failed to delete queue branch %s: %s", branch, e)

            # Re-amend trailers with fresh timestamp for retry (new SHA = cache bust)
            _set_approval_trailers(wt_path, user_email)
            new_sha = git("rev-parse", "HEAD", cwd=wt_path)
            git("push", "-f", "origin", f"{new_sha}:refs/bored/{commit_id}", cwd=wt_path)

            # Intentionally keep reservation: the original reviewer retries their
            # own failure rather than passing a broken item to someone else.
            # Refresh the reservation timestamp so it doesn't expire during long CI runs.
            res = db_get_reservation(commit_id)
            if res:
                db_reserve(commit_id, res["user_id"], res["user_email"])
            db_update_item(
                commit_id,
                status="failed",
                ci_run_url=ci_url,
                sha=new_sha,
                github_url=f"https://github.com/{CODE_REPO}/commit/{new_sha}",
                error=f"CI failed: {ci_url}",
            )
    except Exception as e:
        log.exception("failed during approve for %s: %s", commit_id, e)
        db_update_item(
            commit_id,
            status="failed",
            ci_run_url="",
            error=str(e),
        )
    finally:
        if wt_path:
            cleanup_worktree(wt_path)


def reject_commit(commit_id, reason, user_email):
    """Post rejection to issue, remove item from queue."""
    try:
        item = db_get_item(commit_id)
        if not item:
            return

        issue_number = item.get("issue_number")

        if issue_number:
            ref = f"refs/bored/{commit_id}"
            # reason is already str-coerced and truncated at the handler level.
            body = (
                f"**Rejected by {user_email}**\n\n"
                f"```\n{reason}\n```\n\n"
                f"Commit ref: `{ref}` (SHA: `{item['sha']}`)\n\n"
                f"{WHENCE}"
            )
            try:
                gh(
                    "issue", "comment", str(issue_number),
                    "--repo", ISSUES_REPO,
                    "--body", body,
                )
            except Exception as e:
                log.warning("failed to post rejection to issue #%d: %s", issue_number, e)

            # Clean up hidden ref for rejected commits (defense against prompt injection).
            # cwd=EXE_REPO is safe here: git push --delete only reads .git/config
            # for the remote URL and doesn't touch the index or working tree.
            deleted = False
            for attempt in range(3):
                try:
                    git("push", "origin", "--delete", f"refs/bored/{commit_id}", cwd=EXE_REPO)
                    deleted = True
                    break
                except Exception as e:
                    log.warning("failed to delete ref refs/bored/%s (attempt %d): %s", commit_id, attempt + 1, e)
                    if attempt < 2:
                        time.sleep(2 ** attempt)
            if not deleted:
                log.error("giving up on ref deletion for refs/bored/%s", commit_id)

        db_remove_item(commit_id)
        log.info("commit %s rejected by %s", commit_id, user_email)
    except Exception:
        log.exception("reject_commit failed for %s, removing item", commit_id)
        db_remove_item(commit_id)


def skip_commit(commit_id, user_id):
    """Release reservation and add 48h cooldown so this user won't see the item again.

    Only allowed when the item is in a skippable state (ready or failed).
    Returns True if skipped, False if the item is in a non-skippable state.
    """
    db = get_db()
    item = db.execute("SELECT status FROM items WHERE id = ?", (commit_id,)).fetchone()
    if item and item["status"] not in ("ready", "failed"):
        return False
    db_release_reservation(commit_id)
    db_add_cooldown(user_id, commit_id)
    log.info("commit %s skipped by user %s", commit_id, user_id)
    return True


# ---------------------------------------------------------------------------
# HTTP Server
# ---------------------------------------------------------------------------


class BoredHandler(http.server.BaseHTTPRequestHandler):
    """HTTP request handler for the bored API and UI."""

    def log_message(self, format, *args):
        log.info("HTTP %s", format % args)

    def _send_json(self, data, status=200):
        body = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _send_error(self, status, message):
        self._send_json({"error": message}, status)

    def _read_body(self):
        """Read and parse JSON body. Sends an error response and returns None on failure."""
        length = int(self.headers.get("Content-Length", 0))
        if length > 64 * 1024:
            self._send_error(413, "request body too large")
            return None
        if length:
            try:
                return json.loads(self.rfile.read(length))
            except json.JSONDecodeError:
                self._send_error(400, "invalid JSON")
                return None
        return {}

    def _user(self):
        """Extract user identity from gateway auth headers."""
        user_id = self.headers.get("X-ExeDev-UserID", "")
        email = self.headers.get("X-ExeDev-Email", "")
        # Reject newlines to prevent trailer/header injection
        if "\n" in user_id or "\r" in user_id or "\n" in email or "\r" in email:
            return ("", "")
        return (user_id, email)

    def _check_reservation(self, commit_id, user_id):
        """Verify user holds a non-expired reservation for this item. Sends 403 and returns False if not."""
        res = db_get_reservation(commit_id)
        if not res or res["user_id"] != user_id:
            self._send_error(403, "you do not hold a reservation for this item")
            return False
        if time.time() - res["reserved_at"] > RESERVATION_TIMEOUT:
            self._send_error(403, "reservation has expired")
            return False
        return True

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path

        if path == "/api/state":
            user_id, user_email = self._user()
            if not user_id:
                self._send_error(401, "authentication required")
                return

            commit = None
            reservation_expires_at = None

            # Check for existing active reservation
            existing = db_get_user_reservation(user_id)
            if existing:
                item_id, reserved_at = existing
                commit = db_get_item(item_id)
                if commit:
                    # Refresh reservation — user is still on the page
                    try:
                        db_reserve(item_id, user_id, user_email)
                    except sqlite3.IntegrityError:
                        commit = None  # item deleted between get and reserve
                    else:
                        reservation_expires_at = time.time() + RESERVATION_TIMEOUT

            # No active reservation (or item was deleted) — reserve next available
            if not commit:
                commit = db_reserve_next_for_user(user_id, user_email)
                if commit:
                    reservation_expires_at = time.time() + RESERVATION_TIMEOUT

            self._send_json({
                "commit": commit,
                "user": {"email": user_email, "id": user_id},
                "queue_depth": db_count_items(),
                "reservation_expires_at": reservation_expires_at,
            })
            return

        # Unknown API paths return 404 JSON, not SPA HTML
        if path.startswith("/api/"):
            self._send_error(404, "not found")
            return

        # Serve static files from ui/dist/
        self._serve_static(path)

    def do_POST(self):
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path

        # POST /api/approve/<id>
        m = re.match(r"^/api/approve/([a-f0-9]+)$", path)
        if m:
            commit_id = m.group(1)
            if not db_get_item(commit_id):
                self._send_error(404, "commit not found")
                return

            user_id, user_email = self._user()
            if not user_id:
                self._send_error(401, "authentication required")
                return
            if not user_email:
                self._send_error(401, "email required for approval")
                return
            if not self._check_reservation(commit_id, user_id):
                return

            # Atomically claim the item for approval — only one request wins.
            db = get_db()
            db.execute("BEGIN IMMEDIATE")
            try:
                n = db.execute(
                    "UPDATE items SET status='approving' WHERE id=? AND status IN ('ready','failed')",
                    (commit_id,),
                ).rowcount
                db.commit()
            except Exception:
                db.rollback()
                raise
            if not n:
                item = db_get_item(commit_id)
                self._send_json({"status": item["status"] if item else "unknown"})
                return

            # Start approve in background thread
            t = threading.Thread(
                target=approve_commit,
                args=(commit_id, user_email),
                daemon=True,
            )
            t.start()
            self._send_json({"status": "approving"})
            return

        # POST /api/reject/<id>
        m = re.match(r"^/api/reject/([a-f0-9]+)$", path)
        if m:
            commit_id = m.group(1)
            if not db_get_item(commit_id):
                self._send_error(404, "commit not found")
                return

            user_id, user_email = self._user()
            if not user_id:
                self._send_error(401, "authentication required")
                return
            if not user_email:
                self._send_error(401, "email required for rejection")
                return
            if not self._check_reservation(commit_id, user_id):
                return

            body = self._read_body()
            if body is None:
                return
            if not isinstance(body, dict):
                self._send_error(400, "expected JSON object")
                return
            reason = str(body.get("reason", "(no reason given)"))[:2000]

            # Atomically claim the item for rejection — only one request wins.
            db = get_db()
            db.execute("BEGIN IMMEDIATE")
            try:
                n = db.execute(
                    "UPDATE items SET status='rejecting' WHERE id=? AND status IN ('ready','failed')",
                    (commit_id,),
                ).rowcount
                db.commit()
            except Exception:
                db.rollback()
                raise
            if not n:
                item = db_get_item(commit_id)
                self._send_json({"status": item["status"] if item else "unknown"})
                return

            # Reject in background thread
            t = threading.Thread(
                target=reject_commit,
                args=(commit_id, reason, user_email),
                daemon=True,
            )
            t.start()
            self._send_json({"status": "rejecting"})
            return

        # POST /api/skip/<id>
        m = re.match(r"^/api/skip/([a-f0-9]+)$", path)
        if m:
            commit_id = m.group(1)
            if not db_get_item(commit_id):
                self._send_error(404, "commit not found")
                return

            user_id, _ = self._user()
            if not user_id:
                self._send_error(401, "authentication required")
                return
            if not self._check_reservation(commit_id, user_id):
                return

            if not skip_commit(commit_id, user_id):
                item = db_get_item(commit_id)
                self._send_json({"status": item["status"] if item else "unknown"}, status=409)
                return
            self._send_json({"status": "skipped"})
            return

        self._send_error(404, "not found")

    def _serve_static(self, path):
        if path == "/" or path == "":
            path = "/index.html"

        # Security: prevent path traversal.
        # The startswith check rejects traversal attempts; os.path.isfile below
        # is the ultimate guard (rejects directories and nonexistent paths).
        path = path.lstrip("/")
        filepath = os.path.realpath(os.path.join(UI_DIST, path))
        if not filepath.startswith(os.path.realpath(UI_DIST) + os.sep):
            self._send_error(403, "forbidden")
            return
        if not os.path.isfile(filepath):
            # Fall back to index.html for SPA routing
            filepath = os.path.join(UI_DIST, "index.html")
            if not os.path.isfile(filepath):
                self._send_error(404, "not found")
                return

        content_type, _ = mimetypes.guess_type(filepath)
        if content_type is None:
            content_type = "application/octet-stream"

        try:
            with open(filepath, "rb") as f:
                data = f.read()
            self.send_response(200)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        except OSError:
            self._send_error(500, "internal server error")


class ThreadedHTTPServer(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    allow_reuse_address = True


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main():
    init_db()

    # Prevent dual-instance conflicts
    lock_path = os.path.join(STATE_DIR, "bored.lock")
    lock_fd = open(lock_path, "w")
    try:
        fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        log.error("another bored instance is already running (lock: %s)", lock_path)
        raise SystemExit(1)

    # Clean up stale worktrees from previous crashes
    try:
        git("worktree", "prune", cwd=EXE_REPO, check=False)
    except Exception:
        pass

    # Reset any items stuck in transient states from a previous crash
    db = get_db()
    stuck = db.execute("SELECT id, status FROM items WHERE status IN ('approving', 'rejecting')").fetchall()
    for row in stuck:
        log.info("resetting stale %s item %s to ready", row["status"], row["id"])
        db_update_item(row["id"], status="ready", error=None)

    # Start background worker
    worker = threading.Thread(target=background_worker, daemon=True)
    worker.start()
    log.info("background worker thread started")

    # Start HTTP server
    server = ThreadedHTTPServer(("", PORT), BoredHandler)
    log.info("HTTP server listening on port %d", PORT)

    try:
        server.serve_forever()
    except KeyboardInterrupt:
        log.info("shutting down")
        server.shutdown()


if __name__ == "__main__":
    main()
