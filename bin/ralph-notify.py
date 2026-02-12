#!/usr/bin/env python3
"""Push Ralph's fix (if any) and notify Slack.

Usage: ralph-notify.py <branch> <commit_subject> <slack_id> <run_url> <before_sha>

Checks if HEAD moved since <before_sha>. If it did, pushes to
refs/queue-ralph/<branch> and posts a success message. Otherwise
posts a "could not fix" message.

Environment: EXE_SLACK_BOT_TOKEN must be set.
"""

import os
import subprocess
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts", "slack"))
from client import SlackClient, ensure_token


def git(*args: str) -> str:
    return subprocess.check_output(["git", *args], text=True).strip()


def main() -> None:
    branch, commit_subject, slack_id, run_url, before_sha = sys.argv[1:6]

    after_sha = git("rev-parse", "HEAD")

    mention = f"<@{slack_id}> " if slack_id else ""

    token = ensure_token()
    slack = SlackClient(token)
    channel_id = slack.find_channel_id("ntfy")

    if before_sha != after_sha:
        # Claude made a fix commit — push it.
        subprocess.check_call(["git", "push", "origin", f"HEAD:refs/queue-ralph/{branch}"])

        commit_msg = git("log", "-1", "--format=%B")

        message = (
            f'🤖 {mention}ralph has a proposed fix for "{commit_subject}"\n'
            f"\n"
            f"{commit_msg}\n"
            f"To review: `git fetch origin refs/queue-ralph/{branch} && git log -1 -p FETCH_HEAD`\n"
            f"To land: `git push origin FETCH_HEAD:refs/heads/{branch}`"
        )
    else:
        # Claude couldn't fix it.
        message = (
            f'🤖 {mention}ralph investigated "{commit_subject}" but could not auto-fix it.\n'
            f"\n"
            f"CI run: {run_url}"
        )

    slack.post_message(channel_id, message)


if __name__ == "__main__":
    main()
