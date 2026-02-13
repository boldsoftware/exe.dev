#!/usr/bin/env python3
"""Push Ralph's fix (if any) and notify Slack.

Usage: ralph-notify.py <branch> <commit_subject> <slack_id> <run_url> <before_sha> <ralph_run_url>

Checks if HEAD moved since <before_sha>. If it did, pushes to
refs/queue-ralph/<branch> and posts a success message. Otherwise
posts a "could not fix" message.

Environment: NTFY_SLACK_WEBHOOK_URL must be set.
"""

import json
import os
import subprocess
import sys
import urllib.request


def git(*args: str) -> str:
    return subprocess.check_output(["git", *args], text=True).strip()


def post_slack(webhook_url: str, text: str) -> None:
    payload = json.dumps({"text": text})
    req = urllib.request.Request(
        webhook_url,
        data=payload.encode(),
        headers={"Content-Type": "application/json"},
    )
    urllib.request.urlopen(req)


def main() -> None:
    branch, commit_subject, slack_id, run_url, before_sha, ralph_run_url = sys.argv[1:7]

    webhook_url = os.environ.get("NTFY_SLACK_WEBHOOK_URL", "").strip()
    if not webhook_url:
        print("NTFY_SLACK_WEBHOOK_URL must be set", file=sys.stderr)
        sys.exit(1)

    after_sha = git("rev-parse", "HEAD")

    mention = f"<@{slack_id}> " if slack_id else ""

    repo_url = run_url.rsplit("/actions/runs/", 1)[0]

    if before_sha and before_sha != after_sha:
        # Claude made a fix commit — push it.
        subprocess.check_call(["git", "push", "origin", f"HEAD:refs/queue-ralph/{branch}"])

        commit_msg = git("log", "-1", "--format=%B")
        commit_url = f"{repo_url}/commit/{after_sha}"

        message = (
            f'🤖 {mention}ralph has a proposed fix for "{commit_subject}"\n'
            f"\n"
            f"{commit_msg}\n"
            f"Failed CI run: {run_url}\n"
            f"Ralph run: {ralph_run_url}\n"
            f"Commit: {commit_url}\n"
            f"To review: `git fetch origin refs/queue-ralph/{branch} && git log -1 -p FETCH_HEAD`\n"
            f"To land: `git push origin FETCH_HEAD:refs/heads/{branch}`"
        )
    else:
        # Claude couldn't fix it.
        message = (
            f'🤖 {mention}ralph investigated "{commit_subject}" but could not auto-fix it.\n'
            f"\n"
            f"Failed CI run: {run_url}\n"
            f"Ralph run: {ralph_run_url}"
        )

    post_slack(webhook_url, message)


if __name__ == "__main__":
    main()
